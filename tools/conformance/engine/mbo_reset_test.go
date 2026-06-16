package engine

// mbo_reset_test.go — Tests for Task 22: cross-port reset-recovery rules.
//
// Rules exercised:
//   RESET.SNAPSHOT_FOLLOWS                       — InstrumentReset must be followed by a
//                                                  recovery snapshot before deltas resume.
//   RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET — recovery snapshot anchor must exactly
//                                                  match InstrumentReset's New Anchor Seq.
//
// Cross-port gate: BOTH the snapshot port and mktdata port must be gapless for a
// Violation. A gap on either port → Unverifiable.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// --- TestResetRecoveryHappyPath: InstrumentReset → matching recovery snapshot → deltas resume silently ---

func TestResetRecoveryHappyPath(t *testing.T) {
	const ch, instrID = uint8(1), uint32(300)
	const resetAnchor = uint64(4) // New Anchor Seq from the InstrumentReset

	e, ac := newMBOEngineW1()

	// Gapless mktdata seqs 1,2,3 then InstrumentReset at seq=4.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
	})
	clearFindings(ac)

	// InstrumentReset at mktdata seq=4 (gapless from 3→4), New Anchor Seq=4.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
	})
	clearFindings(ac)

	// Recovery snapshot arrives on snapshot port: Begin(anchor=4) → End.
	// Flush to ensure the SnapshotEnd is classified (awaitingRecovery cleared)
	// BEFORE mktdata deltas resume.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, resetAnchor, 0, 10, 3 /*lastInstrSeq*/), 1),
		snapEntry(buildSnapEndFull(ch, instrID, resetAnchor, 10), 2),
		flushEntry(), // drain snapshot port buffer before deltas resume
		// Deltas resume after recovery (gapless mktdata).
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 5),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 6),
	})

	if hasViolation(ac, "RESET.SNAPSHOT_FOLLOWS") {
		t.Error("happy path: RESET.SNAPSHOT_FOLLOWS must not fire when recovery snapshot precedes delta resumption")
	}
	if hasViolation(ac, "RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET") {
		t.Error("happy path: RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET must not fire when anchor matches")
	}
}

// --- TestResetNoRecoverySnapshot: InstrumentReset with no recovery snapshot → RESET.SNAPSHOT_FOLLOWS ---

func TestResetNoRecoverySnapshot(t *testing.T) {
	const ch, instrID = uint8(1), uint32(301)

	e, ac := newMBOEngineW1()

	// Bind the snapshot port with a clean empty-book group (instrID+100 to avoid
	// interference with the instrument under test). F5: without this, the snapshot
	// port would be unbound and RESET.SNAPSHOT_FOLLOWS would be NA, not Violation.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID+100, 0, 0, 77, 0), 1),
		snapEntry(buildSnapEndFull(ch, instrID+100, 0, 77), 2),
	})

	// Initial deltas on gapless mktdata seqs 1,2,3.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
	})
	clearFindings(ac)

	// InstrumentReset at mktdata seq=4 (anchor=4); then delta resumes at seq=5
	// without a recovery snapshot. Mktdata seqs are contiguous (3→4→5).
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, 4), 4),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 5), // premature delta — no recovery snapshot
	})

	// Premature delta resumption should fire RESET.SNAPSHOT_FOLLOWS.
	if !hasViolation(ac, "RESET.SNAPSHOT_FOLLOWS") {
		t.Error("expected RESET.SNAPSHOT_FOLLOWS Violation when delta resumes before recovery snapshot")
	}
}

// --- TestResetNoRecoveryAtEndOfRun: InstrumentReset with no recovery and no deltas → RESET.SNAPSHOT_FOLLOWS at EndRun ---

func TestResetNoRecoveryAtEndOfRun(t *testing.T) {
	const ch, instrID = uint8(1), uint32(302)
	const resetAnchor = uint64(3)

	e, ac := newMBOEngineW1()

	// Bind the snapshot port first. F5: without a bound snapshot port the EndRun
	// check would emit NA rather than Violation.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID+100, 0, 0, 88, 0), 1),
		snapEntry(buildSnapEndFull(ch, instrID+100, 0, 88), 2),
	})

	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
	})
	clearFindings(ac)

	// InstrumentReset fires; no recovery snapshot, no deltas, end of stream.
	// Mktdata seqs 1,2,3 are contiguous.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
	})
	e.EndRun()

	if !hasViolation(ac, "RESET.SNAPSHOT_FOLLOWS") {
		t.Error("expected RESET.SNAPSHOT_FOLLOWS Violation at EndRun when recovery snapshot never arrived")
	}
}

// --- TestResetRecoveryWrongAnchor: recovery snapshot with wrong anchor → RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET ---

func TestResetRecoveryWrongAnchor(t *testing.T) {
	const ch, instrID = uint8(1), uint32(303)
	const resetAnchor = uint64(5) // New Anchor Seq from InstrumentReset
	const wrongAnchor = uint64(3) // != resetAnchor

	e, ac := newMBOEngineW1()

	// Contiguous mktdata seqs 1,2,3,4 then InstrumentReset at seq=5.
	// Seqs are gapless so any finding should be Violation (not Unverifiable).
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 4), 4),
	})
	clearFindings(ac)

	// InstrumentReset at mktdata seq=5, New Anchor Seq=5. Gapless (4→5).
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
	})
	clearFindings(ac)

	// Recovery snapshot arrives with the WRONG anchor (3 instead of 5).
	// Snapshot port is gapless (seqs 1→2 contiguous).
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, wrongAnchor, 0, 20, 2), 1),
		snapEntry(buildSnapEndFull(ch, instrID, wrongAnchor, 20), 2),
	})

	if !hasViolation(ac, "RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET") {
		t.Error("expected RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET Violation when recovery snapshot anchor != reset anchor")
	}
	// RESET.SNAPSHOT_FOLLOWS must NOT fire here: a recovery snapshot (even wrong-anchor) did arrive.
	if hasViolation(ac, "RESET.SNAPSHOT_FOLLOWS") {
		t.Error("RESET.SNAPSHOT_FOLLOWS must not fire when a recovery snapshot (even wrong-anchor) was received before deltas")
	}
}

// --- TestResetPrematureDeltaUnverifiableOnSnapGap: snapshot-port gap → RESET.SNAPSHOT_FOLLOWS Unverifiable ---
//
// After InstrumentReset, no recovery snapshot is seen; a delta resumes. The
// snapshot port has a gap that was established BEFORE the delta arrives, so the
// premature-delta finding must be Unverifiable.

func TestResetPrematureDeltaUnverifiableOnSnapGap(t *testing.T) {
	const ch, instrID = uint8(1), uint32(304)
	const resetAnchor = uint64(4)

	e, ac := newMBOEngineW1()

	// Establish a snapshot-port gap FIRST so dirtyWindow is set before the reset.
	// Snap seqs 1→3 (skip 2) → snapshot dirtyWindow=true.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID+1, 0, 0, 77, 0), 1),
		// skip snap seq 2 — gap
		snapEntry(buildSnapEndFull(ch, instrID+1, 0, 77), 3),
	})

	// Gapless mktdata seqs 1,2,3.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 3), 3),
	})
	clearFindings(ac)

	// InstrumentReset at seq=4 (gapless from 3→4). Snapshot port is already dirty.
	// Then premature delta at seq=5 — no recovery snapshot.
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 5), // premature delta
	})

	// RESET.SNAPSHOT_FOLLOWS must fire as Unverifiable (snapshot port had a gap).
	gotUnverifiable := false
	for _, fn := range findingsFor(ac, "RESET.SNAPSHOT_FOLLOWS") {
		if fn.Status == core.Violation {
			t.Errorf("RESET.SNAPSHOT_FOLLOWS must be Unverifiable when snapshot port has a gap, got Violation: %s", fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			gotUnverifiable = true
		}
	}
	if !gotUnverifiable {
		t.Error("expected RESET.SNAPSHOT_FOLLOWS Unverifiable finding when snapshot port has a gap")
	}
}

// --- TestResetSnapshotFollowsUnverifiableOnMktdataGap: mktdata gap → Unverifiable ---

func TestResetSnapshotFollowsUnverifiableOnMktdataGap(t *testing.T) {
	const ch, instrID = uint8(1), uint32(305)
	const resetAnchor = uint64(4)

	e, ac := newMBOEngineW1()

	// Bind the snapshot port with a clean group first. F5: without this, the
	// snapshot port would be unbound and RESET.SNAPSHOT_FOLLOWS would be NA.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID+100, 0, 0, 99, 0), 1),
		snapEntry(buildSnapEndFull(ch, instrID+100, 0, 99), 2),
	})

	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
	})
	clearFindings(ac)

	// InstrumentReset at seq=4; skip seq=3 (mktdata gap → dirtyWindow=true).
	runStream(e, []streamEntry{
		// skip mktdata seq 3 — gap
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), 4),
		// Premature delta at seq=5 — no recovery snapshot.
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 5),
	})

	// RESET.SNAPSHOT_FOLLOWS should fire but be Unverifiable due to mktdata gap.
	gotUnverifiable := false
	for _, fn := range findingsFor(ac, "RESET.SNAPSHOT_FOLLOWS") {
		if fn.Status == core.Violation {
			t.Errorf("RESET.SNAPSHOT_FOLLOWS must be Unverifiable when mktdata port has a gap, got Violation: %s", fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			gotUnverifiable = true
		}
	}
	if !gotUnverifiable {
		t.Error("expected RESET.SNAPSHOT_FOLLOWS Unverifiable finding when mktdata port has a gap")
	}
}

// --- TestResetEndRunUnverifiableOnSnapGap: EndRun check Unverifiable when snapshot port dirty ---

func TestResetEndRunUnverifiableOnSnapGap(t *testing.T) {
	const ch, instrID = uint8(1), uint32(306)
	const resetAnchor = uint64(3)

	e, ac := newMBOEngineW1()

	runStream(e, []streamEntry{
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 1), 1),
		mktdataEntrySeq(buildOrderAddFrame(ch, instrID, 2), 2),
	})
	clearFindings(ac)

	// InstrumentReset at mktdata seq=3 (gapless from 2→3).
	runStream(e, []streamEntry{
		mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
	})

	// Snapshot port has a gap (snap seq 1 then 3, skip 2) — no recovery snapshot.
	// This makes the snapshot port dirty.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID+1, 1, 0, 99, 0), 1),
		// skip snap seq 2
		snapEntry(buildSnapEndFull(ch, instrID+1, 1, 99), 3),
	})
	clearFindings(ac)

	e.EndRun()

	// EndRun must emit RESET.SNAPSHOT_FOLLOWS as Unverifiable (snapshot port dirty).
	gotUnverifiable := false
	for _, fn := range findingsFor(ac, "RESET.SNAPSHOT_FOLLOWS") {
		if fn.Status == core.Violation {
			t.Errorf("RESET.SNAPSHOT_FOLLOWS at EndRun must be Unverifiable when snapshot port has a gap, got Violation: %s", fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			gotUnverifiable = true
		}
	}
	if !gotUnverifiable {
		t.Error("expected RESET.SNAPSHOT_FOLLOWS Unverifiable at EndRun when snapshot port dirty")
	}
}
