package engine

// mbo_gap_gate_test.go — a mktdata gap stops the order rules and the oracle of an
// instance until a snapshot group seeds each book again, not for the whole era.
//
// malbeclabs/edge-feed-spec#131: the order rules (REF.*) and the oracle required
// the instance's mktdata series to be gapless in the whole era. One lost datagram
// made them Unverifiable on every instrument until the next Reset Count, which on
// a live feed is days.
//
// A gap cannot be scoped to the instruments whose deltas it removed. A lost
// datagram can carry an InstrumentReset, and Per-Instrument Seq continues across
// one (market-by-order/spec.md, Per-Instrument Delta Sequence), so the next delta
// of the instrument does not show the loss. So a gap makes every book of the
// instance untrusted, and the next snapshot group of each instrument seeds it
// again (with the delta log, also a busy one).

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// TestGapStopsTheOrderRulesUntilTheNextGroup: after a mktdata gap, a cancel of an
// unknown order is Unverifiable. After the next group seeds the book, the same
// cancel is a violation again, in the same era.
func TestGapStopsTheOrderRulesUntilTheNextGroup(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0) // seeds an empty book at K = 0
	e.Flush()
	a.add(e, 1, 7)              // mktdata datagram 0
	a.skip(core.PortMktData, 3) // datagrams 1 to 3 are lost
	a.add(e, 2, 8)              // datagram 4
	e.Flush()
	clearFindings(ac)
	a.cancel(e, 3, 999) // datagram 5
	e.Flush()
	if v, u := danglingCancels(ac); v != 0 || u != 1 {
		t.Fatalf("after the gap: %d violations and %d Unverifiable, want 1 Unverifiable%s", v, u, describe(ac.findings))
	}

	a.group(e, 5, 2, 3, 7, 8) // seeds the book again
	e.Flush()
	clearFindings(ac)
	a.cancel(e, 4, 998)
	e.Flush()
	if v, u := danglingCancels(ac); v != 1 || u != 0 {
		t.Errorf("after the next group: %d violations and %d Unverifiable, want 1 violation%s", v, u, describe(ac.findings))
	}
}

// TestPerInstrumentJumpStopsTheCancelAfterIt: the publisher skips delta 2 of the
// instrument with no gap in the mktdata series. Delta 2 could have added order
// 999, so the cancel that jumps over it is Unverifiable.
func TestPerInstrumentJumpStopsTheCancelAfterIt(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 7)
	e.Flush()
	clearFindings(ac)
	a.cancel(e, 3, 999) // the delta that jumps
	e.Flush()
	if !e.instanceGapless(core.PortMktData, instCh) {
		t.Fatal("the test needs a gapless mktdata series")
	}
	if v, u := danglingCancels(ac); v != 0 || u != 1 {
		t.Errorf("got %d violations and %d Unverifiable, want 1 Unverifiable%s", v, u, describe(ac.findings))
	}
}

// TestOracleComparesAgainAfterAGap: after a mktdata gap, the next group seeds the
// book, and the group after it is compared, in the same era.
func TestOracleComparesAgainAfterAGap(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 7)              // datagram 0
	a.skip(core.PortMktData, 3) // datagrams 1 to 3 are lost
	a.add(e, 2, 8)              // datagram 4
	e.Flush()
	a.group(e, 4, 2, 2, 7, 8) // seeds the book again
	a.add(e, 3, 9)            // datagram 5
	e.Flush()
	clearFindings(ac)
	a.group(e, 5, 3, 3, 7, 8, 9)
	e.Flush()
	got := findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	if len(got) != 1 || got[0].Status != core.Pass {
		t.Errorf("got %v, want one pass%s", got, describe(ac.findings))
	}
}

// TestLostInstrumentResetIsNotGradedAgainstTheOldBook (from review): the datagram
// that carried an InstrumentReset is lost. Per-Instrument Seq continues across the
// reset, so the next delta does not show it. The recovery group must not be
// compared with the book from before the reset, and a cancel of an order of the
// recovery group must not be a dangling cancel. The oracle confirms on one cycle
// here, so a single wrong comparison would be a violation.
func TestLostInstrumentResetIsNotGradedAgainstTheOldBook(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1, OracleConfirmCycles: 1}, ac)
	a := newMBOPub(srcA, 3)
	a.ready(e)
	a.group(e, 0, 1, 0) // seeds an empty book at K = 0
	e.Flush()
	a.add(e, 1, 7)              // datagram 0
	a.add(e, 2, 8)              // datagram 1
	a.skip(core.PortMktData, 1) // datagram 2 carried InstrumentReset: the book is now empty
	e.Flush()
	a.group(e, 2, 2, 2, 11) // the recovery group: order 11 only
	e.Flush()
	a.cancel(e, 3, 11) // datagram 3: reveals the gap
	e.Flush()
	a.group(e, 3, 3, 3) // the next group: empty
	e.Flush()
	if v := violations(ac, "REF."); len(v) != 0 {
		t.Errorf("got %d REF.* violations, want 0%s", len(v), describe(v))
	}
	if v := violations(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"); len(v) != 0 {
		t.Errorf("got %d oracle violations, want 0: a group was compared with the book from before the reset%s", len(v), describe(v))
	}
	for _, f := range findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		if f.Status == core.Suspected {
			t.Errorf("oracle suspected a mismatch: %s", f.Detail)
		}
	}
}
