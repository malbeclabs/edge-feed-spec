package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// dropNth removes the n-th (0-based) frame leading with typ: one lost datagram.
func dropNth(t *testing.T, steps []mbpStep, typ uint8, n int) []mbpStep {
	t.Helper()
	k := 0
	for i, s := range steps {
		f, _ := wire.Decode(s.raw, wire.MagicMBP)
		if len(f.Messages) > 0 && f.Messages[0].Type == typ {
			if k == n {
				return append(append([]mbpStep{}, steps[:i]...), steps[i+1:]...)
			}
			k++
		}
	}
	t.Fatalf("no frame %d leading with type 0x%02X", n, typ)
	return nil
}

// A publisher that mints `Snapshot ID` per instrument and snapshots round-robin gives
// consecutive groups on a channel the same ID, which the spec allows. Losing one
// group's `SnapshotEnd` and the next group's `SnapshotBegin` then merges the two:
// the second instrument's levels match the open Begin's ID and count against it.
// Seen live as "declared Total Levels = 14 but carried 19" on a lossy path. That is
// loss, not publisher misconduct, and it must not grade as a MUST violation.
func TestMBPGroupsMergedByLossAreUnverifiable(t *testing.T) {
	b := make([][3]int64, 0, 5)
	for i := 0; i < 5; i++ {
		// Prices overlapping group A's, so the merge also repeats keys.
		b = append(b, bid(int64(1000-i), 3))
	}
	steps := newTape().
		group(3, 10, 7, 1, 0, ladder(2)...).
		group(1, 11, 7, 1, 0, ladder(14)...).
		group(2, 12, 7, 1, 0, b...).steps
	steps = dropNth(t, steps, wire.TypeSnapshotEnd, 1)   // End(A)
	steps = dropNth(t, steps, wire.TypeSnapshotBegin, 2) // Begin(B)

	_, fs := runMBPSteps(t, steps)
	viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE")
	if viol != 0 {
		t.Errorf("losing End(A) and Begin(B) produced %d Violation(s); a merge by loss is not publisher misconduct", viol)
	}
	if got := countByReason(fs, "MBP.SNAP.GROUP_STRUCTURE", core.ReasonLoss); got == 0 || unver == 0 {
		t.Errorf("the merged group must still be reported, as Unverifiable for loss (got %d)", got)
	}
}

// The gate is no wider than the merge: on a group that lost a datagram but closes
// with its **own** End, nothing from another group can have been counted, so an
// over-count there is still the publisher's.
func TestMBPOverCountWithOwnEndStaysViolationOnDirtyGroup(t *testing.T) {
	tp := newTape()
	tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 2, 7, 0, 0))
	tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid))
	tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 999, 5, mbpClearSideBid))
	tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 998, 5, mbpClearSideBid))
	tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 997, 5, mbpClearSideBid))
	tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7))
	// Lose one level: the group is dirty, still carries 3 against a declared 2.
	steps := dropNth(t, tp.steps, wire.TypeSnapshotLevel, 1)

	_, fs := runMBPSteps(t, steps)
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 1 {
		t.Errorf("over-count on a dirty group with its own End: got %d Violation(s), want 1", viol)
	}
}
