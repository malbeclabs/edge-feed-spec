package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// The tests in this file are for one property of
// MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT: the oracle compares a group only
// with a journal that continues from the baseline's K without a hole
// (malbeclabs/edge-feed-spec#141).
//
// The first delta of an instrument seeds the tracker at its own number, s0. A
// group from before s0 - 1 can then become the baseline. The deltas between the
// two were never seen: they came before the observation window, or before the
// first delta that arrived. Each capture object is a cold start, so this shape
// occurs at each object boundary, with no loss.
//
// A forward gap and a group behind the adopted baseline make the same shape: a
// baseline before the gap, or before the pruned deltas, with a journal that
// starts after them.

// oracleOutcome counts the oracle's findings by status, and the unverifiable
// ones that carry reason.
func oracleOutcome(t *testing.T, tp *mbpTape, reason string) (pass, viol, unver, withReason int) {
	t.Helper()
	_, fs := runMBPSteps(t, tp.steps)
	viol, unver = countByStatus(t, fs, recon)
	for _, f := range fs {
		if f.RuleID == recon && f.Status == core.Pass {
			pass++
		}
	}
	return pass, viol, unver, countByReason(fs, recon, reason)
}

// The issue's shape. The first deltas seen are 10 and 11. The first group is at
// K = 5, so the deltas 6-9 were not seen, and one of them deleted bid(99). The
// group at K = 11 is the publisher's correct ladder. Before the fix, the oracle
// replayed base(5) plus deltas 10 and 11, kept the stale bid(99), and reported a
// violation.
func TestMBPBaselineBeforeTheFirstDeltaIsNotCompared(t *testing.T) {
	hole := func() *mbpTape {
		return newTape().
			delta(10, mbpClearSideBid, mbpActionChange, 100, 9).
			delta(11, mbpClearSideBid, mbpActionChange, 100, 8).
			group(1, 0, 1, 5, 0, bid(100, 5), bid(99, 3)).
			group(1, 1, 2, 11, 0, bid(100, 8))
	}

	pass, viol, unver, coldStart := oracleOutcome(t, hole(), core.ReasonColdStart)
	if viol != 0 {
		t.Errorf("%d violation(s): deltas the subscriber never saw were graded as a snapshot mismatch", viol)
	}
	// One for the first group (adopted as the baseline) and one for the group the
	// hole makes not comparable. Both are reported; neither is silence.
	if pass != 0 || unver != 2 || coldStart != 2 {
		t.Errorf("pass=%d unverifiable=%d cold_start=%d, want 0, 2 and 2", pass, unver, coldStart)
	}

	// The group at K = 11 reaches s0 - 1, so it closes the hole and the next group
	// compares. A forgiveness that never ends would pass the assertions above too.
	matched := hole().
		delta(12, mbpClearSideBid, mbpActionChange, 100, 7).
		group(1, 2, 3, 12, 0, bid(100, 7))
	if pass, viol, _, _ := oracleOutcome(t, matched, core.ReasonColdStart); pass != 1 || viol != 0 {
		t.Errorf("after the hole: pass=%d violation=%d, want 1 and 0", pass, viol)
	}
	drifted := hole().
		delta(12, mbpClearSideBid, mbpActionChange, 100, 7).
		group(1, 2, 3, 12, 0, bid(100, 4))
	if pass, viol, _, _ := oracleOutcome(t, drifted, core.ReasonColdStart); pass != 0 || viol != 1 {
		t.Errorf("after the hole: pass=%d violation=%d, want 0 and 1 for a real mismatch", pass, viol)
	}
}

// A group that is past the first baseline but still before s0 - 1 does not close
// the hole: the deltas between it and s0 were not seen either.
func TestMBPBaselineStillBeforeTheFirstDeltaIsNotCompared(t *testing.T) {
	tape := newTape().
		delta(10, mbpClearSideBid, mbpActionChange, 100, 9).
		group(1, 0, 1, 5, 0, bid(100, 5), bid(99, 3)).
		group(1, 1, 2, 8, 0, bid(100, 6), bid(99, 3)).
		delta(11, mbpClearSideBid, mbpActionChange, 100, 8).
		group(1, 2, 3, 11, 0, bid(100, 8)).
		delta(12, mbpClearSideBid, mbpActionChange, 100, 7).
		group(1, 3, 4, 12, 0, bid(100, 7))

	// The group at K = 8 is compared with base(5): a hole, so cold_start. The group
	// at K = 11 is compared with base(8): delta 9 is still not seen, so cold_start.
	// The group at K = 12 is compared with base(11) and delta 12, and matches.
	pass, viol, unver, coldStart := oracleOutcome(t, tape, core.ReasonColdStart)
	if pass != 1 || viol != 0 || unver != 3 || coldStart != 3 {
		t.Errorf("pass=%d violation=%d unverifiable=%d cold_start=%d, want 1, 0, 3 and 3",
			pass, viol, unver, coldStart)
	}
}

// The legitimate case: the first delta seen is s0 = K + 1, because it arrived
// before the group at K on the other port. The journal continues from K, so the
// next group compares, and a real mismatch is still a violation.
func TestMBPFirstDeltaAfterTheBaselineIsCompared(t *testing.T) {
	tape := func(snapQty int64) *mbpTape {
		return newTape().
			delta(6, mbpClearSideBid, mbpActionChange, 100, 9).
			group(1, 0, 1, 5, 0, bid(100, 5), bid(99, 3)).
			delta(7, mbpClearSideBid, mbpActionDelete, 99, 0).
			group(1, 1, 2, 7, 0, bid(100, snapQty))
	}
	if pass, viol, _, _ := oracleOutcome(t, tape(9), core.ReasonColdStart); pass != 1 || viol != 0 {
		t.Errorf("s0 = K+1, matching group: pass=%d violation=%d, want 1 and 0", pass, viol)
	}
	if pass, viol, _, _ := oracleOutcome(t, tape(4), core.ReasonColdStart); pass != 0 || viol != 1 {
		t.Errorf("s0 = K+1, diverging group: pass=%d violation=%d, want 0 and 1", pass, viol)
	}
}

// A journal that is dense from the baseline compares and passes. Two ways to be
// dense: the group comes first and seeds the tracker at K, or the first delta is
// at or before K and the group subsumes it.
func TestMBPDenseJournalFromTheBaselineMatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		tape *mbpTape
	}{
		{"group seeds the tracker", newTape().
			group(1, 0, 1, 5, 0, bid(100, 5)).
			delta(6, mbpClearSideBid, mbpActionChange, 100, 9).
			delta(7, mbpClearSideBid, mbpActionChange, 100, 8).
			group(1, 1, 2, 7, 0, bid(100, 8))},
		{"first delta at or before K", newTape().
			delta(4, mbpClearSideBid, mbpActionChange, 100, 4).
			delta(5, mbpClearSideBid, mbpActionChange, 100, 5).
			delta(6, mbpClearSideBid, mbpActionChange, 100, 9).
			group(1, 0, 1, 5, 0, bid(100, 5)).
			delta(7, mbpClearSideBid, mbpActionChange, 100, 8).
			group(1, 1, 2, 7, 0, bid(100, 8))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pass, viol, unver, coldStart := oracleOutcome(t, tc.tape, core.ReasonColdStart)
			if pass != 1 || viol != 0 || unver != 1 || coldStart != 1 {
				t.Errorf("pass=%d violation=%d unverifiable=%d cold_start=%d, want 1, 0, 1 and 1",
					pass, viol, unver, coldStart)
			}
		})
	}
}

// A gap marks the instrument gapped, and the next group clears that. When that
// group is from before the gap, because it arrived late on the snapshot port,
// the journal after it still has the gap. Deltas 3 and 4 are lost, and delta 3
// deleted bid(99).
func TestMBPBaselineBeforeAGapIsNotCompared(t *testing.T) {
	gap := func() *mbpTape {
		return newTape().
			group(1, 0, 1, 0, 0, bid(100, 5), bid(99, 3)).
			delta(1, mbpClearSideBid, mbpActionChange, 100, 6).
			delta(2, mbpClearSideBid, mbpActionChange, 100, 7).
			delta(5, mbpClearSideBid, mbpActionChange, 100, 8).
			group(1, 1, 2, 1, 0, bid(100, 6), bid(99, 3)).
			delta(6, mbpClearSideBid, mbpActionChange, 100, 9).
			group(1, 2, 3, 6, 0, bid(100, 9))
	}
	// The first group is the cold start. The group at K = 1 is unverifiable because
	// the instrument is gapped. The group at K = 6 is unverifiable because the
	// baseline at K = 1 is before the gap.
	pass, viol, unver, loss := oracleOutcome(t, gap(), core.ReasonLoss)
	if pass != 0 || viol != 0 || unver != 3 || loss != 2 {
		t.Errorf("pass=%d violation=%d unverifiable=%d loss=%d, want 0, 0, 3 and 2", pass, viol, unver, loss)
	}
	// The group at K = 6 is past the gap, so the next group compares.
	drifted := gap().
		delta(7, mbpClearSideBid, mbpActionChange, 100, 10).
		group(1, 3, 4, 7, 0, bid(100, 4))
	if pass, viol, _, _ := oracleOutcome(t, drifted, core.ReasonLoss); pass != 0 || viol != 1 {
		t.Errorf("after the gap: pass=%d violation=%d, want 0 and 1 for a real mismatch", pass, viol)
	}
}

// A group behind the adopted baseline becomes the baseline. The deltas between
// the two baselines were pruned with the later one, so the journal after the
// earlier one has a hole. Delta 2, which deleted bid(99), is one of them. The
// publisher is correct throughout.
func TestMBPBaselineBehindTheLastOneIsNotCompared(t *testing.T) {
	late := func() *mbpTape {
		return newTape().
			group(1, 0, 1, 0, 0, bid(100, 5), bid(99, 3)).
			delta(1, mbpClearSideBid, mbpActionChange, 100, 6).
			delta(2, mbpClearSideBid, mbpActionDelete, 99, 0).
			delta(3, mbpClearSideBid, mbpActionChange, 100, 7).
			group(1, 1, 3, 3, 0, bid(100, 7)).
			group(1, 1, 2, 1, 0, bid(100, 6), bid(99, 3)).
			delta(4, mbpClearSideBid, mbpActionChange, 100, 9).
			group(1, 2, 4, 4, 0, bid(100, 9))
	}
	// The group at K = 3 passes. The group at K = 1 is behind it. The group at K = 4
	// is compared with base(1), whose journal lost deltas 2 and 3.
	pass, viol, unver, reorder := oracleOutcome(t, late(), core.ReasonReorder)
	if pass != 1 || viol != 0 || unver != 3 || reorder != 2 {
		t.Errorf("pass=%d violation=%d unverifiable=%d reorder=%d, want 1, 0, 3 and 2", pass, viol, unver, reorder)
	}
	matched := late().
		delta(5, mbpClearSideBid, mbpActionChange, 100, 10).
		group(1, 3, 5, 5, 0, bid(100, 10))
	if pass, viol, _, _ := oracleOutcome(t, matched, core.ReasonReorder); pass != 2 || viol != 0 {
		t.Errorf("after the late group: pass=%d violation=%d, want 2 and 0", pass, viol)
	}
}
