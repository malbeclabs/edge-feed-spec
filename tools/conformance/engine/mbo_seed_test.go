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

// TestSnapshotThatCannotSeed: a group seeds nothing when the tracker is already
// past its K, or when the group is incomplete. A cancel of an unknown order then
// stays Unverifiable.
func TestSnapshotThatCannotSeed(t *testing.T) {
	t.Run("tracker past K", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.add(e, 50, 7)
		a.add(e, 51, 8)
		e.Flush()
		a.group(e, 0, 7, 50, 7)
		e.Flush()
		a.cancel(e, 52, 999)
		e.Flush()
		if v, _ := danglingCancels(ac); v != 0 {
			t.Errorf("got %d violations, want 0: deltas after K are already in the book%s", v, describe(ac.findings))
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
	a.add(e, 1, 12)
	e.Flush()
	a.group(e, 1, 2, 1, 11, 12)
	e.Flush()
	got = findingsFor(ac, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	if len(got) != 1 || got[0].Status != core.Pass {
		t.Errorf("second group: got %v, want one Pass", got)
	}
}
