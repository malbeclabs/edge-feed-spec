package engine

// gate_test.go — Tests for the per-instrument seq tracker and verifiability gate
// (Task 18).
//
// Scenarios:
//  1. Dense run (seqs 1,2,3): DELTA.PERINSTR_DENSITY silent, status verifiable.
//  2. Seq jump 1→3 WITH covering mktdata channel-seq gap → Unverifiable (loss),
//     not Violation.
//  3. Seq jump 1→3 on a GAPLESS channel → DELTA.PERINSTR_DENSITY Violation.
//  4. First delta seq==2 on cold start (no reset observed) → DELTA.PERINSTR_FIRST_VALUE
//     Unverifiable, never Violation.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// newMBOEngine creates an MBO-feed engine with ReorderWindow=1, wired to an
// allCapture reporter. ReorderWindow=1 means each frame N+1 immediately pops
// frame N from the buffer, so we can inspect findings after each Process call
// (plus a final Flush).
func newMBOEngine() (*Engine, *allCapture) {
	ac := &allCapture{}
	cfg := Config{
		Feed:          core.FeedMBO,
		ReorderWindow: 1,
	}
	return New(cfg, ac), ac
}

// buildOrderAddFrame builds an MBO OrderAdd (0x10) frame for the given channel,
// instrument, and per-instrument sequence number.
//
// OrderAdd is 52 bytes total (4 header + 48 body).
// Relevant fields (from tier1.go / fields.go field offsets):
//
//	Body[0:4]  = Instrument ID (u32 LE)       spec offset 4
//	Body[4:6]  = padding (reserved)           spec offset 8
//	Body[6]    = Side (u8)                    spec offset 10
//	Body[7]    = OrderFlags (u8)              spec offset 11
//	Body[8:12] = Per-Instrument Seq (u32 LE)  spec offset 12
//	Body[12:…] = remaining fields (padded to 48 total body bytes)
//	Body[36:44]= Quantity (u64 LE) — must be >0 for FIELD.QTY_POSITIVE
func buildOrderAddFrame(ch uint8, instrID uint32, perInstrSeq uint32) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID)     // Instrument ID     body[0:4]
			b.Pad(2)           // reserved          body[4:6]
			b.U8(0)            // Side=0 (bid)      body[6]
			b.U8(0)            // OrderFlags=0      body[7]
			b.U32(perInstrSeq) // Per-Instrument Seq body[8:12]
			b.Pad(24)          // padding           body[12:36]
			b.U64(1)           // Quantity=1        body[36:44] — must be >0
			// total body: 4+2+1+1+4+24+8 = 44 bytes — need 48; pad 4 more
			b.Pad(4) // body[44:48]
		}).
		Bytes()
}

// processSeq feeds a raw frame to the engine with the given mktdata sequence.
func processSeq(e *Engine, raw []byte, magic uint16, seq uint64) {
	f, sf := wire.Decode(raw, magic)
	f.Header.Sequence = seq
	e.Process(f, core.PortMktData, sf)
}

// --- Test 1: Dense run — no DELTA.PERINSTR_DENSITY violation ---

// TestPerInstrDensityDenseRun: OrderAdds with per-instrument seq 1,2,3 on a
// gapless mktdata channel must NOT emit any DELTA.PERINSTR_DENSITY violation.
func TestPerInstrDensityDenseRun(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Frame mktdata seqs 1,2,3 — gapless, per-instrument seqs also 1,2,3.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	processSeq(e, buildOrderAddFrame(ch, instrID, 2), wire.MagicMBO, 2)
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 3)
	// Flush the last buffered frame.
	e.Flush()

	for _, fn := range findingsFor(ac, "DELTA.PERINSTR_DENSITY") {
		if fn.Status == core.Violation {
			t.Errorf("DELTA.PERINSTR_DENSITY: dense run must NOT emit Violation; got %v (detail: %s)",
				fn.Status, fn.Detail)
		}
	}
}

// --- Test 2: Seq jump WITH mktdata channel-seq gap → Unverifiable ---

// TestPerInstrDensityJumpWithChannelGap: per-instrument seq jumps 1→3 but the
// mktdata channel had a gap (seq 2 missing).  The detector cannot tell if the
// publisher dropped a delta or the network dropped the frame.  Must be
// Unverifiable, NOT Violation.
func TestPerInstrDensityJumpWithChannelGap(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Frame mktdata seq=1, per-instrument seq=1.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)

	// Skip mktdata seq=2 (gap), jump to mktdata seq=3.
	// Per-instrument seq jumps from 1 to 3 as well.
	// The port tracker will mark dirtyWindow=true when seq=3 is classified
	// (forward gap from 1→3, skipping 2).
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 3)
	e.Flush()

	// Must have at least one Unverifiable finding; must NOT have Violation.
	hasUnverifiable := false
	for _, fn := range findingsFor(ac, "DELTA.PERINSTR_DENSITY") {
		if fn.Status == core.Violation {
			t.Errorf("DELTA.PERINSTR_DENSITY: jump with channel gap must be Unverifiable, got Violation (detail: %s)",
				fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			hasUnverifiable = true
		}
	}
	if !hasUnverifiable {
		t.Error("DELTA.PERINSTR_DENSITY: expected Unverifiable for per-instrument seq jump with channel gap, got none")
	}
}

// --- Test 3: Seq jump WITHOUT channel gap → Violation ---

// TestPerInstrDensityJumpGaplessChannel: per-instrument seq jumps 1→3 on a
// gapless mktdata channel.  The publisher definitely skipped a delta.
// Must be Violation.
func TestPerInstrDensityJumpGaplessChannel(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Mktdata seq=1, per-instrument seq=1.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	// Mktdata seq=2, but per-instrument seq=3 (jump) — gapless mktdata channel.
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 2)
	e.Flush()

	if !hasViolation(ac, "DELTA.PERINSTR_DENSITY") {
		t.Error("DELTA.PERINSTR_DENSITY: expected Violation for per-instrument seq jump on gapless channel, got none")
	}
}

// --- Test 4: First delta seq!=1 on cold start → Unverifiable ---

// TestPerInstrFirstValueColdStart: the very first delta for an instrument has
// per-instrument seq=2 (not 1) and we haven't seen a reset (cold start).
// DELTA.PERINSTR_FIRST_VALUE must be emitted as Unverifiable, never Violation.
func TestPerInstrFirstValueColdStart(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(99)

	// No prior frames for this instrument (cold start — joined mid-stream).
	// First delta has per-instrument seq=2 (not 1).
	processSeq(e, buildOrderAddFrame(ch, instrID, 2), wire.MagicMBO, 1)
	e.Flush()

	// Must emit at least one Unverifiable finding and MUST NOT emit Violation.
	hasUnverifiable := false
	for _, fn := range findingsFor(ac, "DELTA.PERINSTR_FIRST_VALUE") {
		if fn.Status == core.Violation {
			t.Errorf("DELTA.PERINSTR_FIRST_VALUE: cold start seq!=1 must be Unverifiable, got Violation (detail: %s)",
				fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			hasUnverifiable = true
		}
	}
	if !hasUnverifiable {
		t.Error("DELTA.PERINSTR_FIRST_VALUE: expected Unverifiable finding for cold-start seq!=1, got none")
	}
}

// TestPerInstrFirstValueSeq1: first delta with per-instrument seq=1 → silent
// (no FIRST_VALUE finding at all).
func TestPerInstrFirstValueSeq1(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(77)

	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	e.Flush()

	for _, fn := range findingsFor(ac, "DELTA.PERINSTR_FIRST_VALUE") {
		if fn.Status == core.Violation {
			t.Errorf("DELTA.PERINSTR_FIRST_VALUE: seq=1 on first delta must be silent, got Violation")
		}
	}
}

// --- Era boundary: InstrumentReset resets one instrument's tracker ---

// TestInstrumentResetHappyPath: after an InstrumentReset, the next delta with
// per-instrument seq=1 must be accepted silently (no FIRST_VALUE finding).
func TestInstrumentResetHappyPath(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(55)

	// Establish some per-instrument seq history for instrID=55.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	clearFindings(ac)

	// InstrumentReset clears the tracker and sets seenReset=true.
	resetRaw := buildInstrumentResetFrame(ch, instrID, 2)
	processSeq(e, resetRaw, wire.MagicMBO, 2)
	clearFindings(ac)

	// Next delta has seq=1 — correct after a reset. Must be silent.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 3)
	e.Flush()

	for _, fn := range findingsFor(ac, "DELTA.PERINSTR_FIRST_VALUE") {
		if fn.Status == core.Violation {
			t.Errorf("DELTA.PERINSTR_FIRST_VALUE: seq=1 after InstrumentReset must be silent, got Violation")
		}
	}
}

// TestInstrumentResetFirstValueViolation: after an InstrumentReset, the first
// delta with per-instrument seq!=1 on a GAPLESS channel must be a Violation
// (we observed the reset in-stream, so a non-1 start is a publisher fault).
func TestInstrumentResetFirstValueViolation(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(55)

	// Establish some history, then reset.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	resetRaw := buildInstrumentResetFrame(ch, instrID, 2)
	processSeq(e, resetRaw, wire.MagicMBO, 2)
	clearFindings(ac)

	// Next delta has seq=3 (not 1) on a gapless channel (mktdata seq 2→3).
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 3)
	e.Flush()

	if !hasViolation(ac, "DELTA.PERINSTR_FIRST_VALUE") {
		t.Error("DELTA.PERINSTR_FIRST_VALUE: seq!=1 after observed reset on gapless channel must be Violation")
	}
}

// TestResetSnapshotFollowsNAWhenSnapPortUnbound: RESET.SNAPSHOT_FOLLOWS must
// not fire as Violation or Unverifiable when the snapshot port has never been
// seen. The subscriber simply has not observed the snapshot port yet — this is
// not a publisher fault and must downgrade to NA (F5).
func TestResetSnapshotFollowsNAWhenSnapPortUnbound(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(77)

	// Send an InstrumentReset on the mktdata port (gapless seq 1→2).
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	processSeq(e, buildInstrumentResetFrame(ch, instrID, 2), wire.MagicMBO, 2)
	clearFindings(ac)

	// Send a delta immediately (seq=3) without any snapshot port activity.
	// The snapshot port is unbound → RESET.SNAPSHOT_FOLLOWS must be NA.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 3)
	e.Flush()
	e.EndRun()

	for _, f := range findingsFor(ac, "RESET.SNAPSHOT_FOLLOWS") {
		if f.Status == core.Violation || f.Status == core.Unverifiable {
			t.Errorf("RESET.SNAPSHOT_FOLLOWS with snapshot port unbound must be NA, got %v", f.Status)
		}
	}
}

// TestResetSnapshotFollowsEndRunNAWhenSnapPortUnbound: RESET.SNAPSHOT_FOLLOWS
// from EndRun (instrument awaiting recovery at end of stream) must be NA when
// the snapshot port has never been seen (F5).
func TestResetSnapshotFollowsEndRunNAWhenSnapPortUnbound(t *testing.T) {
	e, ac := newMBOEngine()
	const ch = uint8(1)
	const instrID = uint32(88)

	// Establish some history and then reset. Do NOT send any further deltas
	// so awaitingRecovery stays true until EndRun.
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	processSeq(e, buildInstrumentResetFrame(ch, instrID, 2), wire.MagicMBO, 2)
	clearFindings(ac)

	// No snapshot port frames: snapPortBound() == false.
	e.Flush()
	e.EndRun()

	for _, f := range findingsFor(ac, "RESET.SNAPSHOT_FOLLOWS") {
		if f.Status == core.Violation || f.Status == core.Unverifiable {
			t.Errorf("RESET.SNAPSHOT_FOLLOWS at EndRun with snap port unbound must be NA, got %v", f.Status)
		}
	}
}

// buildInstrumentResetFrame builds an InstrumentReset (0x14) frame.
//
// InstrumentReset is 28 bytes total (4 header + 24 body).
// Layout:
//
//	Body[0:4]  = Instrument ID (u32 LE)  spec offset 4
//	Body[4:8]  = padding
//	Body[8:16] = New Anchor Seq (u64 LE)  spec offset 12
//	Body[16:24]= padding
func buildInstrumentResetFrame(ch uint8, instrID uint32, mktdataSeq uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeInstrReset, 28, func(b *wb.Body) {
			b.U32(instrID)    // Instrument ID  body[0]
			b.Pad(4)          // padding        body[4]
			b.U64(mktdataSeq) // New Anchor Seq body[8]  (must equal frame seq for RESET.ANCHOR_SEQ_IS_CURRENT_FRAME)
			b.U64(0)          // padding        body[16]
		}).
		Bytes()
}
