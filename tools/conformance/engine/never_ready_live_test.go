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
// non-terminal ones still wait, each era says its piece exactly once, and a channel
// that reaches ready *late* is a violation rather than a pass.

import (
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// liveReadyEngine is an engine with both --expect-* values set, which is what arms
// this rule at all.
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
		t.Errorf("REFDATA.NEVER_REACHES_READY: reported %d times across one era, want exactly 1", n)
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
		t.Error("REFDATA.NEVER_REACHES_READY: a late ready must not also report a pass for the same era")
	}
}

// TestNeverReachesReadyReArmsOnANewEra: the verdict is per era. A Reset Count change
// starts a fresh subscriber, which gets its own window — otherwise one bad era would
// silence the rule for the life of the process, which is the same unreachability in a
// different disguise.
func TestNeverReachesReadyReArmsOnANewEra(t *testing.T) {
	e, ac := liveReadyEngine(t)

	// Frame sequence is contiguous throughout, in both eras. A single skipped seq
	// latches the dirty window for the channel instance — it is cleared only by a
	// publisher reset, not by onResetChannel — and every later verdict would
	// downgrade to Unverifiable/loss, which is a different arm than the one here.
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
