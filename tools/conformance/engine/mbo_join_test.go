package engine

// mbo_join_test.go — a subscriber that joins a snapshot stream inside a group.
//
// A validator that starts while a publisher runs receives the snapshot port from
// the middle of a group: SnapshotOrder and SnapshotEnd messages whose SnapshotBegin
// went out before the validator listened. That is the subscriber's cold start, not
// a publisher fault (market-by-order/spec.md, Cold Start step 5 discards an order
// with no open snapshot). A replay of a live capture graded about 170 such orders a
// publisher as SNAP.BEGIN_ORDER_END_GROUPING Must violations, all in its first
// minute, so every validator restart would page.

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
}

// TestOrphanAfterTheFirstBeginIsStillAViolation is the converse: once the
// validator has seen a SnapshotBegin from the publisher, it is past the join, and
// an orphan SnapshotOrder on a gapless snapshot port is the publisher's fault.
func TestOrphanAfterTheFirstBeginIsStillAViolation(t *testing.T) {
	e, ac, a := readyPub(t)
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
