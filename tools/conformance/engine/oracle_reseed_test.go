package engine

// oracle_reseed_test.go — a confirmed oracle mismatch stops trusting the book, so
// the group that confirmed it seeds the book again.
//
// malbeclabs/edge-feed-spec#137: after a publisher restart, a recorder's delta
// book diverged from the publisher's snapshots and stayed trusted. With no mktdata
// gap to untrust it, nothing seeded it again, and for 90 minutes the order rules
// graded deltas against it: thousands of REF.CANCEL_DANGLING_ORDER, and an oracle
// mismatch on every group.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// TestConfirmedMismatchSeedsTheBookAgain: the publisher's snapshots hold order 8,
// which no delta announced, so the delta book diverges from them. The second
// group confirms the mismatch and seeds the book, so the cancel of 8 after it is
// not dangling, and the next group matches.
func TestConfirmedMismatchSeedsTheBookAgain(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1, OracleConfirmCycles: 2}, ac)
	a := newMBOPub(srcA, 3)
	a.ready(e)
	a.group(e, 0, 1, 0) // seeds an empty book at K = 0
	e.Flush()
	a.add(e, 1, 7) // datagram 0
	e.Flush()
	a.group(e, 0, 2, 1, 7, 8) // suspected
	a.group(e, 0, 3, 1, 7, 8) // confirmed
	e.Flush()
	if v := violations(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"); len(v) != 1 {
		t.Fatalf("got %d oracle violations, want 1: the second group confirms the mismatch%s", len(v), describe(ac.findings))
	}
	clearFindings(ac)

	a.cancel(e, 2, 8) // datagram 1
	e.Flush()
	if v, _ := danglingCancels(ac); v != 0 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 0: the confirming group seeded the book with order 8%s", v, describe(ac.findings))
	}
	a.group(e, 1, 4, 2, 7)
	e.Flush()
	if v := violations(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"); len(v) != 0 {
		t.Errorf("got %d oracle violations, want 0 after the seed%s", len(v), describe(v))
	}
	if !hasPass(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Errorf("the group after the cancel must match the seeded book%s", describe(ac.findings))
	}
}

// TestSuspectedMismatchKeepsTheBook: one divergent group is only suspected. The
// book stays trusted, so the next group is compared, not seeded, and a mismatch
// that repeats is still confirmed.
func TestSuspectedMismatchKeepsTheBook(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1, OracleConfirmCycles: 3}, ac)
	a := newMBOPub(srcA, 3)
	a.ready(e)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 7)
	e.Flush()
	for id := uint32(2); id <= 4; id++ {
		a.group(e, 0, id, 1, 7, 8)
	}
	e.Flush()
	if v := violations(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"); len(v) != 1 {
		t.Errorf("got %d oracle violations, want 1 on the third group: the first two only suspect it%s", len(v), describe(ac.findings))
	}
}
