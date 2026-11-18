package engine

// mbo_gap_gate_test.go — a mktdata gap stops the order rules and the oracle of
// an instrument only when it removed a delta of that instrument.
//
// malbeclabs/edge-feed-spec#131: the order rules (REF.*) and the oracle also
// required the instance's mktdata series to be gapless in the whole era. One lost
// datagram made them Unverifiable on every instrument until the next Reset
// Count, which on a live feed is days. The per-instrument seq shows a lost delta
// of the instrument: the next delta jumps, and the book is no longer trusted. A
// book that is trusted, with this delta next in its series, lost nothing.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// TestGapWithoutTheInstrumentKeepsItsRulesGraded: the lost datagrams carried no
// delta of the instrument. Its book is complete, so a cancel of an unknown order
// is still a violation.
func TestGapWithoutTheInstrumentKeepsItsRulesGraded(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0) // seeds an empty book at K = 0
	e.Flush()
	a.add(e, 1, 7)
	a.skip(core.PortMktData, 3) // three datagrams of other instruments are lost
	a.add(e, 2, 8)
	e.Flush()
	if e.instanceGapless(core.PortMktData, instCh) {
		t.Fatal("the test needs a mktdata gap in the era")
	}
	clearFindings(ac)
	a.cancel(e, 3, 999)
	a.cancel(e, 4, 7)
	e.Flush()
	if v, u := danglingCancels(ac); v != 1 || u != 0 {
		t.Errorf("got %d violations and %d Unverifiable, want 1 violation (order 999)%s", v, u, describe(ac.findings))
	}
}

// TestGapWithTheInstrumentStopsItsRules: the lost datagram carried delta 2 of the
// instrument. Delta 2 could have added order 999, so the cancel is Unverifiable.
func TestGapWithTheInstrumentStopsItsRules(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 7)
	a.skip(core.PortMktData, 1) // carried delta 2
	a.add(e, 3, 8)
	e.Flush()
	clearFindings(ac)
	a.cancel(e, 4, 999)
	e.Flush()
	if v, u := danglingCancels(ac); v != 0 || u != 1 {
		t.Errorf("got %d violations and %d Unverifiable, want 1 Unverifiable%s", v, u, describe(ac.findings))
	}
}

// TestOracleComparesAfterAGapWithoutTheInstrument: the oracle compares a trusted
// book after a mktdata gap that removed no delta of the instrument.
func TestOracleComparesAfterAGapWithoutTheInstrument(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 7)
	a.skip(core.PortMktData, 3)
	a.add(e, 2, 8)
	e.Flush()
	clearFindings(ac)
	a.group(e, 0, 2, 2, 7, 8)
	e.Flush()
	got := findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	if len(got) != 1 || got[0].Status != core.Pass {
		t.Errorf("got %v, want one pass%s", got, describe(ac.findings))
	}
}
