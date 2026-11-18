package engine

// mbo_join_test.go — the snapshot groups of a subscriber that has just joined.
//
// A validator that starts while a publisher runs can receive the snapshot port from
// the middle of a group: SnapshotOrder and SnapshotEnd messages whose SnapshotBegin
// went out before the validator listened. That is the subscriber's cold start, not
// a publisher fault (market-by-order/spec.md, Cold Start step 5 discards an order
// with no open snapshot).
//
// The first freeze of the publisher's definition set arrives while the first group
// is open. A replay of a live capture lost about 170 orders a publisher to that
// freeze, each a SNAP.BEGIN_ORDER_END_GROUPING Must violation in the first minute
// (TestFirstRefdataFreezeKeepsTheOpenGroup).

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// TestJoinInsideAGroupIsColdStart: the first snapshot datagrams of the publisher
// that the validator sees are the tail of a group. They are Unverifiable,
// reason cold_start. The next whole group is graded as usual.
func TestJoinInsideAGroupIsColdStart(t *testing.T) {
	e, ac, a := readyPub(t)
	a.skip(core.PortSnapshot, 100) // the validator joins at snapshot datagram 100
	a.order(e, 9, 11)              // the SnapshotBegin of group 9 went out before the join
	a.order(e, 9, 12)
	a.end(e, 0, 9)
	a.group(e, 0, 10, 0, 21)
	e.Flush()

	if v := violations(ac, "SNAP."); len(v) != 0 {
		t.Errorf("got %d SNAP.* violations, want 0: the validator joined inside a group%s", len(v), describe(v))
	}
	cold := 0
	for _, f := range findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		if f.Status == core.Unverifiable && f.Reason == core.ReasonColdStart {
			cold++
		}
	}
	if cold != 3 {
		t.Errorf("got %d cold_start outcomes for the joined group's tail, want 3 (two orders and the end)%s", cold, describe(ac.findings))
	}
	// Each orphan order also reports the price-bound opportunity, as cold_start.
	bound := 0
	for _, f := range findingsFor(ac, "SNAP.ORDER_PRICE_BOUND") {
		if f.Status == core.Unverifiable && f.Reason == core.ReasonColdStart {
			bound++
		}
	}
	if bound != 2 {
		t.Errorf("got %d cold_start SNAP.ORDER_PRICE_BOUND outcomes, want 2 (two orders)%s", bound, describe(ac.findings))
	}
}

// TestOrphanAfterTheFirstBeginIsStillAViolation is the converse: once the
// validator has seen a SnapshotBegin from the publisher, it is past the join, and
// an orphan SnapshotOrder on a gapless snapshot port is the publisher's fault.
func TestOrphanAfterTheFirstBeginIsStillAViolation(t *testing.T) {
	e, ac, a := readyPub(t)
	a.skip(core.PortSnapshot, 100) // not from Sequence Number 0: the SnapshotBegin ends the join
	a.group(e, 0, 10, 0, 21)
	a.order(e, 11, 22) // no SnapshotBegin for group 11
	e.Flush()

	if v := violations(ac, "SNAP.BEGIN_ORDER_END_GROUPING"); len(v) != 1 {
		t.Errorf("got %d SNAP.BEGIN_ORDER_END_GROUPING violations, want 1%s", len(v), describe(ac.findings))
	}
}

// TestFirstRefdataFreezeKeepsTheOpenGroup: the publisher's first definition set
// freezes while the subscriber's first snapshot group is in flight. That is the
// usual order at a join, because a group of a large book spans many datagrams.
// The group's instrument is in the set, so the group stays open and closes
// without a violation. A replay of a live capture lost about 170 orders a publisher
// to this, each a SNAP.BEGIN_ORDER_END_GROUPING Must violation.
func TestFirstRefdataFreezeKeepsTheOpenGroup(t *testing.T) {
	e, ac := newMBOEngineW1()
	a := newMBOPub(srcA, 3)
	a.begin(e, 0, 7, 0)
	e.Flush() // the group is open
	a.ready(e)
	e.Flush() // the first set freezes
	a.order(e, 7, 11)
	a.end(e, 0, 7)
	e.Flush()
	if v := violations(ac, "SNAP."); len(v) != 0 {
		t.Errorf("got %d SNAP.* violations, want 0: the first set's freeze discarded the open group%s", len(v), describe(v))
	}
}

// TestManifestChangeDiscardsOnlyTheGroupOfARemovedInstrument: a new set that keeps
// the open group's instrument keeps the group. A set without it discards the
// group, because its partial state describes an instrument that left.
func TestManifestChangeDiscardsOnlyTheGroupOfARemovedInstrument(t *testing.T) {
	k := refdataKey{src: srcA, ch: instCh}
	for _, tc := range []struct {
		name     string
		newSet   []uint32
		wantOpen bool
	}{
		{"instrument stays", []uint32{instInstr}, true},
		{"instrument removed", []uint32{instInstr + 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newMBOEngineW1()
			a := newMBOPub(srcA, 3)
			a.refdataSet(e, 1, instInstr, instInstr+1)
			e.Flush()
			a.begin(e, 0, 7, 0)
			e.Flush() // the group of instInstr is open
			a.refdataSet(e, 2, tc.newSet...)
			e.Flush()
			if got := e.mbos[k].openSnaps[instCh] != nil; got != tc.wantOpen {
				t.Errorf("group open after the new set = %v, want %v", got, tc.wantOpen)
			}
		})
	}
}

// TestJoinAllowanceEndsAtTheFirstSnapshotEnd: the first SnapshotEnd closes the
// group that the validator joined inside, so an orphan after it is a violation.
func TestJoinAllowanceEndsAtTheFirstSnapshotEnd(t *testing.T) {
	e, ac, a := readyPub(t)
	a.skip(core.PortSnapshot, 100)
	a.end(e, 0, 9)     // closes the group of the join: cold_start
	a.order(e, 10, 21) // no SnapshotBegin of group 10
	a.end(e, 0, 10)
	e.Flush()
	var cold, viol int
	for _, f := range findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		switch {
		case f.Status == core.Unverifiable && f.Reason == core.ReasonColdStart:
			cold++
		case f.Status == core.Violation:
			viol++
		}
	}
	if cold != 1 || viol != 2 {
		t.Errorf("got %d cold_start and %d violations, want 1 and 2%s", cold, viol, describe(ac.findings))
	}
}

// TestSeriesFromZeroHasNoJoinAllowance: the validator receives the snapshot
// series from Sequence Number 0, so no group began before it. An orphan is a
// violation.
func TestSeriesFromZeroHasNoJoinAllowance(t *testing.T) {
	e, ac, a := readyPub(t)
	a.order(e, 9, 11) // snapshot datagram 0
	a.end(e, 0, 9)
	e.Flush()
	if v := len(findingsWithStatus(ac.findings, "SNAP.BEGIN_ORDER_END_GROUPING", core.Violation)); v != 2 {
		t.Errorf("got %d violations, want 2: the series starts at 0%s", v, describe(ac.findings))
	}
}

// TestJoinAllowanceEndsAtANewSnapshotID: the group the validator joined inside has
// one Snapshot ID. An orphan of another Snapshot ID is of a later group, so it is
// a violation even before any SnapshotBegin or SnapshotEnd arrives.
func TestJoinAllowanceEndsAtANewSnapshotID(t *testing.T) {
	e, ac, a := readyPub(t)
	a.skip(core.PortSnapshot, 100)
	a.order(e, 9, 11)  // the joined group: cold_start
	a.order(e, 9, 12)  // the same group: cold_start
	a.order(e, 10, 21) // another group without a SnapshotBegin
	e.Flush()
	var cold, viol int
	for _, f := range findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		switch {
		case f.Status == core.Unverifiable && f.Reason == core.ReasonColdStart:
			cold++
		case f.Status == core.Violation:
			viol++
		}
	}
	if cold != 2 || viol != 1 {
		t.Errorf("got %d cold_start and %d violations, want 2 and 1%s", cold, viol, describe(ac.findings))
	}
}

// TestOldEraOrphanIsTransition: the instance enters a new era while a group of the
// old era is open. A late SnapshotOrder of that group is a straggler, and the
// Reset Count change discarded its SnapshotBegin. That is Unverifiable, reason
// transition: not a join, and not a fault.
func TestOldEraOrphanIsTransition(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0) // past the join
	a.begin(e, 0, 9, 0) // group 9 of era 3
	e.Flush()
	snapSeq := a.seq[core.PortSnapshot]
	a.restart(4)
	a.ready(e) // the instance enters era 4
	e.Flush()
	clearFindings(ac)
	old := newMBOPub(srcA, 3)
	old.seq[core.PortSnapshot] = snapSeq
	old.order(e, 9, 11) // a late order of group 9
	e.Flush()
	for _, rule := range []string{"SNAP.BEGIN_ORDER_END_GROUPING", "SNAP.ORDER_PRICE_BOUND"} {
		got := findingsFor(ac, rule)
		if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonTransition {
			t.Errorf("%s: got %v, want one Unverifiable, reason transition", rule, got)
		}
	}
}
