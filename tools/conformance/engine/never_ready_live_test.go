package engine

// never_ready_live_test.go — REFDATA.NEVER_REACHES_READY in a process that never ends.
//
// The rule asks whether a fresh both-port subscriber reached ready() within manifest
// cadence + definition cycle. That is decidable the moment the window closes, and
// deciding it only at EndRun made the rule unreachable in the deployment it matters
// most in: a `dz-conformance@` systemd instance runs until it is stopped, so the
// verdict never arrived and no alert could ever see it (#50).
//
// These tests pin the live contract: terminal verdicts land when they become true,
// non-terminal ones still wait, each serving period says its piece exactly once, and a
// channel that reaches ready *late* is a violation rather than a pass.

import (
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// liveReadyEngine is an engine with both --expect-* values set, which is what puts
// this rule in scope at all.
func liveReadyEngine(t *testing.T) (*Engine, *allCapture) {
	t.Helper()
	return newCadenceEngine(Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
	})
}

// manifestAt feeds one ManifestSummary(valid=1, count) at a wire time.
func manifestAt(e *Engine, seq uint64, sendTS uint64, count uint32) {
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, sendTS, 1, 1, count, 1),
		wire.MagicTOB, core.PortRefData, seq)
}

// TestNeverReachesReadyDecidesWithoutEndRun is the issue itself: a publisher that
// announces two instruments and ships one, watched well past the window, must be
// reported by a process that never exits.
func TestNeverReachesReadyDecidesWithoutEndRun(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	// Manifests keep arriving at 1/s, as a live publisher's do. The window (2s)
	// closes at t=2s and the verdict is due there, not at some later exit.
	for i := 2; i <= 6; i++ {
		manifestAt(e, uint64(i+1), uint64(i)*nsPerSec, 2)
	}
	e.Flush()

	// EndRun is deliberately NOT called: this is the systemd shape.
	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Fatal("REFDATA.NEVER_REACHES_READY: no verdict in a run that never ends — the rule is unreachable, which is #50")
	}
	if n := len(findingsFor(ac, "REFDATA.NEVER_REACHES_READY")); n != 1 {
		t.Errorf("REFDATA.NEVER_REACHES_READY: reported %d times across one serving period, want exactly 1", n)
	}
}

// TestNeverReachesReadyDefersWhileTheWindowIsOpen is the converse: before the window
// closes, "not ready yet" is not an answer, and reporting one would blame a publisher
// that is still inside the time the rule allows it.
func TestNeverReachesReadyDefersWhileTheWindowIsOpen(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	// t=1s: span is 1s against a 2s window.
	manifestAt(e, 3, nsPerSec, 2)
	e.Flush()

	if len(findingsFor(ac, "REFDATA.NEVER_REACHES_READY")) != 0 {
		t.Error("REFDATA.NEVER_REACHES_READY: decided inside the window; a publisher still has time to reach ready")
	}
}

// TestNeverReachesReadyLateReadyIsAViolation pins the masking bug the move fixes.
// EndRun only ever asked whether ready was reached, never whether it was reached in
// time, so a channel that took far longer than the window reported a pass. The window
// is the rule.
func TestNeverReachesReadyLateReadyIsAViolation(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	for i := 2; i <= 6; i++ {
		manifestAt(e, uint64(i+1), uint64(i)*nsPerSec, 2)
	}
	// The second definition finally arrives at t=7s, long after the 2s window.
	processFrame(e, buildInstrDefFrameWithTS(7*nsPerSec, 101, 1, 1), wire.MagicTOB, core.PortRefData, 8)
	manifestAt(e, 9, 8*nsPerSec, 2)
	e.Flush()
	e.EndRun()

	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: reaching ready 7s into a 2s window is a violation, not a pass")
	}
	if hasPass(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: a late ready must not also report a pass for the same serving period")
	}
}

// TestNeverReachesReadyDecidesAgainOnANewEra: a Reset Count change starts a fresh
// subscriber, which gets its own window — otherwise one bad serving period would
// silence the rule for the life of the process, which is the same unreachability in a
// different disguise.
func TestNeverReachesReadyDecidesAgainOnANewEra(t *testing.T) {
	e, ac := liveReadyEngine(t)

	// Frame sequence is contiguous throughout, in both eras. A single skipped seq
	// latches the dirty window for the channel instance — it is cleared only by a
	// publisher reset, not by onResetChannel — and every later verdict would
	// downgrade to Unverifiable/loss, which is a different branch than the one here.
	for i := 1; i <= 6; i++ {
		manifestAt(e, uint64(i), uint64(i-1)*nsPerSec, 2)
	}
	e.Flush()
	if n := len(findingsFor(ac, "REFDATA.NEVER_REACHES_READY")); n != 1 {
		t.Fatalf("setup: want 1 verdict for the first era, got %d", n)
	}

	// New era on this channel instance: state is discarded and the window reopens.
	e.refdata.onResetChannel(1)
	clearFindings(ac)

	for i := 7; i <= 12; i++ {
		manifestAt(e, uint64(i), uint64(i+13)*nsPerSec, 2)
	}
	e.Flush()

	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: a new era must get its own verdict, not inherit the last one's silence")
	}
}

// TestNeverReachesReadyLateReadyWithNoTrailingSummaryIsAViolation is the same masking
// bug reached from the side the span cannot see. The observation span ends at the last
// Valid=1 summary, so where none lands between the window closing and a late readiness
// there is no span past the window at all — and a check that short-circuits on "ready
// was reached" then reports a Pass for a channel that took half again the window. The
// readiness timestamp is what settles it.
func TestNeverReachesReadyLateReadyWithNoTrailingSummaryIsAViolation(t *testing.T) {
	// Window is cadence + cycle = 6s.
	e, ac := newCadenceEngine(Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 5 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
	})

	// One summary announcing two instruments, and one of them at t=0.1s.
	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)

	// The second definition completes the set at t=9s — 3s past the window — and no
	// summary follows it, so lastServingSendTS is still the t=0 one and the span
	// stays 0.
	processFrame(e, buildInstrDefFrameWithTS(9*nsPerSec, 101, 1, 1), wire.MagicTOB, core.PortRefData, 3)
	e.Flush()
	e.EndRun()

	if hasPass(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: ready at 9s in a 6s window is a violation, not a pass")
	}
	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: no verdict at all — a late ready with no summary behind it must still be graded")
	}
}

// TestNeverReachesReadyDoesNotVivifyAChannel: deciding per datagram must look the
// channel up, not create it. A refdata datagram carrying neither a ManifestSummary nor
// an InstrumentDefinition — a Heartbeat, or a runt decoding to the all-zero header —
// otherwise materializes state for a channel that has no reference data, and EndRun
// then grades a channel that was never there.
func TestNeverReachesReadyDoesNotVivifyAChannel(t *testing.T) {
	e, ac := liveReadyEngine(t)

	// Channel 1 is real and healthy: announced one instrument, shipped it.
	processFrame(e, buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 1, 1), wire.MagicTOB, core.PortRefData, 1)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)

	// Channel 7 carries a Heartbeat and nothing else.
	processFrame(e, buildHeartbeatFrame(wire.MagicTOB, nsPerSec, 7), wire.MagicTOB, core.PortRefData, 3)
	e.Flush()
	e.EndRun()

	for _, f := range findingsFor(ac, "REFDATA.NEVER_REACHES_READY") {
		if f.ChannelID == 7 {
			t.Errorf("REFDATA.NEVER_REACHES_READY: graded channel 7, which carried no reference data: %s", f.Detail)
		}
	}
}

// processFrameSchema is processFrame with the header's Schema Version overridden after
// decode, so the datagram reaches the engine as one from a publisher ahead of us
// without wire.Decode itself reporting FRAME.SCHEMA_VERSION. That isolates the
// downgrade beginFrame applies, which is what is under test here.
func processFrameSchema(e *Engine, raw []byte, magic uint16, port core.Port, seq uint64, schema uint8) {
	f, sf := wire.Decode(raw, magic)
	f.Header.Sequence = seq
	f.Header.SchemaVersion = schema
	e.Process(srcA, f, port, sf)
}

// TestNeverReachesReadyRecordsTheDecidingSeq: the verdict now claims to land at the
// datagram that settled it, so it has to say which one. Seq 0 was harmless while the
// rule only spoke from EndRun, where there is no datagram to point at; live, an
// operator alerting on this has to be able to find it in a capture.
func TestNeverReachesReadyRecordsTheDecidingSeq(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	// Frame seq 3 carries t=2s, which is where the 2s window closes.
	for i := 2; i <= 6; i++ {
		manifestAt(e, uint64(i+1), uint64(i)*nsPerSec, 2)
	}
	e.Flush()

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) != 1 {
		t.Fatalf("setup: want exactly 1 verdict, got %d", len(found))
	}
	if found[0].Seq != 3 {
		t.Errorf("REFDATA.NEVER_REACHES_READY: recorded seq %d, want 3 — the datagram that closed the window", found[0].Seq)
	}
}

// TestNeverReachesReadyDoesNotLatchASchemaDowngrade: an unknown (higher) schema version
// downgrades every non-envelope rule for the datagram being classified, and deciding
// mid-run means the verdict inherits the version of whichever datagram happened to
// close the window. neverReadyDecided would then make that permanent — one datagram
// from a mid-upgrade publisher, arriving at exactly the wrong moment, and the period's
// Violation is recorded as NA/Info with no later datagram able to restate it. A
// non-final decision defers instead.
func TestNeverReachesReadyDoesNotLatchASchemaDowngrade(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	manifestAt(e, 3, nsPerSec, 2)

	// t=2s closes the 2s window, and this is the one datagram from a publisher ahead
	// of us.
	processFrameSchema(e, buildManifestFrameWithTS(wire.MagicTOB, 2*nsPerSec, 1, 1, 2, 1),
		wire.MagicTOB, core.PortRefData, 4, wire.ExpectedSchemaVersion(wire.MagicTOB)+1)
	e.Flush()

	// A clean datagram behind it, which is the one that should decide the period.
	manifestAt(e, 5, 3*nsPerSec, 2)
	e.Flush()

	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: the period is stuck on the downgrade one datagram forced; a later clean datagram must still decide it")
	}
}

// TestNeverReachesReadyHalfConfiguredIsNotSilent: the second silence path, the same
// shape as #50 reached through the config. Config.Configured used to call the rule
// configured on ExpectDefinitionCycle alone, so a run with --expect-definition-cycle
// and no --expect-manifest-cadence kept a Must rule in scope while the guard returned
// without a word for every channel. The two agree now, and the run says what it could
// not measure instead of saying nothing.
func TestNeverReachesReadyHalfConfiguredIsNotSilent(t *testing.T) {
	e, ac := newCadenceEngine(Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	})

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	manifestAt(e, 3, 10*nsPerSec, 2)
	e.Flush()
	e.EndRun()

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) == 0 {
		t.Fatal("REFDATA.NEVER_REACHES_READY: no verdict at all with only one --expect-* set — silence is the #50 failure, in a different disguise")
	}
	for _, f := range found {
		if f.Status == core.Violation {
			t.Errorf("REFDATA.NEVER_REACHES_READY: violated on a window it cannot measure: %s", f.Detail)
		}
		if f.Severity != core.Info {
			t.Errorf("REFDATA.NEVER_REACHES_READY: severity %v, want Info — the rule is not configured, so it must not claim Must coverage", f.Severity)
		}
	}
}
