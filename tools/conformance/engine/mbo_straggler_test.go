package engine

// mbo_straggler_test.go — datagrams of an era that the state was already wiped for.
//
// A Reset Count change wipes the market-by-order state of the instance on the
// first of its three ports to see it (onResetCountForEra). The other ports can
// still hold datagrams of the old era in their reorder buffers, and they classify
// them before they advance. Those datagrams belong to the old era. If they act on
// the state of the new era, the new era starts with old values: an old delta
// leaves its Per-Instrument Seq in the tracker, so the era's first delta at 1 reads
// as DELTA.PERINSTR_NO_SNAPSHOT_RESET, and an old SnapshotBegin leaves a group open.
//
// This happens on a routine publisher restart, whenever the refdata or snapshot
// port shows the new Reset Count first.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// TestOldEraDeltaDoesNotTouchTheNewEra: an old-era delta drains after the refdata
// port wiped the state for the new era. The new era's deltas from 1 are not a
// restart without a Reset Count change, and not a divergent replay.
func TestOldEraDeltaDoesNotTouchTheNewEra(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 1)
	a.add(e, 2, 2) // stays in the reorder buffer
	a.restart(4)
	a.ready(e) // the refdata port shows the new Reset Count first
	e.Flush()  // drains the old-era delta
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 21)
	a.add(e, 2, 22)
	e.Flush()
	if v := violations(ac, "DELTA."); len(v) != 0 {
		t.Fatalf("got %d DELTA.* violations, want 0: an old-era delta acted on the new era%s", len(v), describe(v))
	}

	// The converse: a fault inside the new era still fires.
	a.add(e, 4, 24) // 3 is missing
	e.Flush()
	if v := violations(ac, "DELTA.PERINSTR_DENSITY"); len(v) != 1 {
		t.Errorf("got %d DELTA.PERINSTR_DENSITY violations, want 1 for the gap in the new era%s", len(v), describe(ac.findings))
	}
}

// TestOldEraSnapshotBeginDoesNotOpenANewEraGroup: an old-era SnapshotBegin drains
// after the refdata port wiped the state for the new era. It must not leave a group
// open in the new era, or the new era's first SnapshotBegin reads as a group that
// the previous one did not close.
func TestOldEraSnapshotBeginDoesNotOpenANewEraGroup(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.send(e, core.PortSnapshot, buildSnapBeginFull(instCh, instInstr, 0, 0, 2, 0)) // stays in the reorder buffer
	a.restart(4)
	a.ready(e)
	e.Flush() // drains the old-era SnapshotBegin
	a.group(e, 0, 1, 0)
	a.group(e, 0, 2, 0)
	e.Flush()
	e.EndRun()
	if v := violations(ac, "SNAP.BEGIN_ORDER_END_GROUPING"); len(v) != 0 {
		t.Errorf("got %d SNAP.BEGIN_ORDER_END_GROUPING violations, want 0: an old-era SnapshotBegin opened a group in the new era%s", len(v), describe(v))
	}
	// The old era's group never ended, because the publisher restarted. A Reset
	// Count change ends a group, so this is an outcome, and not a fault.
	found := false
	for _, f := range findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		if f.Status == core.Unverifiable && f.Reason == core.ReasonTransition {
			found = true
		}
	}
	if !found {
		t.Errorf("the old era's open group got no Unverifiable transition outcome%s", describe(findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING")))
	}
}

// TestOldEraGroupOnALateSnapshotPortStaysWhole (from review): the refdata port
// enters the new era and wipes the state before the snapshot port sends anything.
// The snapshot port then sends a whole group of the old era, in three datagrams.
// They are stragglers, and they are graded together: the group's SnapshotOrder
// and SnapshotEnd find its SnapshotBegin, so there is no grouping violation, and
// the order gets its price-bound outcome.
func TestOldEraGroupOnALateSnapshotPortStaysWhole(t *testing.T) {
	e, ac, a := readyPub(t) // era 3 on the refdata port, no snapshot datagram yet
	a.restart(4)
	a.ready(e) // the refdata port enters era 4 and wipes the state
	e.Flush()
	old := newMBOPub(srcA, 3) // the snapshot port, still in era 3
	old.group(e, 0, 7, 0, 11)
	e.Flush()
	if v := violations(ac, "SNAP."); len(v) != 0 {
		t.Errorf("got %d SNAP.* violations, want 0: the old-era group was split across datagrams%s", len(v), describe(v))
	}
	if len(findingsFor(ac, "SNAP.ORDER_PRICE_BOUND")) == 0 {
		t.Errorf("SNAP.ORDER_PRICE_BOUND has no outcome for the old-era order%s", describe(ac.findings))
	}
}

// TestOrphanSnapshotOrderHasAPriceBoundOutcome (from review): a SnapshotOrder with
// no SnapshotBegin names no instrument, so no Price Bound applies. The grouping
// finding names the defect, and SNAP.ORDER_PRICE_BOUND still reports the
// opportunity, as Unverifiable, reason superseded.
func TestOrphanSnapshotOrderHasAPriceBoundOutcome(t *testing.T) {
	e, ac, a := readyPub(t)
	a.order(e, 7, 11) // no SnapshotBegin
	e.Flush()
	got := findingsFor(ac, "SNAP.ORDER_PRICE_BOUND")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonSuperseded {
		t.Errorf("SNAP.ORDER_PRICE_BOUND: got %v, want one Unverifiable, reason superseded", got)
	}
}
