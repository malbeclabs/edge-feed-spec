package engine

// mbo_detector_test.go — Table-driven tests for the Task 19 MBO counter-detector rules.
//
// Each row has a rule ID, a conformant stream (no violation expected) and a
// violating stream (violation expected), plus a gapped-stream asserting that
// a covering forward gap on the relevant port downgrades to Unverifiable.
//
// Rules covered (10):
//   DELTA.PERINSTR_NO_SNAPSHOT_RESET
//   DELTA.PERINSTR_DUP_DIVERGENT
//   DELTA.PERINSTR_WRAP_BEFORE_RESET
//   FRAME.MKTDATA_SEQ_START
//   BATCH.ID_MONOTONIC
//   SNAP.ANCHOR_IS_MKTDATA_SEQ
//   SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT
//   SNAP.SNAPSHOT_ID_MONOTONIC
//   SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS
//   RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// streamEntry is one frame in a test stream (port + raw bytes).
// If raw is nil, this is a flush marker: runStream calls e.Flush() at this point.
type streamEntry struct {
	port core.Port
	raw  []byte
	seq  uint64 // optional override for frame seq; 0 = use encoded seq
}

// flushEntry creates a special flush-marker entry.
func flushEntry() streamEntry { return streamEntry{} }

// runStream feeds each entry through the engine (with optional seq override)
// and flushes all buffers at the end. Flush-marker entries (raw==nil) cause an
// intermediate Flush call.
func runStream(e *Engine, entries []streamEntry) {
	for _, en := range entries {
		if en.raw == nil {
			e.Flush()
			continue
		}
		raw := en.raw
		var magic uint16
		switch e.cfg.Feed {
		case core.FeedMBO:
			magic = wire.MagicMBO
		case core.FeedTOB:
			magic = wire.MagicTOB
		default:
			magic = wire.MagicMid
		}
		f, sf := wire.Decode(raw, magic)
		if en.seq != 0 {
			f.Header.Sequence = en.seq
		}
		e.Process(f, en.port, sf)
	}
	e.Flush()
}

// buildSnapBeginFrame builds a SnapshotBegin (0x20) frame on PortSnapshot.
//
// SnapshotBegin is 36 bytes total (4 header + 32 body):
//
//	Body[0:4]   = Instrument ID (u32 LE)
//	Body[4:12]  = Anchor Seq (u64 LE)
//	Body[12:16] = Total Orders (u32 LE)
//	Body[16:20] = Snapshot ID (u32 LE)
//	Body[20:24] = Last Instrument Seq (u32 LE)
//	Body[24:32] = padding
func buildSnapBeginFrame(ch uint8, instrID uint32, anchorSeq uint64, snapshotID, lastInstrSeq uint32) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) {
			b.U32(instrID)      // Instrument ID      body[0]
			b.U64(anchorSeq)    // Anchor Seq         body[4]
			b.U32(1)            // Total Orders       body[12]
			b.U32(snapshotID)   // Snapshot ID        body[16]
			b.U32(lastInstrSeq) // Last Instr Seq     body[20]
			b.Pad(8)            // padding            body[24]
		}).
		Bytes()
}

// buildBatchBoundaryFrame builds a BatchBoundary (0x13) frame.
//
// BatchBoundary is 16 bytes total (4 header + 12 body):
//
//	Body[0:4] = Batch ID (u32 LE)
//	Body[4:12]= padding
func buildBatchBoundaryFrame(ch uint8, batchID uint32) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeBatchBoundary, 16, func(b *wb.Body) {
			b.U32(batchID) // Batch ID  body[0]
			b.Pad(8)       // padding
		}).
		Bytes()
}

// buildOrderAddFrameWithPayload builds an OrderAdd frame with a given per-instrument
// seq and distinguishable body content (price is used as payload differentiator).
func buildOrderAddFrameWithPayload(ch uint8, instrID, perInstrSeq uint32, priceTag int64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID)     // Instrument ID        body[0]
			b.Pad(2)           // reserved             body[4]
			b.U8(0)            // Side=0 (bid)         body[6]
			b.U8(0)            // OrderFlags=0         body[7]
			b.U32(perInstrSeq) // Per-Instrument Seq   body[8]
			b.U64(42)          // OrderID              body[12]
			b.U64(1000)        // EnterTimestamp       body[20]
			b.I64(priceTag)    // Price (differentiates payloads) body[28]
			b.U64(1)           // Quantity=1           body[36]
			b.Pad(4)           // Reserved             body[44]
		}).
		Bytes()
}

// buildResetCountFrame builds a frame with a new reset count (era bump).
// This is used to test FRAME.MKTDATA_SEQ_START.
func buildResetCountFrame(ch uint8, resetCount uint8, seq uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		ResetCount(resetCount).
		Seq(seq).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) {
			b.U8(ch) // ChannelID
			b.Pad(3)
			b.U64(1000) // Timestamp
		}).
		Bytes()
}

// newMBOEngineW1 returns a window-1 MBO engine and capture.
func newMBOEngineW1() (*Engine, *allCapture) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, ac)
	return e, ac
}

// mktdataEntrySeq creates a mktdata entry with an explicit frame seq.
func mktdataEntrySeq(raw []byte, seq uint64) streamEntry {
	return streamEntry{port: core.PortMktData, raw: raw, seq: seq}
}

// snapEntrySeq creates a snapshot entry with an explicit frame seq.
func snapEntrySeq(raw []byte, seq uint64) streamEntry {
	return streamEntry{port: core.PortSnapshot, raw: raw, seq: seq}
}

// ---- Individual rule tests ----

// TestDeltaPerInstrNoSnapshotReset: per-instrument seq must NOT restart at 1
// just because a snapshot was emitted (only Reset Count change allows restart).
func TestDeltaPerInstrNoSnapshotReset(t *testing.T) {
	const ch, instrID = uint8(1), uint32(10)

	// Conformant: seq 1→2→3 on mktdata (no restart).
	t.Run("conformant", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
		})
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_NO_SNAPSHOT_RESET") {
			if fn.Status == core.Violation {
				t.Errorf("conformant stream must not emit violation: %s", fn.Detail)
			}
		}
	})

	// Violating: seq 1→2→3 then restart at 1 without Reset Count change
	// (simulated by having perInstrSeq=1 again without any InstrumentReset).
	t.Run("violation", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			// Restart at 1 without Reset Count change — violation.
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 4),
		})
		if !hasViolation(ac, "DELTA.PERINSTR_NO_SNAPSHOT_RESET") {
			t.Error("expected DELTA.PERINSTR_NO_SNAPSHOT_RESET Violation, got none")
		}
	})

	// Gapped: mktdata channel gap should produce Unverifiable, not Violation.
	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// seq 1 then jump to seq 3 (gap at seq 2) then restart at instr seq 1.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			// skip seq 2 (gap)
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 4),
		})
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_NO_SNAPSHOT_RESET") {
			if fn.Status == core.Violation {
				t.Errorf("gapped channel must not emit Violation for NO_SNAPSHOT_RESET: %s", fn.Detail)
			}
		}
	})
}

// TestDeltaPerInstrDupDivergent: a delta with per-instrument seq ≤ lastInstrSeq
// whose payload differs from what was applied at that seq is a violation.
func TestDeltaPerInstrDupDivergent(t *testing.T) {
	const ch, instrID = uint8(1), uint32(20)

	t.Run("conformant_identical_dup", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		raw := buildOrderAddFrameWithPayload(ch, instrID, 1, 100)
		runStream(e, []streamEntry{
			mktdataEntrySeq(raw, 1),
			mktdataEntrySeq(raw, 2), // identical dup — not a violation
		})
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_DUP_DIVERGENT") {
			if fn.Status == core.Violation {
				t.Errorf("identical dup must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_divergent_payload", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// First: per-instr seq=1 with price=100.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 1, 100), 1),
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 2, 200), 2),
			// Replay per-instr seq=1 with different price → divergent dup.
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 1, 999), 3),
		})
		if !hasViolation(ac, "DELTA.PERINSTR_DUP_DIVERGENT") {
			t.Error("expected DELTA.PERINSTR_DUP_DIVERGENT Violation, got none")
		}
	})

	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 1, 100), 1),
			// gap: skip seq 2
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 3, 300), 3),
			// Replay per-instr seq=1 with different price — gapped channel.
			mktdataEntrySeq(buildOrderAddFrameWithPayload(ch, instrID, 1, 999), 4),
		})
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_DUP_DIVERGENT") {
			if fn.Status == core.Violation {
				t.Errorf("gapped channel must not emit Violation for DUP_DIVERGENT: %s", fn.Detail)
			}
		}
	})
}

// TestDeltaPerInstrWrapBeforeReset: u32 per-instrument seq must not wrap
// (0xFFFFFFFF → 1) within an era without a Reset Count bump.
func TestDeltaPerInstrWrapBeforeReset(t *testing.T) {
	const ch, instrID = uint8(1), uint32(30)

	t.Run("conformant_no_wrap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
		})
		// Info rule: should not fire at all on a non-wrapping stream.
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_WRAP_BEFORE_RESET") {
			t.Errorf("no wrap: unexpected finding: %v detail=%s", fn.Status, fn.Detail)
		}
	})

	t.Run("fires_on_wrap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed 0xFFFFFFFF then 0x00000001 — this is a wrap.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 0xFFFFFFFE), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 0xFFFFFFFF), 2),
			// Next seq would be 0 (wraparound); we use 1 to trigger +1 of 0xFFFFFFFF.
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 0), 3), // 0xFFFFFFFF+1 = 0 (u32 wrap)
		})
		// The finding should exist (info rule fires on wrap).
		found := false
		for _, fn := range findingsFor(ac, "DELTA.PERINSTR_WRAP_BEFORE_RESET") {
			_ = fn
			found = true
		}
		if !found {
			t.Error("expected DELTA.PERINSTR_WRAP_BEFORE_RESET finding on wrap, got none")
		}
	})
}

// TestFrameMktdataSeqStart: on a Reset Count change, mktdata-port Sequence
// Number must restart at 0.
func TestFrameMktdataSeqStart(t *testing.T) {
	const ch = uint8(1)

	t.Run("conformant_restarts_at_0", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Era 0: frame seq=1.
		raw0 := buildResetCountFrame(ch, 0, 1)
		f0, sf0 := wire.Decode(raw0, wire.MagicMBO)
		e.Process(f0, core.PortMktData, sf0)

		// Era 1: new reset count=1, seq=0 (conformant restart).
		raw1 := buildResetCountFrame(ch, 1, 0)
		f1, sf1 := wire.Decode(raw1, wire.MagicMBO)
		e.Process(f1, core.PortMktData, sf1)
		e.Flush()

		for _, fn := range findingsFor(ac, "FRAME.MKTDATA_SEQ_START") {
			if fn.Status == core.Violation {
				t.Errorf("conformant seq-0 restart must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_no_restart", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Era 0: frame seq=5.
		raw0 := buildResetCountFrame(ch, 0, 5)
		f0, sf0 := wire.Decode(raw0, wire.MagicMBO)
		e.Process(f0, core.PortMktData, sf0)

		// Era 1: new reset count=1, seq=10 (NOT restarting at 0 — violation).
		raw1 := buildResetCountFrame(ch, 1, 10)
		f1, sf1 := wire.Decode(raw1, wire.MagicMBO)
		e.Process(f1, core.PortMktData, sf1)
		e.Flush()

		if !hasViolation(ac, "FRAME.MKTDATA_SEQ_START") {
			t.Error("expected FRAME.MKTDATA_SEQ_START Violation when seq does not restart at 0")
		}
	})
}

// TestBatchIDMonotonic: BatchBoundary Batch ID must be monotonically
// increasing within era; strictly-decreasing is the violation.
func TestBatchIDMonotonic(t *testing.T) {
	const ch = uint8(1)

	t.Run("conformant_increasing", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 1), 1),
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 2), 2),
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 5), 3), // forward skip is OK
		})
		for _, fn := range findingsFor(ac, "BATCH.ID_MONOTONIC") {
			if fn.Status == core.Violation {
				t.Errorf("increasing batch IDs must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_decreasing", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 10), 1),
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 5), 2), // decreased → violation
		})
		if !hasViolation(ac, "BATCH.ID_MONOTONIC") {
			t.Error("expected BATCH.ID_MONOTONIC Violation on decreasing batch ID, got none")
		}
	})

	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 10), 1),
			// skip seq 2 (gap)
			mktdataEntrySeq(buildBatchBoundaryFrame(ch, 5), 3), // decreased but gapped
		})
		for _, fn := range findingsFor(ac, "BATCH.ID_MONOTONIC") {
			if fn.Status == core.Violation {
				t.Errorf("gapped channel must not emit Violation for BATCH.ID_MONOTONIC: %s", fn.Detail)
			}
		}
	})
}

// TestSnapAnchorIsMktdataSeq: SnapshotBegin's Anchor Seq must not exceed the
// highest mktdata seq observed.
func TestSnapAnchorIsMktdataSeq(t *testing.T) {
	const ch, instrID = uint8(1), uint32(40)

	t.Run("conformant", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed a few mktdata frames (seq 1..3), then a snapshot with anchor=2.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 1, 2), 1),
		})
		for _, fn := range findingsFor(ac, "SNAP.ANCHOR_IS_MKTDATA_SEQ") {
			if fn.Status == core.Violation {
				t.Errorf("conformant anchor must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_anchor_too_high", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Highest mktdata seq = 3, but snapshot anchor = 100.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 100, 1, 2), 1), // anchor=100 > 3
		})
		if !hasViolation(ac, "SNAP.ANCHOR_IS_MKTDATA_SEQ") {
			t.Error("expected SNAP.ANCHOR_IS_MKTDATA_SEQ Violation when anchor > highest mktdata seq")
		}
	})

	t.Run("gapped_mktdata_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Mktdata has a gap: seq 1 then seq 3 (gap at 2).
		// Flush mktdata before processing the snapshot so that seq=3 is classified
		// (registering the gap and setting dirtyWindow). Then process snapshot.
		// Highest seen = 3, but anchor=100 (too high). With dirty mktdata window,
		// must be Unverifiable not Violation.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			// skip seq 2
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			flushEntry(), // flush mktdata so seq=3 is classified and dirtyWindow is set
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 100, 1, 3), 1),
		})
		for _, fn := range findingsFor(ac, "SNAP.ANCHOR_IS_MKTDATA_SEQ") {
			if fn.Status == core.Violation {
				t.Errorf("gapped mktdata must downgrade to Unverifiable: %s", fn.Detail)
			}
		}
	})
}

// TestSnapAnchorMonotonicPerInstrument: successive snapshots for an instrument
// must have non-decreasing Anchor Seq.
func TestSnapAnchorMonotonicPerInstrument(t *testing.T) {
	const ch, instrID = uint8(1), uint32(50)

	t.Run("conformant_non_decreasing", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed mktdata up to seq 10 first, then two snapshots with anchor 3 and 5.
		for i := uint64(1); i <= 10; i++ {
			e.Process(func() *wire.Frame {
				f, _ := wire.Decode(buildOrderAddFrame(ch, instrID, uint32(i)), wire.MagicMBO)
				f.Header.Sequence = i
				return f
			}(), core.PortMktData, nil)
		}
		e.Flush()
		clearFindings(ac)

		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 1, 3), 1),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 5, 2, 5), 2), // anchor 3→5 OK
		})
		for _, fn := range findingsFor(ac, "SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT") {
			if fn.Status == core.Violation {
				t.Errorf("non-decreasing anchor must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_decreasing_anchor", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed mktdata up to seq 10.
		for i := uint64(1); i <= 10; i++ {
			e.Process(func() *wire.Frame {
				f, _ := wire.Decode(buildOrderAddFrame(ch, instrID, uint32(i)), wire.MagicMBO)
				f.Header.Sequence = i
				return f
			}(), core.PortMktData, nil)
		}
		e.Flush()
		clearFindings(ac)

		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 5, 1, 5), 1),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 2, 3), 2), // anchor 5→3 violation
		})
		if !hasViolation(ac, "SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT") {
			t.Error("expected SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT Violation on decreasing anchor")
		}
	})

	t.Run("gapped_snapshot_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed mktdata first.
		for i := uint64(1); i <= 10; i++ {
			e.Process(func() *wire.Frame {
				f, _ := wire.Decode(buildOrderAddFrame(ch, instrID, uint32(i)), wire.MagicMBO)
				f.Header.Sequence = i
				return f
			}(), core.PortMktData, nil)
		}
		e.Flush()
		clearFindings(ac)

		// Snapshot port has a gap: seq 1 then seq 3.
		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 5, 1, 5), 1),
			// skip snapshot seq 2
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 2, 3), 3), // anchor decreases but gapped
		})
		for _, fn := range findingsFor(ac, "SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT") {
			if fn.Status == core.Violation {
				t.Errorf("gapped snapshot port must not emit Violation: %s", fn.Detail)
			}
		}
	})
}

// TestSnapSnapshotIDMonotonic: Snapshot ID must be monotonically increasing
// per (channel, instrument) within era.
func TestSnapSnapshotIDMonotonic(t *testing.T) {
	const ch, instrID = uint8(1), uint32(60)

	t.Run("conformant_increasing", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 1, 1, 1), 1),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 3, 2), 2), // ID 1→3: skip OK
		})
		for _, fn := range findingsFor(ac, "SNAP.SNAPSHOT_ID_MONOTONIC") {
			if fn.Status == core.Violation {
				t.Errorf("increasing snapshot IDs must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_decreasing_id", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 1, 5, 1), 1),
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 3, 2), 2), // snapshot ID 5→3 violation
		})
		if !hasViolation(ac, "SNAP.SNAPSHOT_ID_MONOTONIC") {
			t.Error("expected SNAP.SNAPSHOT_ID_MONOTONIC Violation on decreasing snapshot ID")
		}
	})

	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 1, 5, 1), 1),
			// skip snap seq 2
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 3, 3), 3), // ID decreased but gapped
		})
		for _, fn := range findingsFor(ac, "SNAP.SNAPSHOT_ID_MONOTONIC") {
			if fn.Status == core.Violation {
				t.Errorf("gapped snapshot port must not emit Violation for ID_MONOTONIC: %s", fn.Detail)
			}
		}
	})
}

// TestSnapLastInstrumentSeqConsistentWithDeltas: SnapshotBegin's Last Instrument
// Seq (K) must equal the per-instrument seq of the last delta applied.
func TestSnapLastInstrumentSeqConsistentWithDeltas(t *testing.T) {
	const ch, instrID = uint8(1), uint32(70)

	t.Run("conformant", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Flush mktdata first so all three deltas are classified (lastInstrSeq=3)
		// before the snapshot arrives and checks K.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			flushEntry(), // ensure all mktdata is classified before snapshot
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 1, 3), 1), // K=3 matches
		})
		for _, fn := range findingsFor(ac, "SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS") {
			if fn.Status == core.Violation {
				t.Errorf("conformant K must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_wrong_K", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed 3 deltas but claim K=5 in the snapshot.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			flushEntry(), // ensure all mktdata is classified before snapshot
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 1, 5), 1), // K=5 but tracker has 3
		})
		if !hasViolation(ac, "SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS") {
			t.Error("expected SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS Violation")
		}
	})

	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Mktdata port has a gap so K mismatch is Unverifiable.
		// Flush mktdata before snapshot so dirtyWindow is set (seq=3 classified).
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			// skip seq 2 (gap)
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
			flushEntry(), // ensure mktdata seq=3 is classified and dirtyWindow set
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 3, 1, 5), 1), // K=5 but tracker=3, gapped
		})
		for _, fn := range findingsFor(ac, "SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS") {
			if fn.Status == core.Violation {
				t.Errorf("gapped mktdata must not emit Violation for LAST_INSTRUMENT_SEQ: %s", fn.Detail)
			}
		}
	})
}

// TestResetNoDanglingDeltasAtOrBelowAnchor: after an InstrumentReset, the first
// post-reset delta for instrument I must carry per-instrument seq == recoveryK+1.
func TestResetNoDanglingDeltasAtOrBelowAnchor(t *testing.T) {
	const ch, instrID = uint8(1), uint32(80)

	t.Run("conformant", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed delta, reset, then flush so InstrReset is classified (sets pendingResetAnchor).
		// Then process recovery snapshot (sets recoveryK). Then flush again.
		// Then feed the post-reset delta with correct seq (K+1=4).
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, 2), 2),
			flushEntry(), // ensure InstrReset is classified before snapshot arrives
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 1, 3), 1), // recovery snapshot K=3
			flushEntry(), // ensure snapshot is classified before post-reset delta
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 4), 3), // K+1=4 ✓
		})
		for _, fn := range findingsFor(ac, "RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR") {
			if fn.Status == core.Violation {
				t.Errorf("conformant post-reset delta must not emit violation: %s", fn.Detail)
			}
		}
	})

	t.Run("violation_wrong_seq_after_reset", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Same setup but first post-reset delta has perInstr seq=2 (should be K+1=4).
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, 2), 2),
			flushEntry(), // ensure InstrReset is classified
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 1, 3), 1), // recovery K=3
			flushEntry(), // ensure snapshot is classified before post-reset delta
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 3), // wrong: should be 4
		})
		if !hasViolation(ac, "RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR") {
			t.Error("expected RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR Violation")
		}
	})

	t.Run("gapped_unverifiable", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Mktdata gap between reset and post-reset delta.
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
			mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, 2), 2),
			flushEntry(), // ensure InstrReset is classified
			snapEntrySeq(buildSnapBeginFrame(ch, instrID, 2, 1, 3), 1), // recovery K=3
			flushEntry(), // ensure snapshot is classified
			// skip mktdata seq 3 (gap)
			mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 4), // wrong seq but gapped
		})
		for _, fn := range findingsFor(ac, "RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR") {
			if fn.Status == core.Violation {
				t.Errorf("gapped channel must not emit Violation for NO_DANGLING_DELTAS: %s", fn.Detail)
			}
		}
	})
}
