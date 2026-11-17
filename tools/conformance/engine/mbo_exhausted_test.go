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
	"testing"
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
