package engine

// mbo_replay_test.go — a snapshot group seeds or audits a book that is past the
// group's Last Instrument Seq, from the logged deltas after it.
//
// market-by-order/spec.md, Cold Start: a subscriber applies a complete snapshot
// group (step 5), then the deltas after its Last Instrument Seq K (step 6). On a
// busy instrument the deltas after K arrive while the group is on the snapshot
// port, so the book is past K when the group closes. malbeclabs/edge-feed-spec#130:
// the validator seeded only a book at K, so the busiest instruments never
// seeded, and the oracle compared only a book at K.

import (
	"maps"
	"slices"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// withDeltaLogLimit sets maxDeltaLog for one test.
func withDeltaLogLimit(t *testing.T, n int) {
	t.Helper()
	old := maxDeltaLog
	maxDeltaLog = n
	t.Cleanup(func() { maxDeltaLog = old })
}

// deltaMsg decodes the one message of a frame.
func deltaMsg(t *testing.T, raw []byte) wire.Message {
	t.Helper()
	f, _ := wire.Decode(raw, wire.MagicMBO)
	if len(f.Messages) != 1 {
		t.Fatalf("frame has %d messages, want 1", len(f.Messages))
	}
	return f.Messages[0]
}

// logSeqs returns the per-instrument seqs a log holds.
func logSeqs(l *deltaLog) []uint32 {
	var out []uint32
	for _, d := range l.live() {
		out = append(out, d.seq)
	}
	return out
}

// TestDeltaLog: the log keeps the most recent deltas with contiguous seqs.
func TestDeltaLog(t *testing.T) {
	cancel := func(seq uint32) wire.Message {
		return deltaMsg(t, buildOrderCancelFull(instCh, instInstr, seq, uint64(seq), 0))
	}
	record := func(l *deltaLog, seqs ...uint32) {
		for _, s := range seqs {
			l.record(s, cancel(s))
		}
	}

	t.Run("contiguous", func(t *testing.T) {
		var l deltaLog
		record(&l, 5, 6, 7)
		if !l.covers(4, 7) || !l.covers(6, 7) || l.covers(3, 7) || l.covers(4, 8) {
			t.Errorf("covers wrong for log %v", logSeqs(&l))
		}
		if got := l.after(5); len(got) != 2 || got[0].seq != 6 {
			t.Errorf("after(5) = %d deltas, want 6 and 7", len(got))
		}
		if !(&deltaLog{}).covers(9, 9) {
			t.Error("a book at K needs no delta, so an empty log covers it")
		}
	})
	t.Run("duplicate and late are not logged", func(t *testing.T) {
		var l deltaLog
		record(&l, 5, 6, 6, 4, 7)
		if got := logSeqs(&l); !slices.Equal(got, []uint32{5, 6, 7}) {
			t.Errorf("log %v, want [5 6 7]", got)
		}
	})
	t.Run("a jump starts a new log", func(t *testing.T) {
		var l deltaLog
		record(&l, 5, 6, 8, 9)
		if got := logSeqs(&l); !slices.Equal(got, []uint32{8, 9}) {
			t.Errorf("log %v, want [8 9]", got)
		}
		if l.covers(5, 9) {
			t.Error("delta 7 is missing, so the log must not cover 6 to 9")
		}
	})
	t.Run("limit drops the oldest", func(t *testing.T) {
		withDeltaLogLimit(t, 3)
		var l deltaLog
		for s := uint32(1); s <= 1000; s++ {
			record(&l, s)
		}
		if got := logSeqs(&l); !slices.Equal(got, []uint32{998, 999, 1000}) {
			t.Errorf("log %v, want [998 999 1000]", got)
		}
		if len(l.entries) > 2*maxDeltaLog {
			t.Errorf("array holds %d entries for %d live", len(l.entries), maxDeltaLog)
		}
		if !l.covers(997, 1000) || l.covers(996, 1000) {
			t.Error("the log covers 998 to 1000 and nothing before")
		}
	})
	t.Run("trim", func(t *testing.T) {
		var l deltaLog
		record(&l, 5, 6, 7, 8)
		l.trimThrough(6)
		if got := logSeqs(&l); !slices.Equal(got, []uint32{7, 8}) {
			t.Errorf("after trimThrough(6): %v, want [7 8]", got)
		}
		l.trimThrough(3)
		if got := logSeqs(&l); !slices.Equal(got, []uint32{7, 8}) {
			t.Errorf("after trimThrough(3): %v, want [7 8]", got)
		}
		l.trimThrough(8)
		if got := logSeqs(&l); len(got) != 0 {
			t.Errorf("after trimThrough(8): %v, want empty", got)
		}
	})
}

// TestSeedAndReplay: the snapshot book at K, with the deltas after K applied,
// is the book at the last delta. Deltas at or below K are already in the
// snapshot and are not applied again.
func TestSeedAndReplay(t *testing.T) {
	var l deltaLog
	l.record(10, deltaMsg(t, buildOrderExecuteWithQty(instCh, instInstr, 10, 1, 0, 2))) // in the snapshot already
	l.record(11, deltaMsg(t, buildOrderExecuteWithQty(instCh, instInstr, 11, 1, 0, 3))) // 10 → 7
	l.record(12, deltaMsg(t, buildOrderAddWithQty(instCh, instInstr, 12, 2, 4)))
	l.record(13, deltaMsg(t, buildOrderCancelFull(instCh, instInstr, 13, 3, 0)))
	l.record(14, deltaMsg(t, buildOrderExecuteWithQty(instCh, instInstr, 14, 9, 0, 1))) // not live: no change

	bk := newInstrBook()
	bk.seedAndReplay(map[uint64]snapOrderRecord{1: {qty: 10}, 3: {qty: 5}}, 10, &l)
	got := bk.Orders()
	if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, []uint64{1, 2}) {
		t.Fatalf("live orders %v, want [1 2]", ids)
	}
	if got[1].remaining != 7 || got[2].remaining != 4 {
		t.Errorf("remaining: order 1 %d (want 7), order 2 %d (want 4)", got[1].remaining, got[2].remaining)
	}
	if !bk.removed.has(3) {
		t.Error("the cancel after K must move order 3 to removed")
	}
}

// TestBusyBookSeedsFromLoggedDeltas: the book is past K when the group closes,
// and the log holds every delta after K. The group seeds the book, and the
// deltas after it are graded.
func TestBusyBookSeedsFromLoggedDeltas(t *testing.T) {
	e, ac, a := readyPub(t)
	a.add(e, 50, 7)
	a.add(e, 51, 8)
	a.cancel(e, 52, 7)
	e.Flush()
	a.group(e, 0, 7, 50, 7) // the snapshot at 50 holds order 7
	e.Flush()

	e.curSrc = srcA
	e.selectMBO(instCh)
	bk := e.mbo.book.book(instCh, instInstr)
	if ids := slices.Sorted(maps.Keys(bk.Orders())); !slices.Equal(ids, []uint64{8}) {
		t.Errorf("seeded book %v, want [8]: order 7 from the snapshot, then add 8 and cancel 7", ids)
	}
	if got := logSeqs(&bk.log); !slices.Equal(got, []uint32{51, 52}) {
		t.Errorf("log after the group %v, want [51 52]: the group close trims through K", got)
	}

	clearFindings(ac)
	a.cancel(e, 53, 8)   // the replayed add
	a.cancel(e, 54, 999) // announced by nobody
	a.cancel(e, 55, 7)   // the replayed cancel removed it
	e.Flush()
	if v, _ := danglingCancels(ac); v != 1 {
		t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1 (order 999)%s", v, describe(ac.findings))
	}
	if !hasViolation(ac, "REF.OPERATION_AFTER_REMOVAL") {
		t.Errorf("a cancel of order 7 after its cancel must be REF.OPERATION_AFTER_REMOVAL%s", describe(ac.findings))
	}
}

// TestSeedNeedsTheLogToReachK: the same stream seeds with a log that reaches back
// to K+1, and does not seed with a log that does not.
func TestSeedNeedsTheLogToReachK(t *testing.T) {
	run := func(t *testing.T, limit, wantViolations int) {
		withDeltaLogLimit(t, limit)
		e, ac, a := readyPub(t)
		a.add(e, 50, 7)
		a.add(e, 51, 8)
		a.add(e, 52, 9)
		e.Flush()
		a.group(e, 0, 7, 50, 7)
		e.Flush()
		clearFindings(ac)
		a.cancel(e, 53, 999)
		e.Flush()
		if v, _ := danglingCancels(ac); v != wantViolations {
			t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want %d%s", v, wantViolations, describe(ac.findings))
		}
	}
	t.Run("log holds 51 and 52", func(t *testing.T) { run(t, 2, 1) })
	t.Run("log holds 52 only", func(t *testing.T) { run(t, 1, 0) })
}

// TestOracleComparesABookPastK: the book is trusted and past K. The oracle
// compares it with the snapshot book plus the logged deltas after K: a correct
// snapshot matches, and a snapshot that omits a resting order does not.
func TestOracleComparesABookPastK(t *testing.T) {
	const ch, instrID = uint8(1), uint32(320)
	run := func(snapIDs ...uint64) *oracleCapture {
		e, oc := newOracleEngine(2)
		reachedReadyMBO(e, ch, instrID, 0, 1)
		seedEmptyBooks(e, ch, instrID)
		runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
		runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 1, 0, 2000, 200, 5), 2)
		runMktdataSeq(e, buildOrderAddExact(ch, instrID, 3, 80003, 1, 0, 3000, 300, 5), 3)
		e.Flush()
		oc.auditResults = nil
		var orders [][]byte
		for _, id := range snapIDs {
			o := map[uint64][]byte{
				80001: buildSnapOrderFields(ch, 10, 80001, 0, 0, 1000, 100, 10),
				80002: buildSnapOrderFields(ch, 10, 80002, 1, 0, 2000, 200, 5),
			}[id]
			orders = append(orders, o)
		}
		completeSnapshot(e, ch, instrID, 2, 10, 2 /* K: the book is at 3 */, orders, 1)
		return oc
	}
	if got := run(80001, 80002).lastAudit(); got != "match" {
		t.Errorf("correct snapshot at K=2, book at 3: audit %q, want match", got)
	}
	if got := run(80001).lastAudit(); got != "mismatch_suspected" {
		t.Errorf("snapshot without order 80002: audit %q, want mismatch_suspected", got)
	}
}

// TestDeltaLogFollowsTheTracker: every delta that moves the tracker forward by
// one is logged, so the log ends where the tracker is. The seed and the oracle
// read that.
func TestDeltaLogFollowsTheTracker(t *testing.T) {
	e, _, a := readyPub(t)
	for s := uint32(1); s <= 5; s++ {
		a.add(e, s, uint64(s))
	}
	a.add(e, 7, 7) // a jump
	a.cancel(e, 8, 7)
	a.send(e, core.PortMktData, buildOrderCancelFull(instCh, instInstr, 6, 6, 0)) // late
	e.Flush()
	e.curSrc = srcA
	e.selectMBO(instCh)
	tr := e.mbo.tracker(instCh, instInstr)
	if got := logSeqs(&e.mbo.book.book(instCh, instInstr).log); !slices.Equal(got, []uint32{7, 8}) || *tr.lastInstrSeq != 8 {
		t.Errorf("log %v with the tracker at %d, want [7 8] at 8", got, *tr.lastInstrSeq)
	}
}
