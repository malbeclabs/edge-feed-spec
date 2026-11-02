package engine

import (
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
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
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 0 {
		t.Errorf("losing End(A) and Begin(B) produced %d Violation(s); a merge by loss is not publisher misconduct", viol)
	}
	// The merged group must still be reported, each defect on its own finding.
	wantGroupFinding(t, fs, "declared Total Levels", core.Unverifiable)
	wantGroupFinding(t, fs, "repeated", core.Unverifiable)
}

// wantGroupFinding requires exactly one GROUP_STRUCTURE finding whose detail
// contains substr, with status st (and the loss reason when st is Unverifiable).
func wantGroupFinding(t *testing.T, fs []core.Finding, substr string, st core.Status) {
	t.Helper()
	var got []core.Finding
	for _, f := range fs {
		if f.RuleID == "MBP.SNAP.GROUP_STRUCTURE" && strings.Contains(f.Detail, substr) {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want one finding containing %q, got %d: %+v", substr, len(got), got)
	}
	if got[0].Status != st {
		t.Errorf("%q: status %v, want %v (%s)", substr, got[0].Status, st, got[0].Detail)
	}
	if st == core.Unverifiable && got[0].Reason != core.ReasonLoss {
		t.Errorf("%q: reason %q, want %q", substr, got[0].Reason, core.ReasonLoss)
	}
}

// mergeTape builds group A (instrument 1, Snapshot ID 7, declared total) with
// levels a, then End(A), Begin(B) for instrument 2 with the same Snapshot ID, the
// levels b, and End(B). It then drops End(A) and Begin(B), so B merges into A.
func mergeTape(t *testing.T, total uint32, a, b []int64) []mbpStep {
	t.Helper()
	tp := newTape()
	tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, total, 7, 0, 0))
	for _, p := range a {
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, p, 5, mbpClearSideBid))
	}
	tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7))
	tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 11, uint32(len(b)), 7, 0, 0))
	for _, p := range b {
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, p, 5, mbpClearSideBid))
	}
	tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 11, 7))
	steps := dropNth(t, tp.steps, wire.TypeSnapshotEnd, 0)
	return dropNth(t, steps, wire.TypeSnapshotBegin, 1)
}

// A merge adds levels only after the gap. A defect that the group already shows in
// the levels it sent before the gap is the publisher's, even when the group then
// closes with a merging End. Each defect is judged on its own.
func TestMBPDefectBeforeGapStaysViolationOnMergedGroup(t *testing.T) {
	t.Run("over-count", func(t *testing.T) {
		// A sends 3 levels against a declared 2, contiguous, before any loss.
		_, fs := runMBPSteps(t, mergeTape(t, 2, []int64{1000, 999, 998}, []int64{990, 989}))
		wantGroupFinding(t, fs, "declared Total Levels", core.Violation)
	})
	t.Run("repeated key", func(t *testing.T) {
		// A repeats a key before any loss; the over-count comes only from B.
		_, fs := runMBPSteps(t, mergeTape(t, 3, []int64{1000, 1000, 999}, []int64{990}))
		wantGroupFinding(t, fs, "repeated", core.Violation)
		wantGroupFinding(t, fs, "declared Total Levels", core.Unverifiable)
	})
}

// The gate takes only an End that a merge can produce: another instrument's End
// with this group's Snapshot ID. Any other foreign End leaves an over-count a
// Violation on a dirty group.
func TestMBPOverCountWithNonMergingEndStaysViolationOnDirtyGroup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   func(*wb.Body)
		field string
	}{
		// This group's own End with only a wrong Anchor Seq.
		{"own instrument, wrong anchor", mbpSnapEnd(1, 99, 7), "Anchor Seq"},
		// Another instrument's End with another Snapshot ID: its levels were refused.
		{"other instrument, other snapshot id", mbpSnapEnd(2, 11, 8), "Snapshot ID 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tp := newTape()
			tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 2, 7, 0, 0))
			for _, p := range []int64{1000, 999, 998, 997} {
				tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, p, 5, mbpClearSideBid))
			}
			tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, tc.end)
			// Lose the second level: the group is dirty after one clean level, and
			// still carries 3 against a declared 2.
			_, fs := runMBPSteps(t, dropNth(t, tp.steps, wire.TypeSnapshotLevel, 1))
			wantGroupFinding(t, fs, tc.field, core.Unverifiable)
			wantGroupFinding(t, fs, "declared Total Levels", core.Violation)
		})
	}
}

// The gate needs a gap inside the group. A group that saw no loss of its own,
// inside the era-wide dirty window left by loss in an earlier group, cannot have
// merged: an over-count there stays a Violation even with a merging End.
func TestMBPOverCountOnCleanGroupInDirtyWindowStaysViolation(t *testing.T) {
	tp := newTape()
	tp.group(5, 1, 3, 1, 0, ladder(2)...)
	tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 2, 7, 0, 0))
	for _, p := range []int64{1000, 999, 998} {
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, p, 5, mbpClearSideBid))
	}
	tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 11, 7))
	// Lose the earlier group's End: the gap falls before this group's Begin.
	_, fs := runMBPSteps(t, dropNth(t, tp.steps, wire.TypeSnapshotEnd, 0))
	// The era-wide window is on: the gated field mismatch is Unverifiable.
	wantGroupFinding(t, fs, "SnapshotEnd instrument 2", core.Unverifiable)
	wantGroupFinding(t, fs, "declared Total Levels", core.Violation)
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

// The baseline is the state at the first gap. A second gap that falls inside the
// merged levels must not move it, or the merged levels read as the group's own.
func TestMBPMergeBaselineIsFirstGap(t *testing.T) {
	steps := mergeTape(t, 2, []int64{1000, 999}, []int64{990, 989, 988})
	// Also lose B's second level (A sent 2 levels, so it is the fourth one).
	steps = dropNth(t, steps, wire.TypeSnapshotLevel, 3)
	_, fs := runMBPSteps(t, steps)
	wantGroupFinding(t, fs, "declared Total Levels", core.Unverifiable)
}
