package engine

// cadence_test.go — Tests for Task 15 config-gated timing/cadence rules:
//
//   - REFDATA.MANIFEST_CADENCE
//   - HEARTBEAT.CADENCE
//   - REFDATA.DEFINITION_CYCLE_COVERAGE
//   - REFDATA.NO_BURST_DEFINITIONS
//   - REFDATA.NEVER_REACHES_READY
//
// Timing is deterministic: all tests use frame SendTS values (nanoseconds) set
// explicitly in wirebuild frames.  No real wall-clock (time.Now) is used.
//
// All tests use ReorderWindow: 1 so that each successive frame immediately pops
// the previous one from the reorder buffer and drives classification without
// needing an explicit Flush() call between steps.  (Flush() is still called
// before EndRun-based checks.)

import (
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

const nsPerSec = uint64(time.Second)

// --- helpers ---

// newCadenceEngine creates an Engine with the given config, wired to an allCapture reporter.
// ReorderWindow is forced to 1 so frames are classified immediately on the next Process call.
func newCadenceEngine(cfg Config) (*Engine, *allCapture) {
	ac := &allCapture{}
	cfg.ReorderWindow = 1
	e := New(cfg, ac)
	return e, ac
}

// processFrame decodes and processes a single raw frame through the engine on
// the given port, assigning the given sequence number.
func processFrame(e *Engine, raw []byte, magic uint16, port core.Port, seq uint64) {
	f, sf := wire.Decode(raw, magic)
	f.Header.Sequence = seq
	e.Process(f, port, sf)
}

// buildHeartbeatFrame builds a 16-byte Heartbeat frame with the given SendTS and channelID.
func buildHeartbeatFrame(magic uint16, sendTS uint64, ch uint8) []byte {
	return wb.Frame(magic).
		Channel(ch).
		SendTS(sendTS).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) {
			b.U8(ch)  // heartbeat channel_id (body[0])
			b.Pad(11) // remaining body bytes (total body 12 → msg 16)
		}).
		Bytes()
}

// buildManifestFrameWithTS builds a ManifestSummary frame with the given SendTS.
func buildManifestFrameWithTS(magic uint16, sendTS uint64, valid uint8, seq uint16, count uint32, ch uint8) []byte {
	return wb.Frame(magic).
		Channel(ch).
		SendTS(sendTS).
		Msg(0x07, 24, manifestBody(valid, seq, count)).
		Bytes()
}

// buildInstrDefFrameWithTS builds a TOB InstrumentDef frame with the given SendTS.
func buildInstrDefFrameWithTS(sendTS uint64, instrID uint32, manifestSeq uint16, ch uint8) []byte {
	return wb.Frame(wire.MagicTOB).
		Channel(ch).
		SendTS(sendTS).
		Msg(0x02, 80, instrDefTOBBody(instrID, manifestSeq)).
		Bytes()
}

// --- REFDATA.MANIFEST_CADENCE tests ---

// TestManifestCadenceViolation: with --expect-manifest-cadence=1s, a 3s gap
// between ManifestSummary messages on a gapless window → Violation.
func TestManifestCadenceViolation(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// First ManifestSummary at t=0 (seq=1). Seq=2 will pop seq=1 from the window.
	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	clearFindings(ac)

	// Second ManifestSummary at t=3s — gap is 3s > 1s threshold.
	// With ReorderWindow=1, processing seq=2 pops seq=1 and classifies it.
	// Then seq=2 is in the buffer. We need to trigger classification of seq=2
	// with a third frame or Flush.
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, 3*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)
	// seq=2 (the cadence-violation frame) is now in the buffer. Flush it.
	e.Flush()

	if !hasViolation(ac, "REFDATA.MANIFEST_CADENCE") {
		t.Error("REFDATA.MANIFEST_CADENCE: expected Violation for 3s gap with 1s threshold, got none")
	}
}

// TestManifestCadenceNoViolation: gap within the threshold → no Violation.
func TestManifestCadenceNoViolation(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 5 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	clearFindings(ac)

	// Gap of 2s < 5s threshold → no violation.
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, 2*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)
	e.Flush()

	if hasViolation(ac, "REFDATA.MANIFEST_CADENCE") {
		t.Error("REFDATA.MANIFEST_CADENCE: must not fire for gap within threshold")
	}
}

// TestManifestCadenceUnconfigured: without --expect-manifest-cadence, a large
// gap must NOT emit a Violation (rule is config-gated / Conditional downgrade).
func TestManifestCadenceUnconfigured(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB} // ExpectManifestCadence == 0
	e, ac := newCadenceEngine(cfg)

	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	clearFindings(ac)

	// 100s gap — would be a violation if configured.
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, 100*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)
	e.Flush()

	for _, f := range findingsFor(ac, "REFDATA.MANIFEST_CADENCE") {
		if f.Status == core.Violation {
			t.Error("REFDATA.MANIFEST_CADENCE: must not Violate when --expect-manifest-cadence is unset")
		}
	}
}

// TestManifestCadenceGapUnverifiable: a gap in the refdata seq window downgrades
// the finding to Unverifiable, not Violation.
func TestManifestCadenceGapUnverifiable(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// First summary.
	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	clearFindings(ac)

	// Jump to seq=5 (skipping seqs 2–4 → gap). The gap marks the port as dirty.
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, 3*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 5)
	e.Flush()

	// Must fire as Unverifiable, must NOT fire as Violation.
	hasUnverifiable := false
	for _, f := range findingsFor(ac, "REFDATA.MANIFEST_CADENCE") {
		if f.Status == core.Violation {
			t.Error("REFDATA.MANIFEST_CADENCE: must be Unverifiable (not Violation) when seq gap exists")
		}
		if f.Status == core.Unverifiable {
			hasUnverifiable = true
		}
	}
	if !hasUnverifiable {
		t.Error("REFDATA.MANIFEST_CADENCE: expected Unverifiable finding when seq gap exists, got none")
	}
}

// --- HEARTBEAT.CADENCE tests ---

// TestHeartbeatCadenceViolation: with --expect-heartbeat=1s, a 3s gap between
// consecutive Heartbeat messages on a NON-MBO feed → Violation.
// (Heartbeat is all-feed; we use TOB for this test.)
func TestHeartbeatCadenceViolation(t *testing.T) {
	cfg := Config{
		Feed:            core.FeedTOB,
		ExpectHeartbeat: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// First heartbeat at t=0.
	raw1 := buildHeartbeatFrame(wire.MagicTOB, 0, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortMktData, 1)
	clearFindings(ac)

	// Second heartbeat at t=3s — gap of 3s > 1s threshold → Violation.
	raw2 := buildHeartbeatFrame(wire.MagicTOB, 3*nsPerSec, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortMktData, 2)
	e.Flush()

	if !hasViolation(ac, "HEARTBEAT.CADENCE") {
		t.Error("HEARTBEAT.CADENCE: expected Violation for 3s gap with 1s threshold, got none")
	}
}

// TestHeartbeatCadenceNoViolation: gap within threshold → no Violation.
func TestHeartbeatCadenceNoViolation(t *testing.T) {
	cfg := Config{
		Feed:            core.FeedTOB,
		ExpectHeartbeat: 5 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	raw1 := buildHeartbeatFrame(wire.MagicTOB, 0, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortMktData, 1)
	clearFindings(ac)

	// Gap of 2s < 5s threshold → no Violation.
	raw2 := buildHeartbeatFrame(wire.MagicTOB, 2*nsPerSec, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortMktData, 2)
	e.Flush()

	if hasViolation(ac, "HEARTBEAT.CADENCE") {
		t.Error("HEARTBEAT.CADENCE: must not fire for gap within threshold")
	}
}

// TestHeartbeatCadenceUnconfigured: without --expect-heartbeat, a large gap
// must NOT emit a Violation.
func TestHeartbeatCadenceUnconfigured(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB} // ExpectHeartbeat == 0
	e, ac := newCadenceEngine(cfg)

	raw1 := buildHeartbeatFrame(wire.MagicTOB, 0, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortMktData, 1)
	clearFindings(ac)

	// 100s gap.
	raw2 := buildHeartbeatFrame(wire.MagicTOB, 100*nsPerSec, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortMktData, 2)
	e.Flush()

	for _, f := range findingsFor(ac, "HEARTBEAT.CADENCE") {
		if f.Status == core.Violation {
			t.Error("HEARTBEAT.CADENCE: must not Violate when --expect-heartbeat is unset")
		}
	}
}

// TestHeartbeatCadenceMidpointFeed: heartbeat cadence applies to all feeds,
// not just MBO. Verify on the Midpoint feed.
func TestHeartbeatCadenceMidpointFeed(t *testing.T) {
	cfg := Config{
		Feed:            core.FeedMidpoint,
		ExpectHeartbeat: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	raw1 := buildHeartbeatFrame(wire.MagicMid, 0, 1)
	processFrame(e, raw1, wire.MagicMid, core.PortMktData, 1)
	clearFindings(ac)

	// 2s gap > 1s threshold → Violation.
	raw2 := buildHeartbeatFrame(wire.MagicMid, 2*nsPerSec, 1)
	processFrame(e, raw2, wire.MagicMid, core.PortMktData, 2)
	e.Flush()

	if !hasViolation(ac, "HEARTBEAT.CADENCE") {
		t.Error("HEARTBEAT.CADENCE: expected Violation on Midpoint feed for 2s gap with 1s threshold")
	}
}

// TestHeartbeatCadenceSeqGapUnverifiable: a seq gap on the mktdata port
// downgrades the finding to Unverifiable.
func TestHeartbeatCadenceSeqGapUnverifiable(t *testing.T) {
	cfg := Config{
		Feed:            core.FeedTOB,
		ExpectHeartbeat: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	raw1 := buildHeartbeatFrame(wire.MagicTOB, 0, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortMktData, 1)
	clearFindings(ac)

	// Jump to seq=5 (gap of 3) — the port will be dirty.
	raw2 := buildHeartbeatFrame(wire.MagicTOB, 3*nsPerSec, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortMktData, 5)
	e.Flush()

	// Must fire as Unverifiable, must NOT fire as Violation.
	hasUnverifiable := false
	for _, f := range findingsFor(ac, "HEARTBEAT.CADENCE") {
		if f.Status == core.Violation {
			t.Error("HEARTBEAT.CADENCE: must be Unverifiable (not Violation) when seq gap exists")
		}
		if f.Status == core.Unverifiable {
			hasUnverifiable = true
		}
	}
	if !hasUnverifiable {
		t.Error("HEARTBEAT.CADENCE: expected Unverifiable finding when seq gap exists, got none")
	}
}

// --- REFDATA.DEFINITION_CYCLE_COVERAGE tests ---

// reachedReady is a helper sequence that builds cycle 1 (initial delivery):
// Manifest(t=0,count=2) → Def100 → Def200 → Manifest(t=1.5s, closes cycle 1).
// Returns the next sequence number to use.
func reachedReadySeq(e *Engine, magic uint16, startSeq uint64) uint64 {
	seq := startSeq
	processFrame(e, buildManifestFrameWithTS(magic, 0, 1, 1, 2, 1), magic, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), magic, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec/10, 200, 1, 1), magic, core.PortRefData, seq)
	seq++
	// Close cycle 1 at t=1.5s. Cycle 2 starts here.
	processFrame(e, buildManifestFrameWithTS(magic, 3*nsPerSec/2, 1, 1, 2, 1), magic, core.PortRefData, seq)
	seq++
	return seq
}

// TestDefinitionCycleCoverage: with --expect-definition-cycle=1s, after the set
// is established (cycle 1 done), cycle 2 has only 1 of 2 defs retransmitted →
// Violation.
func TestDefinitionCycleCoverage(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness (all defs delivered).
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: retransmit only def100 (not def200).
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++

	// Close cycle 2 at t=4s (cycle length 4s-1.5s=2.5s ≥ 1s threshold).
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 4*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	if !hasViolation(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE") {
		t.Error("REFDATA.DEFINITION_CYCLE_COVERAGE: expected Violation for incomplete cycle, got none")
	}
}

// TestDefinitionCycleCoverageComplete: cycle 2 has all defs retransmitted → no Violation.
func TestDefinitionCycleCoverageComplete(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness.
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: retransmit both defs.
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(3*nsPerSec, 200, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++

	// Close cycle 2 at t=5s.
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 5*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	if hasViolation(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE") {
		t.Error("REFDATA.DEFINITION_CYCLE_COVERAGE: must not fire when all defs retransmitted")
	}
}

// TestDefinitionCycleCoverageWrongInstruments: cycle 2 only retransmits instrID
// 100 twice (never instrID 200) — Violation (set-membership check against the
// frozen snapshot {100, 200}). This also covers the case where len(defs) <
// expectedCount so channelReady() would be false; the coverage check must use the
// frozen setSnapshot, not a live channelReady() guard.
func TestDefinitionCycleCoverageWrongInstruments(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness with active set {100, 200}.
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: retransmit instrID 100 twice — instrID 200 is never seen this
	// cycle.  len(defs) will be 1 (< expectedCount 2) so channelReady() is false,
	// but the coverage check must still fire using the frozen setSnapshot {100,200}.
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(3*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq) // 100 again
	seq++

	// Close cycle 2 at t=5s.
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 5*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	if !hasViolation(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE") {
		t.Error("REFDATA.DEFINITION_CYCLE_COVERAGE: expected Violation when instrID 200 not retransmitted, got none")
	}
}

// TestDefinitionCycleCoverageUnconfigured: without --expect-definition-cycle,
// must not emit a Violation regardless of coverage.
func TestDefinitionCycleCoverageUnconfigured(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB} // ExpectDefinitionCycle == 0
	e, ac := newCadenceEngine(cfg)

	// Establish readiness.
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: only def100 retransmitted.
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 4*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	for _, f := range findingsFor(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE") {
		if f.Status == core.Violation {
			t.Error("REFDATA.DEFINITION_CYCLE_COVERAGE: must not Violate when --expect-definition-cycle is unset")
		}
	}
}

// TestDefinitionCycleCoverageRealCadence proves the coverage check fires at spec
// cadence — manifests arriving far more frequently than the definition cycle.
// Under the old per-manifest cycle reset this was structurally dead (the
// inter-manifest gap never reached ExpectDefinitionCycle); the tumbling window
// fixes it. Cycle = 3s, manifests every 1s. Cycle 1 establishes {100,200} and
// passes; cycle 2 retransmits only 100 (200 dropped) → Violation at close.
func TestDefinitionCycleCoverageRealCadence(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB, ExpectDefinitionCycle: 3 * time.Second}
	e, ac := newCadenceEngine(cfg)
	magic := wire.MagicTOB
	seq := uint64(1)
	mf := func(ts uint64) { // ManifestSummary (seq=1, count=2) at SendTS=ts
		processFrame(e, buildManifestFrameWithTS(magic, ts, 1, 1, 2, 1), magic, core.PortRefData, seq)
		seq++
	}
	df := func(ts uint64, id uint32) { // InstrumentDefinition for id at SendTS=ts
		processFrame(e, buildInstrDefFrameWithTS(ts, id, 1, 1), magic, core.PortRefData, seq)
		seq++
	}

	// Cycle 1 window [0, 3s): establish active set {100,200}; manifests every 1s.
	mf(0)
	df(nsPerSec/10, 100) // 0.1s
	df(nsPerSec/5, 200)  // 0.2s
	mf(nsPerSec)         // 1s
	mf(2 * nsPerSec)     // 2s
	mf(3 * nsPerSec)     // 3s → closes cycle 1 (both seen → pass), reopens at 3s
	clearFindings(ac)

	// Cycle 2 window [3s, 6s): retransmit only 100; manifests still 1s apart.
	df(3*nsPerSec+nsPerSec/10, 100) // 3.1s
	mf(4 * nsPerSec)                // 4s
	df(4*nsPerSec+nsPerSec/10, 100) // 4.1s
	mf(5 * nsPerSec)                // 5s
	mf(6 * nsPerSec)                // 6s → closes cycle 2: instrID 200 never retransmitted
	e.Flush()

	if !hasViolation(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE") {
		t.Error("REFDATA.DEFINITION_CYCLE_COVERAGE: expected Violation when an instrument is dropped at spec cadence (manifests far more frequent than the cycle); the old per-manifest reset left this dead")
	}
}

// --- REFDATA.NO_BURST_DEFINITIONS tests ---

// TestNoBurstDefinitions: in cycle 2 (after readiness established), all defs
// arrive at the same SendTS (burst) → Violation.
func TestNoBurstDefinitions(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness (initial delivery with different SendTS values).
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: retransmit both defs at the SAME SendTS — burst.
	const burstTS = 2 * nsPerSec
	processFrame(e, buildInstrDefFrameWithTS(burstTS, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(burstTS, 200, 1, 1), wire.MagicTOB, core.PortRefData, seq) // same SendTS
	seq++

	// Close cycle 2 at t=4s (cycle length ≥ 1s).
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 4*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	if !hasViolation(ac, "REFDATA.NO_BURST_DEFINITIONS") {
		t.Error("REFDATA.NO_BURST_DEFINITIONS: expected Violation for burst (all defs at same SendTS), got none")
	}
}

// TestNoBurstDefinitionsPaced: in cycle 2, defs arrive at distinct SendTS values → no Violation.
func TestNoBurstDefinitionsPaced(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness.
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: retransmit defs at different SendTS values (paced).
	processFrame(e, buildInstrDefFrameWithTS(2*nsPerSec, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(3*nsPerSec, 200, 1, 1), wire.MagicTOB, core.PortRefData, seq) // different SendTS
	seq++

	// Close cycle 2 at t=5s.
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 5*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	if hasViolation(ac, "REFDATA.NO_BURST_DEFINITIONS") {
		t.Error("REFDATA.NO_BURST_DEFINITIONS: must not fire for paced defs at distinct SendTS values")
	}
}

// TestNoBurstDefinitionsUnconfigured: without --expect-definition-cycle, burst
// must not emit a Violation.
func TestNoBurstDefinitionsUnconfigured(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB}
	e, ac := newCadenceEngine(cfg)

	// Cycle 1: establish readiness.
	seq := reachedReadySeq(e, wire.MagicTOB, 1)
	clearFindings(ac)

	// Cycle 2: burst retransmit.
	const burstTS = 2 * nsPerSec
	processFrame(e, buildInstrDefFrameWithTS(burstTS, 100, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildInstrDefFrameWithTS(burstTS, 200, 1, 1), wire.MagicTOB, core.PortRefData, seq)
	seq++
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 4*nsPerSec, 1, 1, 2, 1), wire.MagicTOB, core.PortRefData, seq)
	e.Flush()

	for _, f := range findingsFor(ac, "REFDATA.NO_BURST_DEFINITIONS") {
		if f.Status == core.Violation {
			t.Error("REFDATA.NO_BURST_DEFINITIONS: must not Violate when --expect-definition-cycle is unset")
		}
	}
}

// --- REFDATA.NEVER_REACHES_READY tests ---

// TestNeverReachesReady: channel observed for ≥ ManifestCadence+DefinitionCycle
// but never reached ready → EndRun fires Violation.
func TestNeverReachesReady(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Send ManifestSummary at t=0 with count=2, but only ever deliver 1 def.
	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)

	// Only 1 def delivered — channel never reaches ready (count=2, only 1 def).
	raw2 := buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)

	// Second ManifestSummary at t=3s: span = 3s ≥ window (2s).
	raw3 := buildManifestFrameWithTS(wire.MagicTOB, 3*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw3, wire.MagicTOB, core.PortRefData, 3)

	e.Flush()
	clearFindings(ac)
	e.EndRun()

	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: expected Violation at EndRun for channel that never reached ready, got none")
	}
}

// TestNeverReachesReadyDoesNotFireWhenReady: channel that did reach ready →
// no Violation at EndRun.
func TestNeverReachesReadyDoesNotFireWhenReady(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Channel reaches ready (count=1, 1 def).
	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 1, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	raw2 := buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)

	// Second ManifestSummary at t=3s.
	raw3 := buildManifestFrameWithTS(wire.MagicTOB, 3*nsPerSec, 1, 1, 1, 1)
	processFrame(e, raw3, wire.MagicTOB, core.PortRefData, 3)

	e.Flush()
	clearFindings(ac)
	e.EndRun()

	if hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: must not fire when channel has reached ready")
	}
}

// TestNeverReachesReadyUnconfigured: without both --expect-* values, EndRun
// must not emit a Violation.
func TestNeverReachesReadyUnconfigured(t *testing.T) {
	cfg := Config{Feed: core.FeedTOB} // no ExpectManifestCadence or ExpectDefinitionCycle
	e, ac := newCadenceEngine(cfg)

	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, 10*nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)

	e.Flush()
	clearFindings(ac)
	e.EndRun()

	for _, f := range findingsFor(ac, "REFDATA.NEVER_REACHES_READY") {
		if f.Status == core.Violation {
			t.Error("REFDATA.NEVER_REACHES_READY: must not Violate when --expect-* flags are unset")
		}
	}
}

// TestNeverReachesReadyWindowTooShort: channel observed for less than the full
// window → EndRun must NOT fire (observation span too short to be conclusive).
func TestNeverReachesReadyWindowTooShort(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 2 * time.Second,
		ExpectDefinitionCycle: 2 * time.Second,
	}
	e, ac := newCadenceEngine(cfg)

	// Only 1s of observation span (< 4s window).
	raw1 := buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 2, 1)
	processFrame(e, raw1, wire.MagicTOB, core.PortRefData, 1)
	raw2 := buildManifestFrameWithTS(wire.MagicTOB, nsPerSec, 1, 1, 2, 1)
	processFrame(e, raw2, wire.MagicTOB, core.PortRefData, 2)

	e.Flush()
	clearFindings(ac)
	e.EndRun()

	if hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: must not fire when observation span < window")
	}
}
