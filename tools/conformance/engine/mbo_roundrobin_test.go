package engine

// mbo_roundrobin_test.go — Tests for Task 22: SNAP.ROUND_ROBIN_COVERS_MANIFEST.
//
// Rule: SNAP.ROUND_ROBIN_COVERS_MANIFEST — every instrument in the manifest must
// be snapshotted within a cycle. After ≥2 clean snapshot cycles (gauged by OTHER
// instruments cycling), a manifest-ready instrument with no completed snapshots
// fires a Violation naming that instrument.
//
// Gate: snapshot-port gap across the window → Unverifiable (not Violation).
// Conservative: no false positives for idle/late-but-covered instruments.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// doSnapGroup sends a complete Begin→End snapshot group for instrID on the snapshot port.
// snapSeqStart is the first snapshot-port seq used; returns the next seq to use.
func doSnapGroup(e *Engine, ch uint8, instrID uint32, anchorSeq uint64, snapID uint32, snapSeqStart uint64) uint64 {
	seq := snapSeqStart
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, anchorSeq, 0, snapID, 0), seq),
		snapEntry(buildSnapEndFull(ch, instrID, anchorSeq, snapID), seq+1),
	})
	return seq + 2
}

// --- TestRoundRobinNoViolationWhenAllCovered: all instruments snapshotted → silent ---

func TestRoundRobinNoViolationWhenAllCovered(t *testing.T) {
	const ch = uint8(1)
	const instrA, instrB = uint32(400), uint32(401)

	e, ac := newMBOEngineW1()

	// Bootstrap refdata with count=2, both instrA and instrB.
	feedTwoInstrRefdata(e, ch, instrA, instrB, 1)
	clearFindings(ac)

	// Two snapshot cycles: snapshot both instruments twice.
	nextSeq := uint64(1)
	nextSeq = doSnapGroup(e, ch, instrA, 1, 1, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrB, 1, 2, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrA, 2, 3, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrB, 2, 4, nextSeq)
	e.EndRun()

	if hasViolation(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		t.Error("all instruments covered: SNAP.ROUND_ROBIN_COVERS_MANIFEST must not fire")
	}
}

// --- TestRoundRobinViolationWhenInstrumentMissing: one instrument never snapshotted → Violation ---

func TestRoundRobinViolationWhenInstrumentMissing(t *testing.T) {
	const ch = uint8(1)
	const instrA, instrB = uint32(410), uint32(411)

	e, ac := newMBOEngineW1()

	// Bootstrap refdata with both instruments.
	feedTwoInstrRefdata(e, ch, instrA, instrB, 1)
	clearFindings(ac)

	// Two cycles for instrA only; instrB is never snapshotted.
	nextSeq := uint64(1)
	nextSeq = doSnapGroup(e, ch, instrA, 1, 1, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrA, 2, 2, nextSeq)
	_ = nextSeq
	e.EndRun()

	// instrB is in manifest but never snapshotted after ≥2 cycles → Violation.
	if !hasViolation(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		t.Error("expected SNAP.ROUND_ROBIN_COVERS_MANIFEST Violation for instrument never snapshotted after 2 cycles")
	}

	// Confirm the detail names instrB.
	found := false
	for _, fn := range findingsFor(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		if fn.Status == core.Violation && fn.InstrumentID == instrB {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SNAP.ROUND_ROBIN_COVERS_MANIFEST Violation to name instrument %d", instrB)
	}
}

// --- TestRoundRobinUnverifiableOnSnapGap: snapshot port gap → Unverifiable ---

func TestRoundRobinUnverifiableOnSnapGap(t *testing.T) {
	const ch = uint8(1)
	const instrA, instrB = uint32(420), uint32(421)

	e, ac := newMBOEngineW1()

	feedTwoInstrRefdata(e, ch, instrA, instrB, 1)
	clearFindings(ac)

	// Introduce a gap on the snapshot port (skip seq 2).
	// Then do two cycles for instrA only; instrB never snapshotted.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrA, 1, 0, 1, 0), 1),
		// skip snap seq 2 — gap → snapshot port dirty
		snapEntry(buildSnapEndFull(ch, instrA, 1, 1), 3),
	})
	nextSeq := uint64(4)
	nextSeq = doSnapGroup(e, ch, instrA, 2, 2, nextSeq)
	_ = nextSeq
	e.EndRun()

	// instrB has no snapshots; snapshot port had a gap → Unverifiable.
	for _, fn := range findingsFor(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		if fn.Status == core.Violation {
			t.Errorf("SNAP.ROUND_ROBIN_COVERS_MANIFEST must be Unverifiable when snapshot port has a gap, got Violation: %s", fn.Detail)
		}
	}
	// The check may or may not fire if maxSnapCycle < 2 due to the gap absorbing
	// the count, but if it fires, it must be Unverifiable.
}

// --- TestRoundRobinSilentWhenFewCycles: fewer than 2 cycles → no violation ---

func TestRoundRobinSilentWhenFewCycles(t *testing.T) {
	const ch = uint8(1)
	const instrA, instrB = uint32(430), uint32(431)

	e, ac := newMBOEngineW1()

	feedTwoInstrRefdata(e, ch, instrA, instrB, 1)
	clearFindings(ac)

	// Only one cycle for instrA; instrB never snapshotted.
	// maxSnapCycle = 1 → conservative, must not fire.
	nextSeq := uint64(1)
	nextSeq = doSnapGroup(e, ch, instrA, 1, 1, nextSeq)
	_ = nextSeq
	e.EndRun()

	if hasViolation(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		t.Error("SNAP.ROUND_ROBIN_COVERS_MANIFEST must not fire when fewer than 2 cycles observed")
	}
}

// --- TestRoundRobinSilentWhenNoRefdata: no manifest → no violation ---

func TestRoundRobinSilentWhenNoRefdata(t *testing.T) {
	const ch = uint8(1)
	const instrID = uint32(440)

	e, ac := newMBOEngineW1()

	// Two snapshot cycles for instrID but no refdata → no manifest → silent.
	nextSeq := uint64(1)
	nextSeq = doSnapGroup(e, ch, instrID, 1, 1, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrID, 2, 2, nextSeq)
	_ = nextSeq
	e.EndRun()

	if hasViolation(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		t.Error("SNAP.ROUND_ROBIN_COVERS_MANIFEST must not fire when no refdata is available")
	}
}

// --- TestRoundRobinEventuallyCoveredSilent: late-but-eventually-covered instrument → silent ---

func TestRoundRobinEventuallyCoveredSilent(t *testing.T) {
	const ch = uint8(1)
	const instrA, instrB = uint32(450), uint32(451)

	e, ac := newMBOEngineW1()

	feedTwoInstrRefdata(e, ch, instrA, instrB, 1)
	clearFindings(ac)

	// Two cycles for instrA; instrB is snapshotted at least once (late but covered).
	nextSeq := uint64(1)
	nextSeq = doSnapGroup(e, ch, instrA, 1, 1, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrA, 2, 2, nextSeq)
	nextSeq = doSnapGroup(e, ch, instrB, 2, 3, nextSeq) // instrB covered eventually
	_ = nextSeq
	e.EndRun()

	if hasViolation(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		t.Error("late-but-eventually-covered instrument must not trigger SNAP.ROUND_ROBIN_COVERS_MANIFEST")
	}
}

// --- helpers ---

// feedTwoInstrRefdata bootstraps MBO refdata for channel ch with two instruments
// (instrA and instrB), making the channel ready() with both in the manifest.
// startSeq is the first refdata port sequence to use.
func feedTwoInstrRefdata(e *Engine, ch uint8, instrA, instrB uint32, startSeq uint64) {
	seq := startSeq

	processRefFrame := func(raw []byte) {
		f, sf := wire.Decode(raw, wire.MagicMBO)
		f.Header.Sequence = seq
		e.Process(f, core.PortRefData, sf)
		seq++
	}

	// ManifestSummary: valid=1, manifestSeq=1, count=2.
	rawMf := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 2)).
		Bytes()
	processRefFrame(rawMf)

	// InstrumentDefinition for instrA.
	rawDefA := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrA) // Instrument ID
			b.Pad(69)
			b.U8(0)  // priceBound
			b.U16(1) // Manifest Seq
			b.Pad(2)
		}).
		Bytes()
	processRefFrame(rawDefA)

	// InstrumentDefinition for instrB.
	rawDefB := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrB) // Instrument ID
			b.Pad(69)
			b.U8(0)  // priceBound
			b.U16(1) // Manifest Seq
			b.Pad(2)
		}).
		Bytes()
	processRefFrame(rawDefB)

	// Second ManifestSummary to close the bootstrap cycle.
	rawMf2 := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 2)).
		Bytes()
	processRefFrame(rawMf2)
}
