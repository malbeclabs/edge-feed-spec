package engine

// mbo_seed_test.go — a book becomes trusted from a snapshot, and only from one.
//
// market-by-order/spec.md, Cold Start: a subscriber builds an instrument's book
// from a complete snapshot group (step 5 inserts each SnapshotOrder into the
// book) and then applies the deltas after its Last Instrument Seq K (step 6). A
// first delta at Per-Instrument Seq 1 is the first delta of the era. It does not
// say that the book is empty: the book at the start of an era can hold resting
// orders, and only a snapshot announces them.
//
// malbeclabs/edge-feed-spec#83: the validator trusted an empty book at Per-Instrument
// Seq 1 and never put SnapshotOrders into it. Each cancel or execute of an order
// that a snapshot announced was then a REF.* Must violation that a conformant
// publisher produced.

import (
	"slices"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// group sends one complete snapshot group of instInstr on the snapshot port.
func (p *mboPub) group(e *Engine, anchor uint64, snapID, k uint32, orderIDs ...uint64) {
	p.send(e, core.PortSnapshot, buildSnapBeginFull(instCh, instInstr, anchor, uint32(len(orderIDs)), snapID, k))
	for _, id := range orderIDs {
		p.order(e, snapID, id)
	}
	p.end(e, anchor, snapID)
}

// danglingCancels counts the REF.CANCEL_DANGLING_ORDER findings of each status.
func danglingCancels(ac *allCapture) (viol, unver int) {
	for _, f := range findingsFor(ac, "REF.CANCEL_DANGLING_ORDER") {
		switch f.Status {
		case core.Violation:
			viol++
		case core.Unverifiable:
			unver++
		}
	}
	return viol, unver
}

// readyPub returns an engine and publisher A with its reference data.
func readyPub(t *testing.T) (*Engine, *allCapture, *mboPub) {
	t.Helper()
	e, ac := newMBOEngineW1()
	a := newMBOPub(srcA, 3)
	a.ready(e)
	e.Flush()
	return e, ac, a
}

// TestSnapshotOrdersSeedTheBook is the reproduction in #83. The snapshot at
// K = 0 announces two resting orders before the first delta of the era. A
// cancel of either is conformant. A cancel of an order that no snapshot and no
// delta announced is still a violation.
func TestSnapshotOrdersSeedTheBook(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 7, 0, 999, 1000)
	e.Flush()
	a.cancel(e, 1, 999)
	a.cancel(e, 2, 1000)
	e.Flush()
	if v, _ := danglingCancels(ac); v != 0 {
		t.Fatalf("got %d REF.CANCEL_DANGLING_ORDER violations, want 0: the snapshot announced both orders%s", v, describe(ac.findings))
	}

	a.cancel(e, 3, 5555)
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1 for an order nobody announced%s", v, describe(ac.findings))
	}
}

// TestFirstDeltaDoesNotTrustAnEmptyBook: the era starts at Per-Instrument Seq 1
// and no snapshot has arrived. The book can hold resting orders that the next
// snapshot announces, so a cancel of an unknown order is Unverifiable.
func TestFirstDeltaDoesNotTrustAnEmptyBook(t *testing.T) {
	e, ac, a := readyPub(t)
	a.add(e, 1, 1)
	a.cancel(e, 2, 999) // a resting order from before the era
	e.Flush()
	if v, u := danglingCancels(ac); v != 0 || u != 1 {
		t.Errorf("got %d violations and %d unverifiable, want 0 and 1: no snapshot has shown the book%s", v, u, describe(ac.findings))
	}
}

// TestJoinMidStreamSeedsAtK: the subscriber joins mid-stream, so its first delta
// is at Per-Instrument Seq 50. A snapshot at K = 50 arrives while the tracker is
// at 50. It seeds the book, and the deltas after it are graded.
func TestJoinMidStreamSeedsAtK(t *testing.T) {
	e, ac, a := readyPub(t)
	a.add(e, 50, 7)
	e.Flush()
	a.group(e, 0, 7, 50, 7, 8)
	e.Flush()
	a.cancel(e, 51, 8)   // announced by the snapshot only
	a.cancel(e, 52, 999) // announced by nobody
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1 (order 999 only)%s", v, describe(ac.findings))
	}
}

// TestJoinMidStreamQuietInstrumentSeedsAtK: no delta of the instrument has
// arrived since the subscriber joined, and the mktdata series is classified
// through the snapshot's Anchor Seq. Every delta up to K came before the join,
// so the snapshot seeds the book at K and the next delta must be K+1.
func TestJoinMidStreamQuietInstrumentSeedsAtK(t *testing.T) {
	e, ac, a := readyPub(t)
	// Traffic on another instrument moves the mktdata series past the anchor.
	for i := uint32(1); i <= 3; i++ {
		a.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr+1, 100+i, uint64(70000+i), 0, 1, 100, 0))
	}
	e.Flush()
	a.group(e, 1, 7, 40, 11)
	e.Flush()
	a.cancel(e, 41, 11)
	a.cancel(e, 42, 999)
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Fatalf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1 (order 999 only)%s", v, describe(ac.findings))
	}

	// A gap after the seed makes the book untrusted again.
	clearFindings(ac)
	a.cancel(e, 45, 998)
	a.cancel(e, 46, 997)
	e.Flush()
	if v, _ := danglingCancels(ac); v != 0 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations after a per-instrument gap, want 0%s", v, describe(ac.findings))
	}
}

// TestSnapshotThatCannotSeed: a group seeds nothing when the tracker is past its
// K and a delta after K is missing, or when the group is incomplete. A cancel of
// an unknown order then stays Unverifiable. A tracker past K with every delta
// after K seeds: see mbo_replay_test.go.
func TestSnapshotThatCannotSeed(t *testing.T) {
	t.Run("tracker past K, delta after K missing", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.add(e, 50, 7)
		a.add(e, 52, 8) // delta 51 is missing
		e.Flush()
		a.group(e, 0, 7, 50, 7)
		e.Flush()
		a.cancel(e, 53, 999)
		e.Flush()
		if v, _ := danglingCancels(ac); v != 0 {
			t.Errorf("got %d violations, want 0: delta 51 could have added order 999%s", v, describe(ac.findings))
		}
	})
	t.Run("group under-counts", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.send(e, core.PortSnapshot, buildSnapBeginFull(instCh, instInstr, 0, 2, 7, 0))
		a.order(e, 7, 11)
		a.end(e, 0, 7)
		e.Flush()
		a.cancel(e, 1, 11)
		a.cancel(e, 2, 999)
		e.Flush()
		if v, _ := danglingCancels(ac); v != 0 {
			t.Errorf("got %d violations, want 0: an incomplete group must not seed the book%s", v, describe(ac.findings))
		}
	})
}

// TestRecoverySnapshotSeedsTheBook: after an InstrumentReset, the recovery
// snapshot seeds the book at its K. The deltas after it are graded against it.
func TestRecoverySnapshotSeedsTheBook(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	a.add(e, 1, 1)
	e.Flush()
	anchor := a.seq[core.PortMktData]
	a.send(e, core.PortMktData, buildInstrumentResetFrame(instCh, instInstr, anchor))
	e.Flush()
	a.group(e, anchor, 2, 7, 11)
	e.Flush()
	a.add(e, 8, 12)
	a.cancel(e, 9, 11)   // announced by the recovery snapshot
	a.cancel(e, 10, 999) // announced by nobody
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1 (order 999 only)%s", v, describe(ac.findings))
	}
}

// TestSeedingGroupIsNotCompared: the oracle does not compare a book with the
// snapshot that seeds it, because that comparison always matches. The next group
// at the tracker's K is compared.
func TestSeedingGroupIsNotCompared(t *testing.T) {
	e, ac, a := readyPub(t)
	a.group(e, 0, 1, 0, 11)
	e.Flush()
	got := findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonColdStart {
		t.Fatalf("seeding group: got %v, want one Unverifiable with reason %s: a book adopting its first baseline is routine, and the coverage-loss alert pages on untrusted", got, core.ReasonColdStart)
	}

	clearFindings(ac)
	a.add(e, 1, 12) // mktdata datagram 0
	e.Flush()
	a.group(e, 0, 2, 1, 11, 12) // Anchor Seq 0: after the add's datagram
	e.Flush()
	got = findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	if len(got) != 1 || got[0].Status != core.Pass {
		t.Errorf("second group: got %v, want one Pass", got)
	}
}

// otherInstrAdds sends n OrderAdds of another instrument, so that the mktdata
// series moves without a delta of instInstr.
func (p *mboPub) otherInstrAdds(e *Engine, n uint32) {
	for i := uint32(1); i <= n; i++ {
		p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr+1, i, uint64(70000+i), 0, 1, 100, 0))
	}
}

// TestRestartAnchorBeforeFirstDatagramDoesNotSeed: the validator restarts while
// the publisher runs, so the first mktdata datagram it classifies is at Sequence
// 100. A group taken at Anchor Seq 50 with K = 40 arrives for an instrument with no
// delta seen yet. Deltas 41 to 44 came between the anchor and the restart, so the
// next delta seen is 45. A seed that moved the tracker to 40 would make 45 a false
// DELTA.PERINSTR_DENSITY violation.
func TestRestartAnchorBeforeFirstDatagramDoesNotSeed(t *testing.T) {
	e, ac, a := readyPub(t)
	a.skip(core.PortMktData, 100)
	a.otherInstrAdds(e, 3)
	e.Flush()
	a.group(e, 50, 7, 40, 11)
	e.Flush()
	a.cancel(e, 45, 11)
	a.cancel(e, 46, 999)
	e.Flush()
	if v := violations(ac, "DELTA."); len(v) != 0 {
		t.Errorf("got %d DELTA.* violations, want 0: the validator never saw the deltas after the anchor%s", len(v), describe(v))
	}
	if v, _ := danglingCancels(ac); v != 0 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 0: the book must not be seeded%s", v, describe(ac.findings))
	}
}

// TestAnchorAheadOfClassifiedMktdataDoesNotSeed: the group's Anchor Seq is past
// every mktdata datagram classified so far, so deltas up to K can still arrive. A
// seed at K would read them as late or replayed deltas.
func TestAnchorAheadOfClassifiedMktdataDoesNotSeed(t *testing.T) {
	e, ac, a := readyPub(t)
	a.otherInstrAdds(e, 1)
	e.Flush()
	a.group(e, 5, 7, 3, 11)
	e.Flush()
	a.add(e, 1, 1)
	a.add(e, 2, 2)
	a.add(e, 3, 3)
	a.cancel(e, 4, 2)
	e.Flush()
	if v := violations(ac, "DELTA."); len(v) != 0 {
		t.Errorf("got %d DELTA.* violations, want 0: the deltas up to K arrived after the group%s", len(v), describe(v))
	}
}

// TestSeedReplacesTheUntrustedBook: the untrusted book holds order 7 from a delta.
// The group at K = 50 holds only order 8, so order 7 is not resting at K. After
// the seed, a cancel of 7 is dangling.
func TestSeedReplacesTheUntrustedBook(t *testing.T) {
	e, ac, a := readyPub(t)
	a.add(e, 50, 7)
	e.Flush()
	a.group(e, 0, 7, 50, 8)
	e.Flush()
	a.cancel(e, 51, 7)
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1: the seed must replace the book, not add to it%s", v, describe(ac.findings))
	}
}

// TestFirstDeltaAfterSeedMustContinue: the first delta after a seed must continue
// from the group's K. If it does not, deltas are missing from the seeded book, so
// it is untrusted, and a cancel of an order that a missing delta could have added
// is not a violation.
func TestFirstDeltaAfterSeedMustContinue(t *testing.T) {
	t.Run("recovery snapshot, first delta past K+1", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		e.Flush()
		a.add(e, 1, 1)
		e.Flush()
		anchor := a.seq[core.PortMktData]
		a.send(e, core.PortMktData, buildInstrumentResetFrame(instCh, instInstr, anchor))
		e.Flush()
		a.group(e, anchor, 2, 7, 11)
		e.Flush()
		a.add(e, 10, 12)     // deltas 8 and 9 are missing
		a.cancel(e, 11, 999) // 8 or 9 could have added it
		e.Flush()
		if v, _ := danglingCancels(ac); v != 0 {
			t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 0%s", v, describe(ac.findings))
		}
	})
	t.Run("seed at K = 0, first delta past 1", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		e.Flush()
		a.add(e, 3, 12)     // deltas 1 and 2 are missing
		a.cancel(e, 4, 999) // 1 or 2 could have added it
		e.Flush()
		if v, _ := danglingCancels(ac); v != 0 {
			t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 0%s", v, describe(ac.findings))
		}
	})
}

// TestUnseededWaitIsColdStart: a book that no group has seeded yet is the state the
// oracle reads not being established, which is cold_start. Only a book that a
// group seeded and a per-instrument gap then made untrusted is untrusted, which
// the coverage-loss alert counts.
func TestUnseededWaitIsColdStart(t *testing.T) {
	e, ac, a := readyPub(t)
	oracleReason := func() string {
		got := findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
		if len(got) != 1 || got[0].Status != core.Unverifiable {
			t.Fatalf("got %v, want one Unverifiable", got)
		}
		clearFindings(ac)
		return got[0].Reason
	}
	a.add(e, 50, 7)
	a.add(e, 52, 8) // delta 51 is missing
	e.Flush()
	clearFindings(ac)
	a.group(e, 0, 7, 50, 7) // the tracker is past K, without delta 51
	e.Flush()
	if r := oracleReason(); r != core.ReasonColdStart {
		t.Errorf("never seeded: reason %q, want %q", r, core.ReasonColdStart)
	}
	a.group(e, 0, 8, 52, 7, 8) // lines up: seeds
	e.Flush()
	if r := oracleReason(); r != core.ReasonColdStart {
		t.Errorf("seeding group: reason %q, want %q", r, core.ReasonColdStart)
	}
	a.add(e, 54, 9) // a per-instrument gap: delta 53 is missing
	a.add(e, 55, 10)
	e.Flush()
	clearFindings(ac)
	a.group(e, 0, 9, 52, 7, 8) // the tracker is past K, without delta 53
	e.Flush()
	if r := oracleReason(); r != core.ReasonUntrusted {
		t.Errorf("seeded, then a gap: reason %q, want %q", r, core.ReasonUntrusted)
	}
}

// TestSeedGatedRulesNeedASeededBook: one stream that is non-conformant for five of
// the seed-gated rules. Without a seed none of them is a violation; after an empty
// seed each one is. This is what reportStarvedRules warns about when
// --snapshot-port is not set.
func TestSeedGatedRulesNeedASeededBook(t *testing.T) {
	rules := []string{
		"REF.DUPLICATE_LIVE_ORDERADD", "FIELD.SOURCE_ID_CONSISTENCY", "REF.OPERATION_AFTER_REMOVAL",
		"REF.CANCEL_DANGLING_ORDER", "REF.EXEC_DANGLING_ORDER",
	}
	gated := core.SeedGatedRules(core.FeedMBO)
	for _, id := range rules {
		if !slices.Contains(gated, id) {
			t.Fatalf("%s needs a seeded book but core.SeedGatedRules does not list it", id)
		}
	}
	stream := func(e *Engine, a *mboPub) {
		a.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr, 1, 1, 0, 1, 100, 0))
		a.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr, 2, 1, 0, 1, 100, 0))          // live already
		a.send(e, core.PortMktData, buildOrderCancelFull(instCh, instInstr, 3, 1, 2))                  // Source ID 2, not 1
		a.send(e, core.PortMktData, buildOrderCancelFull(instCh, instInstr, 4, 1, 1))                  // removed already
		a.cancel(e, 5, 999)                                                                            // never added
		a.send(e, core.PortMktData, buildOrderExecuteFull(instCh, instInstr, 6, 998, 0, 1, 0, 100, 1)) // never added
		e.Flush()
	}

	e, ac, a := readyPub(t)
	stream(e, a)
	for _, id := range rules {
		if n := len(violations(ac, id)); n != 0 {
			t.Errorf("unseeded: %s has %d violations, want 0", id, n)
		}
	}

	e, ac, a = readyPub(t)
	a.group(e, 0, 1, 0)
	e.Flush()
	stream(e, a)
	for _, id := range rules {
		if n := len(violations(ac, id)); n == 0 {
			t.Errorf("seeded: %s has no violation, so the stream does not exercise it", id)
		}
	}
}

// TestOldEraDatagramDoesNotOpenTheSeedWindow: the publisher restarts and its
// refdata port shows the new Reset Count first, so the state is wiped for the new
// era. An old-era mktdata datagram still in the reorder buffer drains after that.
// Its Sequence Number is of the old series, so it must not count as the first or
// the highest mktdata datagram of the new era. If it did, a new-era group at K = 2
// would seed the book before new-era deltas 1 and 2 arrive, and they would read as
// a false DELTA.PERINSTR_NO_SNAPSHOT_RESET violation.
func TestOldEraDatagramDoesNotOpenTheSeedWindow(t *testing.T) {
	e, ac, a := readyPub(t)
	a.otherInstrAdds(e, 3) // the last one stays in the reorder buffer
	a.restart(4)
	a.ready(e) // the refdata port leads the reset and wipes the state
	e.Flush()  // drains the old-era datagram into the wiped state
	a.group(e, 1, 7, 2, 11)
	e.Flush()
	a.add(e, 1, 21)
	a.add(e, 2, 22)
	a.add(e, 3, 23)
	e.Flush()
	if v := violations(ac, "DELTA."); len(v) != 0 {
		t.Errorf("got %d DELTA.* violations, want 0: an old-era datagram opened the seed window%s", len(v), describe(v))
	}
}
