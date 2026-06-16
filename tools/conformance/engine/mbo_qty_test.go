package engine

// mbo_qty_test.go — Tests for Task 25: MBO quantity-conservation rules.
//
// Rules tested:
//   REF.EXEC_OVERFILL              — execute qty exceeds remaining for a resting order
//   REF.FULLFILL_FLAG_DISAGREEMENT — full-fill flag set but exec qty != remaining
//   BATCH.ATOMICITY_CONSISTENCY    — orphan (remaining==0) live order at BatchBoundary
//
// Gating: referential rules are Unverifiable when gateConsumer is false
// (preceding per-instrument gap, no refdata, or mktdata channel gap).
// BATCH.ATOMICITY_CONSISTENCY is gated on mktdata channel contiguity only.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// buildOrderAddWithQty builds an OrderAdd frame with an explicit order quantity.
// This allows setting up resting orders with a specific remaining qty for
// quantity-conservation tests.
//
// OrderAdd is 52 bytes total (4 header + 48 body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6]    = Side (u8)
//	Body[7]    = Order Flags (u8)
//	Body[8:12] = Per-Instrument Seq (u32 LE)
//	Body[12:20]= Order ID (u64 LE)
//	Body[20:28]= Enter Timestamp (u64 LE)
//	Body[28:36]= Price (i64 LE)
//	Body[36:44]= Quantity (u64 LE)
//	Body[44:48]= Reserved
func buildOrderAddWithQty(ch uint8, instrID uint32, perSeq uint32, orderID uint64, qty uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID) // body[0]  Instrument ID
			b.U16(1)       // body[4]  Source ID
			b.U8(1)        // body[6]  Side (ask)
			b.U8(0)        // body[7]  Order Flags (visible)
			b.U32(perSeq)  // body[8]  Per-Instrument Seq
			b.U64(orderID) // body[12] Order ID
			b.U64(1000)    // body[20] Enter Timestamp
			b.I64(100)     // body[28] Price
			b.U64(qty)     // body[36] Quantity
			b.Pad(4)       // body[44] Reserved
		}).
		Bytes()
}

// buildOrderExecuteWithQty builds an OrderExecute frame with explicit exec qty and exec flags.
//
// OrderExecute is 56 bytes total (4 header + 52 body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6]    = Aggressor Side (u8)
//	Body[7]    = Exec Flags (u8)  bit0=full-fill
//	Body[8:12] = Per-Instrument Seq (u32 LE)
//	Body[12:20]= Order ID (u64 LE)
//	Body[20:28]= Trade ID (u64 LE)
//	Body[28:36]= Reserved (u64)
//	Body[36:44]= Exec Price (i64 LE)
//	Body[44:52]= Exec Quantity (u64 LE)
func buildOrderExecuteWithQty(ch uint8, instrID uint32, perSeq uint32, orderID uint64, execFlags uint8, execQty uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderExecute, 56, func(b *wb.Body) {
			b.U32(instrID)  // body[0]  Instrument ID
			b.U16(1)        // body[4]  Source ID
			b.U8(2)         // body[6]  Aggressor Side (sell, hits ask side=1)
			b.U8(execFlags) // body[7]  Exec Flags
			b.U32(perSeq)   // body[8]  Per-Instrument Seq
			b.U64(orderID)  // body[12] Order ID
			b.U64(0)        // body[20] Trade ID
			b.U64(0)        // body[28] Reserved
			b.I64(100)      // body[36] Exec Price
			b.U64(execQty)  // body[44] Exec Quantity
		}).
		Bytes()
}

// --- REF.EXEC_OVERFILL tests ---

// TestExecOverfill: OrderExecute qty > remaining for a live resting order.
func TestExecOverfill(t *testing.T) {
	const ch, instrID = uint8(1), uint32(200)
	const orderID = uint64(201)

	t.Run("violation_gapless", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Seed gapless history and set up a resting order with qty=5.
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add resting order with qty=5 (side=ask=1, so sell aggressor=2 is correct).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID, 5), seq)
		seq++
		clearFindings(ac)

		// Execute with qty=10 > remaining=5 (partial-fill, not full-fill).
		// execFlags=0 (partial); execQty=10 > remaining=5 → overfill.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID, 0, 10), seq)
		e.Flush()

		if !hasViolation(ac, "REF.EXEC_OVERFILL") {
			t.Error("expected REF.EXEC_OVERFILL Violation when exec qty > remaining")
		}
	})

	t.Run("conformant_partial_fill", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID2 = uint64(202)
		// Resting qty=10; execute qty=3 (partial, no full-fill).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID2, 10), seq)
		seq++
		clearFindings(ac)

		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID2, 0, 3), seq)
		e.Flush()

		if hasViolation(ac, "REF.EXEC_OVERFILL") {
			t.Error("partial fill within remaining must not emit REF.EXEC_OVERFILL")
		}
	})

	t.Run("unverifiable_per_instr_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Set up refdata but inject a per-instrument gap to make gateConsumer=false.
		reachedReadyMBO(e, ch, instrID, 0, 1)

		// Add with perSeq=1 (establishes seq=1, but next msg will jump to 3).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 1, orderID, 5), 1)
		// Jump perSeq from 1 to 3 → per-instrument gap → gateConsumer=false.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 3, orderID, 0, 10), 2)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.EXEC_OVERFILL") {
			if fn.Status == core.Violation {
				t.Errorf("per-instrument gap must produce Unverifiable for REF.EXEC_OVERFILL, got Violation: %s", fn.Detail)
			}
		}
	})

	t.Run("unverifiable_mktdata_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 0, 1)

		const orderID3 = uint64(203)
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 1, orderID3, 5), 1)
		// Skip mktdata seq=2 (dirtyWindow), then execute with overfill.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 2, orderID3, 0, 10), 3)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.EXEC_OVERFILL") {
			if fn.Status == core.Violation {
				t.Errorf("mktdata gap must produce Unverifiable for REF.EXEC_OVERFILL, got Violation: %s", fn.Detail)
			}
		}
	})

	t.Run("hidden_order_exempt", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderIDH = uint64(204)
		const hiddenFlags = uint8(0x04)
		// Add hidden order: not tracked for qty conservation.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderIDH, 1, 1, 100, hiddenFlags), seq)
		seq++
		clearFindings(ac)

		// Execute with large qty on hidden order — must NOT emit REF.EXEC_OVERFILL.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderIDH, 0, 9999), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.EXEC_OVERFILL") {
			t.Errorf("hidden order execute must not emit REF.EXEC_OVERFILL: %s", fn.Detail)
		}
	})
}

// --- REF.FULLFILL_FLAG_DISAGREEMENT tests ---

// TestFullFillFlagDisagreement: full-fill flag set but exec qty != remaining.
func TestFullFillFlagDisagreement(t *testing.T) {
	const ch, instrID = uint8(1), uint32(210)
	const orderID = uint64(211)
	const fullFillFlags = uint8(0x01) // ExecFlags bit0=1

	t.Run("violation_gapless", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Resting qty=10.
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID, 10), seq)
		seq++
		clearFindings(ac)

		// Full-fill flag set but exec qty=7 != remaining=10 → disagreement.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID, fullFillFlags, 7), seq)
		e.Flush()

		if !hasViolation(ac, "REF.FULLFILL_FLAG_DISAGREEMENT") {
			t.Error("expected REF.FULLFILL_FLAG_DISAGREEMENT Violation when full-fill flag set but qty != remaining")
		}
	})

	t.Run("conformant_full_fill_matches", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID2 = uint64(212)
		// Resting qty=10; full-fill with exec qty=10 (matches remaining).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID2, 10), seq)
		seq++
		clearFindings(ac)

		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID2, fullFillFlags, 10), seq)
		e.Flush()

		if hasViolation(ac, "REF.FULLFILL_FLAG_DISAGREEMENT") {
			t.Error("full-fill exec qty == remaining must not emit REF.FULLFILL_FLAG_DISAGREEMENT")
		}
		if hasViolation(ac, "REF.EXEC_OVERFILL") {
			t.Error("conformant full-fill must not emit REF.EXEC_OVERFILL")
		}
	})

	t.Run("no_fullfill_disagreement_on_overfill", func(t *testing.T) {
		// When execQty > remaining and full-fill is also set, applyOrderExecute returns
		// errOverfill (checked first). Only REF.EXEC_OVERFILL should fire, not both.
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID3 = uint64(213)
		// Resting qty=5.
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID3, 5), seq)
		seq++
		clearFindings(ac)

		// full-fill + execQty=10 > remaining=5 → errOverfill returned first.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID3, fullFillFlags, 10), seq)
		e.Flush()

		if !hasViolation(ac, "REF.EXEC_OVERFILL") {
			t.Error("expected REF.EXEC_OVERFILL when execQty > remaining (even with full-fill flag)")
		}
		// errOverfill is returned before errFullFillDisagree in book.go; only one fires.
		overfillCount := len(findingsFor(ac, "REF.EXEC_OVERFILL"))
		disagreeCount := len(findingsFor(ac, "REF.FULLFILL_FLAG_DISAGREEMENT"))
		if overfillCount+disagreeCount > 1 {
			t.Errorf("at most one quantity-conservation finding per execute: got overfill=%d disagree=%d",
				overfillCount, disagreeCount)
		}
	})

	t.Run("unverifiable_per_instr_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 0, 1)

		// Resting order with perSeq=1.
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 1, orderID, 10), 1)
		// Jump perSeq from 1 to 3 → gateConsumer=false.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 3, orderID, fullFillFlags, 7), 2)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.FULLFILL_FLAG_DISAGREEMENT") {
			if fn.Status == core.Violation {
				t.Errorf("per-instrument gap must produce Unverifiable for REF.FULLFILL_FLAG_DISAGREEMENT, got Violation: %s", fn.Detail)
			}
		}
	})

	t.Run("hidden_order_exempt", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderIDH = uint64(214)
		const hiddenFlags = uint8(0x04)
		// Add hidden order.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderIDH, 1, 1, 100, hiddenFlags), seq)
		seq++
		clearFindings(ac)

		// Full-fill with mismatched qty on hidden order — must NOT emit finding.
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderIDH, fullFillFlags, 99), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.FULLFILL_FLAG_DISAGREEMENT") {
			t.Errorf("hidden order must not emit REF.FULLFILL_FLAG_DISAGREEMENT: %s", fn.Detail)
		}
	})
}

// --- BATCH.ATOMICITY_CONSISTENCY tests ---

// TestBatchAtomicityConsistency: orphan live order (remaining==0) at BatchBoundary.
//
// The book builder's applyOrderExecute always removes an order when remaining
// reaches zero during normal operation. This check is therefore a defensive
// boundary assertion; the only way to trigger it in unit tests is to directly
// inject a zero-remaining live order into the book state before the boundary.
func TestBatchAtomicityConsistency(t *testing.T) {
	const ch, instrID = uint8(1), uint32(220)
	const orderID = uint64(221)

	t.Run("conformant_no_orphan", func(t *testing.T) {
		// Normal flow: add order, partial fill (remaining > 0), then BatchBoundary.
		// Must NOT emit BATCH.ATOMICITY_CONSISTENCY.
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID2 = uint64(222)
		// Resting qty=10; partial fill qty=3 → remaining=7 (not zero).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID2, 10), seq)
		seq++
		runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 5, orderID2, 0, 3), seq)
		seq++
		clearFindings(ac)

		// BatchBoundary — book is consistent (remaining=7).
		runMktdataSeq(e, buildBatchBoundaryFrame(ch, 1), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY") {
			t.Errorf("conformant book must not emit BATCH.ATOMICITY_CONSISTENCY: %s", fn.Detail)
		}
	})

	t.Run("violation_orphan_zero_remaining", func(t *testing.T) {
		// Inject a zero-remaining live order directly into the book state to simulate
		// the impossible state that BATCH.ATOMICITY_CONSISTENCY guards against.
		// (This state cannot occur through normal apply calls; it represents a
		// corrupted or externally-modified book.)
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Ensure the book exists for this instrument.
		e.ensureMBO()
		bk := e.mbo.book.book(ch, instrID)
		// Directly inject a zero-remaining live order (impossible via normal applies).
		bk.live[orderID] = liveOrder{
			side:      1,
			remaining: 0, // orphan: should have been removed
		}

		// BatchBoundary should detect the orphan and emit BATCH.ATOMICITY_CONSISTENCY.
		runMktdataSeq(e, buildBatchBoundaryFrame(ch, 1), seq)
		e.Flush()

		if !hasViolation(ac, "BATCH.ATOMICITY_CONSISTENCY") {
			t.Error("expected BATCH.ATOMICITY_CONSISTENCY Violation for zero-remaining live order at BatchBoundary")
		}
	})

	t.Run("violation_not_emitted_between_boundaries", func(t *testing.T) {
		// BATCH.ATOMICITY_CONSISTENCY must only fire at BatchBoundary, not at every delta.
		// Inject orphan, send a non-boundary delta, confirm no emission. Then send
		// BatchBoundary and confirm it does fire.
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		e.ensureMBO()
		bk := e.mbo.book.book(ch, instrID)
		bk.live[orderID] = liveOrder{side: 0, remaining: 0}

		// Send an OrderAdd delta (not a boundary) — must NOT trigger BATCH.ATOMICITY_CONSISTENCY.
		const orderID3 = uint64(223)
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 4, orderID3, 10), seq)
		seq++
		e.Flush()

		for _, fn := range findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY") {
			t.Errorf("BATCH.ATOMICITY_CONSISTENCY must not fire on non-boundary delta: %s", fn.Detail)
		}
		clearFindings(ac)

		// Now send BatchBoundary — the orphan should be detected.
		runMktdataSeq(e, buildBatchBoundaryFrame(ch, 1), seq)
		e.Flush()

		if !hasViolation(ac, "BATCH.ATOMICITY_CONSISTENCY") {
			t.Error("expected BATCH.ATOMICITY_CONSISTENCY at BatchBoundary for orphan order")
		}
	})

	t.Run("unverifiable_mktdata_gap", func(t *testing.T) {
		// On a loss-tainted channel the book is incomplete; anomaly is Unverifiable.
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 0, 1)

		// Inject orphan.
		e.ensureMBO()
		bk := e.mbo.book.book(ch, instrID)
		bk.live[orderID] = liveOrder{side: 1, remaining: 0}

		// Create a mktdata channel gap (skip seq=2, send seq=3 → dirtyWindow=true).
		runMktdataSeq(e, buildOrderAddWithQty(ch, instrID, 1, 90001, 10), 1)
		// seq 2 missing → gap. BatchBoundary at seq 3.
		runMktdataSeq(e, buildBatchBoundaryFrame(ch, 1), 3)
		e.Flush()

		for _, fn := range findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY") {
			if fn.Status == core.Violation {
				t.Errorf("mktdata gap must produce Unverifiable for BATCH.ATOMICITY_CONSISTENCY, got Violation: %s", fn.Detail)
			}
		}
	})
}
