package engine

// mbo_exhausted_test.go — an OrderExecute that leaves an order at zero remaining,
// without the full-fill flag, does not remove the order.
//
// market-by-order/spec.md, OrderExecute: "The order is removed from the book if
// `Exec Flags` bit 0 is set. Subscribers MAY also remove the order when
// remaining quantity reaches zero even if the flag is not set, as a consistency
// guard." The guard is a choice of the subscriber, so the publisher removes the
// order with the flag or with an OrderCancel. A publisher that sends the
// OrderCancel after the execute is conformant, and its cancel is not an
// operation after removal. Until the cancel arrives the order rests at zero
// remaining. A BatchBoundary with the order still resting at zero is the
// inconsistency that BATCH.ATOMICITY_CONSISTENCY reports.

import (
	"slices"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// exhaustedCh and exhaustedInstr are the channel and instrument of these tests.
const (
	exhaustedCh    uint8  = 1
	exhaustedInstr uint32 = 240
)

// exhaustOrder adds order id with quantity 4 and executes all 4 without the
// full-fill flag. The per-instrument history before it is gapless. It returns
// the next per-instrument seq and mktdata seq.
func exhaustOrder(t *testing.T, e *Engine, ac *allCapture, id uint64) (perSeq uint32, seq uint64) {
	t.Helper()
	seq = seedGaplessHistory(e, exhaustedCh, exhaustedInstr, 3, 1)
	runMktdataSeq(e, buildOrderAddWithQty(exhaustedCh, exhaustedInstr, 4, id, 4), seq)
	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, 5, id, 0, 4), seq+1)
	e.Flush()
	clearFindings(ac)
	return 6, seq + 2
}

// TestCancelAfterExecuteToZeroIsNotAfterRemoval: the cancel that removes an order
// an execute left at zero is the publisher's removal of it. A second cancel is an
// operation after removal.
func TestCancelAfterExecuteToZeroIsNotAfterRemoval(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(241)
	perSeq, seq := exhaustOrder(t, e, ac, id)

	runMktdataSeq(e, buildOrderCancelFull(exhaustedCh, exhaustedInstr, perSeq, id, 0), seq)
	e.Flush()
	for _, rule := range []string{"REF.OPERATION_AFTER_REMOVAL", "REF.CANCEL_DANGLING_ORDER"} {
		for _, f := range findingsFor(ac, rule) {
			t.Errorf("cancel of an order at zero remaining: %s %v: %s", rule, f.Status, f.Detail)
		}
	}

	runMktdataSeq(e, buildOrderCancelFull(exhaustedCh, exhaustedInstr, perSeq+1, id, 0), seq+1)
	e.Flush()
	if !hasViolation(ac, "REF.OPERATION_AFTER_REMOVAL") {
		t.Error("a second cancel must be REF.OPERATION_AFTER_REMOVAL: the first cancel removed the order")
	}
}

// TestExecuteOnAnOrderAtZeroIsOverfill: the order rests at zero remaining, so an
// execute of 1 more is an overfill of it, not an operation after removal.
func TestExecuteOnAnOrderAtZeroIsOverfill(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(242)
	perSeq, seq := exhaustOrder(t, e, ac, id)

	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, perSeq, id, 0, 1), seq)
	e.Flush()
	if !hasViolation(ac, "REF.EXEC_OVERFILL") {
		t.Error("an execute of 1 on an order at zero remaining must be REF.EXEC_OVERFILL")
	}
	for _, f := range findingsFor(ac, "REF.OPERATION_AFTER_REMOVAL") {
		t.Errorf("an execute on an order at zero remaining is not after removal: %v: %s", f.Status, f.Detail)
	}
}

// TestExecuteToZeroAndCancelInOneBatchIsConsistent: the cancel arrives before the
// BatchBoundary, so the book at the boundary holds no order at zero remaining.
func TestExecuteToZeroAndCancelInOneBatchIsConsistent(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(243)
	perSeq, seq := exhaustOrder(t, e, ac, id)

	runMktdataSeq(e, buildOrderCancelFull(exhaustedCh, exhaustedInstr, perSeq, id, 0), seq)
	runMktdataSeq(e, buildBatchBoundaryFrame(exhaustedCh, 1), seq+1)
	e.Flush()
	for _, f := range findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY") {
		t.Errorf("the cancel came before the boundary: %v: %s", f.Status, f.Detail)
	}
}

// TestExecuteToZeroAcrossABoundaryIsInconsistent: the BatchBoundary arrives
// before the cancel, so the book at the boundary holds an order at zero
// remaining. The cancel after it still removes the order.
func TestExecuteToZeroAcrossABoundaryIsInconsistent(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(244)
	perSeq, seq := exhaustOrder(t, e, ac, id)

	runMktdataSeq(e, buildBatchBoundaryFrame(exhaustedCh, 1), seq)
	e.Flush()
	if !hasViolation(ac, "BATCH.ATOMICITY_CONSISTENCY") {
		t.Error("an order at zero remaining at a BatchBoundary must be BATCH.ATOMICITY_CONSISTENCY")
	}

	clearFindings(ac)
	runMktdataSeq(e, buildOrderCancelFull(exhaustedCh, exhaustedInstr, perSeq, id, 0), seq+1)
	e.Flush()
	for _, f := range findingsFor(ac, "REF.OPERATION_AFTER_REMOVAL") {
		t.Errorf("the cancel after the boundary removes the order: %v: %s", f.Status, f.Detail)
	}
}

// TestOrphanOnACorruptedBookIsUnverifiable: a duplicate execute takes the order to
// zero in this book, while the publisher's order still has 2. The orphan is
// Unverifiable, reason reorder.
func TestOrphanOnACorruptedBookIsUnverifiable(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(245)
	seq := seedGaplessHistory(e, exhaustedCh, exhaustedInstr, 3, 1)
	runMktdataSeq(e, buildOrderAddWithQty(exhaustedCh, exhaustedInstr, 4, id, 4), seq)
	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, 5, id, 0, 2), seq+1)
	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, 5, id, 0, 2), seq+2) // duplicate
	runMktdataSeq(e, buildBatchBoundaryFrame(exhaustedCh, 1), seq+3)
	e.Flush()
	got := findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonReorder {
		t.Errorf("got %v, want one Unverifiable, reason reorder", got)
	}
}

// TestOrphanOnAnUntrustedBookIsUnverifiable: delta 5 is missing, so the book is not
// trusted, and delta 5 could have cancelled the order. The orphan is Unverifiable.
func TestOrphanOnAnUntrustedBookIsUnverifiable(t *testing.T) {
	e, ac := newMBOEngineW1()
	const id = uint64(246)
	seq := seedGaplessHistory(e, exhaustedCh, exhaustedInstr, 3, 1)
	runMktdataSeq(e, buildOrderAddWithQty(exhaustedCh, exhaustedInstr, 4, id, 2), seq)
	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, 6, id, 0, 2), seq+1) // delta 5 is missing
	runMktdataSeq(e, buildBatchBoundaryFrame(exhaustedCh, 1), seq+2)
	e.Flush()
	got := findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonUntrusted {
		t.Errorf("got %v, want one Unverifiable, reason untrusted", got)
	}
}

// TestOrphanOnAnUnseededBookIsColdStart: no snapshot group has seeded the book,
// so it can lack the cancel of an order from before the subscriber joined. The
// orphan is Unverifiable, reason cold_start.
func TestOrphanOnAnUnseededBookIsColdStart(t *testing.T) {
	e, ac := newMBOEngineW1()
	reachedReadyMBO(e, exhaustedCh, exhaustedInstr, 0, 1)
	runMktdataSeq(e, buildOrderAddWithQty(exhaustedCh, exhaustedInstr, 1, 247, 2), 1)
	runMktdataSeq(e, buildOrderExecuteWithQty(exhaustedCh, exhaustedInstr, 2, 247, 0, 2), 2)
	runMktdataSeq(e, buildBatchBoundaryFrame(exhaustedCh, 1), 3)
	e.Flush()
	got := findingsFor(ac, "BATCH.ATOMICITY_CONSISTENCY")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonColdStart {
		t.Errorf("got %v, want one Unverifiable, reason cold_start", got)
	}
	// It decides only on a seeded book, so a run without --snapshot-port names it.
	if !slices.Contains(core.SeedGatedRules(core.FeedMBO), "BATCH.ATOMICITY_CONSISTENCY") {
		t.Error("core.SeedGatedRules must list BATCH.ATOMICITY_CONSISTENCY")
	}
}

// TestOracleSkipsAnOrderAtZero: a snapshot taken between an execute to zero and
// its OrderCancel leaves the order out, because a SnapshotOrder has Quantity > 0.
// The delta book still holds the order at zero. That is not a divergence.
func TestOracleSkipsAnOrderAtZero(t *testing.T) {
	const ch, instrID = uint8(1), uint32(248)
	e, oc := newOracleEngine(1)
	reachedReadyMBO(e, ch, instrID, 0, 1)
	seedEmptyBooks(e, ch, instrID)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 1, 0, 2000, 200, 5), 2)
	runMktdataSeq(e, buildOrderExecuteWithQty(ch, instrID, 3, 80002, 0, 5), 3) // 80002 at zero
	e.Flush()
	oc.auditResults = nil
	completeSnapshot(e, ch, instrID, 3, 10, 3, [][]byte{buildSnapOrderFields(ch, 10, 80001, 0, 0, 1000, 100, 10)}, 1)
	if got := oc.lastAudit(); got != "match" {
		t.Errorf("snapshot without the order at zero: audit %q, want match", got)
	}
}
