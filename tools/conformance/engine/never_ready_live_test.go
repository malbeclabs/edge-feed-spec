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
//
// They also pin what a live verdict must not do. It must not grade a set the engine has
// not read to the end of — the definitions that complete it can still be in the
// instance's reorder buffer — and it must not measure a serving period with the
// clock of the one before it, which is what a publisher restart leaves behind.

import (
	"net/netip"
	"slices"
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
//
// **No manifests between the window closing and the late definition**, and that absence is
// the test. With them, the span branch latches the violation before the late definition is
// ever classified, so the test passed with the readiness deadline at
// `decideNeverReachesReady` disabled entirely — it pinned the wrong branch. Here the only
// thing that can produce a verdict is the readiness comparison, because nothing else has
// closed the period: the channel reaches ready at t=7s and the deadline is what judges it.
func TestNeverReachesReadyLateReadyIsAViolation(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	// The second definition finally arrives at t=7s, long after the 2s window, and it is
	// the datagram that makes the channel ready.
	processFrame(e, buildInstrDefFrameWithTS(7*nsPerSec, 101, 1, 1), wire.MagicTOB, core.PortRefData, 3)
	manifestAt(e, 4, 8*nsPerSec, 2)
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

	// Datagram sequence is contiguous throughout, in both eras. A single skipped seq
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
	e.refdata.onResetChannel(refdataKey{src: srcA, ch: 1})
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

// processDatagramSchema is processFrame with the header's Schema Version overridden after
// decode, so the datagram reaches the engine as one from a publisher ahead of us
// without wire.Decode itself reporting FRAME.SCHEMA_VERSION. That isolates the
// downgrade beginFrame applies, which is what is under test here.
func processDatagramSchema(e *Engine, raw []byte, magic uint16, port core.Port, seq uint64, schema uint8) {
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
	// Seq 3 carries t=2s, which is the 2s deadline itself, and seq 4 carries t=3s — the
	// first datagram PAST it. The deadline is inclusive (see decideNeverReachesReady), so
	// a publisher that has not reached ready at exactly 2s still had until that instant
	// and the verdict belongs at seq 4.
	for i := 2; i <= 6; i++ {
		manifestAt(e, uint64(i+1), uint64(i)*nsPerSec, 2)
	}
	e.Flush()

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) != 1 {
		t.Fatalf("setup: want exactly 1 verdict, got %d", len(found))
	}
	if found[0].Seq != 4 {
		t.Errorf("REFDATA.NEVER_REACHES_READY: recorded seq %d, want 4 — the first datagram past the window", found[0].Seq)
	}
}

// unsupportedSchema returns the lowest Schema Version this feed does not support, so a
// test that needs "a datagram this validator cannot decode" says exactly that rather than
// assuming the unsupported ones are the high ones.
func unsupportedSchema(magic uint16) uint8 {
	supported := wire.SupportedSchemas(magic)
	for v := uint8(1); v < 255; v++ {
		if !slices.Contains(supported, v) {
			return v
		}
	}
	panic("every schema version is supported; this test needs rewriting")
}

// TestNeverReachesReadyDoesNotLatchASchemaDowngrade: an UNSUPPORTED schema version
// downgrades every non-envelope rule for the datagram being classified, and deciding
// mid-run means the verdict inherits the version of whichever datagram happened to close
// the window. neverReadyDecided would then make that permanent — one such datagram,
// arriving at exactly the wrong moment, and the period's Violation is recorded as NA/Info
// with no later datagram able to restate it. A non-final decision defers instead.
//
// **Unsupported is set membership, not ordering.** `wire.SupportedSchemas` returns {1, 3}
// for top of book, so an unsupported version can be BELOW the current one — 2 is the live
// example — and the old framing of this test ("an unknown (higher) version", "a publisher
// ahead of us") rested on an ordering that does not exist. unsupportedSchema picks the
// first version the feed does not support, so the test cannot rot with the set.
func TestNeverReachesReadyDoesNotLatchASchemaDowngrade(t *testing.T) {
	e, ac := liveReadyEngine(t)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	manifestAt(e, 3, nsPerSec, 2)

	// t=2s closes the 2s window, and this is the one datagram from a publisher ahead
	// of us.
	processDatagramSchema(e, buildManifestFrameWithTS(wire.MagicTOB, 2*nsPerSec, 1, 1, 2, 1),
		wire.MagicTOB, core.PortRefData, 4, unsupportedSchema(wire.MagicTOB))
	e.Flush()

	// A clean datagram behind it, which is the one that should decide the period.
	manifestAt(e, 5, 3*nsPerSec, 2)
	e.Flush()

	if !hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: the period is stuck on the downgrade one datagram forced; a later clean datagram must still decide it")
	}
}

// TestNeverReachesReadyHalfConfiguredIsOutOfScopeAndSaysNothing: a half-configured run
// takes the rule out of scope, and out of scope means **no verdict at all**.
//
// This is the config half of #50 answered the way `denominator.go` requires rather than
// the way it was first written here: "A rule that is off because its `--expect-*` flag was
// not passed reports nothing… This invariant is about stream state, not configuration." An
// `inapplicable` for a Must rule on every run missing a flag is a claim about the feed
// manufactured out of a fact about the command line.
//
// The silence #50 is about is a different thing, and `Config.Configured` is what keeps them
// apart: it requires BOTH flags for this rule, so a run with one of them has the rule out
// of scope AND out of the denominator, rather than in scope and quiet — which is the state
// that made coverage and no-op indistinguishable.
func TestNeverReachesReadyHalfConfiguredIsOutOfScopeAndSaysNothing(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	if cfg.Configured("REFDATA.NEVER_REACHES_READY") {
		t.Fatal("setup: the rule must be out of scope with only one --expect-* set; the denominator claim depends on it")
	}

	e, ac := newCadenceEngine(cfg)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	manifestAt(e, 3, 10*nsPerSec, 2)
	e.Flush()
	e.EndRun()

	if found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY"); len(found) != 0 {
		t.Errorf("REFDATA.NEVER_REACHES_READY: %d verdict(s) on a run that did not configure the window; out of scope reports nothing (%s)",
			len(found), found[0].Detail)
	}
}

// processDatagramFrom is processFrame from a named path, so a test can put two
// redundant publishers on one channel. Each path owns its own reorder buffer.
func processDatagramFrom(e *Engine, src netip.Addr, raw []byte, port core.Port, seq uint64) {
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = seq
	e.Process(src, f, port, sf)
}

// processDatagramInEra is processFrame with the header's Reset Count overridden after
// decode, which is how a test drives a publisher restart from the wire rather than by
// calling onResetChannel behind the engine's back.
func processDatagramInEra(e *Engine, raw []byte, seq uint64, era uint8) {
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = seq
	f.Header.ResetCount = era
	e.Process(srcA, f, core.PortRefData, sf)
}

// TestNeverReachesReadyWaitsForTheDefinitionsStillInAReorderBuffer: a verdict taken while
// the instance's own reorder buffer still holds an in-window datagram grades a set the
// engine has not read to the end of, and neverReadyDecided makes that first answer the
// period's only answer.
//
// The drain is what exposes it: drainAll empties the heap before the first item is
// classified, so the datagrams behind the current one are in flight and invisible to
// anything that asks the buffer. Here the definitions carry SendTS values inside the
// window but sequence numbers after the summary that closes it, so the set completes at
// t=0.2s of a 2s window and is classified after t=3s. The Violation this would report
// is a false one.
//
// **Reorder window 8, the CLI default.** At 1 a path holds one datagram, and the shape
// this covers needs a buffer deep enough to hold a whole serving period.
func TestNeverReachesReadyWaitsForTheDefinitionsStillInAReorderBuffer(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
		ReorderWindow:         8,
	}, ac)

	manifest := func(ts uint64) []byte { return buildManifestFrameWithTS(wire.MagicTOB, ts, 1, 1, 2, 1) }
	path := [][]byte{manifest(0), manifest(nsPerSec), manifest(2 * nsPerSec), manifest(3 * nsPerSec),
		buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), buildInstrDefFrameWithTS(nsPerSec/5, 101, 1, 1)}
	for i, raw := range path {
		processDatagramFrom(e, srcA, raw, core.PortRefData, uint64(i+1))
	}
	e.Flush()
	e.EndRun()

	if hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Errorf("REFDATA.NEVER_REACHES_READY: the channel was ready at t=0.2s of a 2s window; the verdict was taken over datagrams still in flight (%s)",
			findingsFor(ac, "REFDATA.NEVER_REACHES_READY")[0].Detail)
	}
	if !hasPass(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: no pass for a channel that reached ready inside its window")
	}
}

// TestNeverReachesReadyGradesEachInstanceOnItsOwnSet: two paths on one Channel ID are two
// channel instances, and each one is graded on the set it sent. Path A sends instrument
// 100 and path B sends instrument 101 of a set of two, so neither instance completes its
// set. Keyed by Channel ID, the two paths filled one set and the channel passed.
func TestNeverReachesReadyGradesEachInstanceOnItsOwnSet(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
		ReorderWindow:         8,
	}, ac)

	manifest := func(ts uint64) []byte { return buildManifestFrameWithTS(wire.MagicTOB, ts, 1, 1, 2, 1) }
	pathA := [][]byte{manifest(0), buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1),
		manifest(nsPerSec), manifest(2 * nsPerSec), manifest(3 * nsPerSec)}
	pathB := [][]byte{manifest(0), buildInstrDefFrameWithTS(nsPerSec/5, 101, 1, 1),
		manifest(nsPerSec), manifest(2 * nsPerSec), manifest(3 * nsPerSec)}
	for i := range pathA {
		processDatagramFrom(e, srcA, pathA[i], core.PortRefData, uint64(i+1))
		processDatagramFrom(e, srcB, pathB[i], core.PortRefData, uint64(i+1))
	}
	e.Flush()
	e.EndRun()

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) != 2 {
		t.Fatalf("REFDATA.NEVER_REACHES_READY: want one verdict per instance (2), got %d", len(found))
	}
	for _, f := range found {
		if f.Status != core.Violation {
			t.Errorf("REFDATA.NEVER_REACHES_READY: want a Violation for each instance, got %v (%s); one path's definitions completed the other path's set",
				f.Status, f.Detail)
		}
	}
}

// TestNeverReachesReadyClearsTheWindowClockOnANewEra: the clock the window closes on is
// period-scoped like the period's first summary, so a publisher restart has to clear it.
// Left behind, the new era is measured from the old era's last datagram and its first
// Valid=1 summary is an instant Must Violation — a conformance failure manufactured out
// of a restart, and latched for the era.
func TestNeverReachesReadyClearsTheWindowClockOnANewEra(t *testing.T) {
	e, ac := liveReadyEngine(t)

	// Era 0: one instrument, announced and shipped, then cadence out to t=100s.
	processDatagramInEra(e, buildManifestFrameWithTS(wire.MagicTOB, 0, 1, 1, 1, 1), 1, 0)
	processDatagramInEra(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), 2, 0)
	processDatagramInEra(e, buildManifestFrameWithTS(wire.MagicTOB, 50*nsPerSec, 1, 1, 1, 1), 3, 0)
	processDatagramInEra(e, buildManifestFrameWithTS(wire.MagicTOB, 100*nsPerSec, 1, 1, 1, 1), 4, 0)
	e.Flush()
	clearFindings(ac)

	// Era 1: the publisher restarted. Its clock starts again at t=1s and both of the
	// instruments it announces arrive inside the 2s window.
	processDatagramInEra(e, buildManifestFrameWithTS(wire.MagicTOB, nsPerSec, 1, 1, 2, 1), 5, 1)
	processDatagramInEra(e, buildInstrDefFrameWithTS(nsPerSec+nsPerSec/10, 100, 1, 1), 6, 1)
	processDatagramInEra(e, buildInstrDefFrameWithTS(nsPerSec+nsPerSec/5, 101, 1, 1), 7, 1)
	processDatagramInEra(e, buildManifestFrameWithTS(wire.MagicTOB, 2*nsPerSec, 1, 1, 2, 1), 8, 1)
	e.Flush()
	e.EndRun()

	if hasViolation(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Errorf("REFDATA.NEVER_REACHES_READY: the new era reached ready 0.2s into its 2s window; it was graded on the era before it (%s)",
			findingsFor(ac, "REFDATA.NEVER_REACHES_READY")[0].Detail)
	}
	if !hasPass(ac, "REFDATA.NEVER_REACHES_READY") {
		t.Error("REFDATA.NEVER_REACHES_READY: no verdict for the new era; a restart gets its own window, not silence")
	}
}

// TestNeverReachesReadyNegativeExpectationIsOutOfScopeToo: `Configured` reads both flags
// as `> 0`, so a negative duration takes the rule out of scope there. The decision ladder
// tested only for zero, so the same run computed a negative window, found every elapsed
// time greater than it, and reported against a deadline no publisher could meet. The two
// have to agree on what "configured" means.
func TestNeverReachesReadyNegativeExpectationIsOutOfScopeToo(t *testing.T) {
	cfg := Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: -1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
	}
	if cfg.Configured("REFDATA.NEVER_REACHES_READY") {
		t.Fatal("setup: a negative --expect-* takes the rule out of scope")
	}

	e, ac := newCadenceEngine(cfg)

	manifestAt(e, 1, 0, 2)
	processFrame(e, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), wire.MagicTOB, core.PortRefData, 2)
	manifestAt(e, 3, 10*nsPerSec, 2)
	e.Flush()
	e.EndRun()

	if found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY"); len(found) != 0 {
		t.Errorf("REFDATA.NEVER_REACHES_READY: %d verdict(s) against a negative window (%s)", len(found), found[0].Detail)
	}
}

// reassignedPathEngine is liveReadyEngine at the CLI default reorder window, which is
// what lets a path that stops sending keep datagrams it never releases.
func reassignedPathEngine() (*Engine, *allCapture) {
	ac := &allCapture{}
	return New(Config{
		Feed:                  core.FeedTOB,
		ExpectManifestCadence: 1 * time.Second,
		ExpectDefinitionCycle: 1 * time.Second,
		ReorderWindow:         8,
	}, ac), ac
}

// TestNeverReachesReadyDoesNotWaitForAPathThatStopped: a publisher address is a tunnel
// lease, and a reassigned lease leaves the old path's reorder buffer full for the life
// of the process. Nothing drains it before Flush, so a wait on it is a wait without end,
// and the rule goes quiet for the channel — the silence of #50, reached through the
// wait that guards the verdict.
//
// Here the old path stops at t=1s with three datagrams in its buffer, all sent before
// the serving period the new path opens at t=1.5s. The publisher announces two
// instruments and ships one, so the period is a Violation, and it has to be one while
// the process still runs.
func TestNeverReachesReadyDoesNotWaitForAPathThatStopped(t *testing.T) {
	e, ac := reassignedPathEngine()

	manifest := func(ts uint64) []byte { return buildManifestFrameWithTS(wire.MagicTOB, ts, 1, 1, 2, 1) }
	processDatagramFrom(e, srcA, manifest(0), core.PortRefData, 1)
	processDatagramFrom(e, srcA, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), core.PortRefData, 2)
	processDatagramFrom(e, srcA, manifest(nsPerSec), core.PortRefData, 3)
	// The lease moves. The new path keeps the cadence out to t=100s and ships only
	// instrument 100.
	seq := uint64(4)
	for ts := 3 * nsPerSec / 2; ts <= 100*nsPerSec; ts += nsPerSec / 2 {
		processDatagramFrom(e, srcB, manifest(ts), core.PortRefData, seq)
		seq++
		processDatagramFrom(e, srcB, buildInstrDefFrameWithTS(ts+nsPerSec/10, 100, 1, 1), core.PortRefData, seq)
		seq++
	}

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) != 1 {
		t.Fatalf("REFDATA.NEVER_REACHES_READY: want 1 verdict before Flush, got %d; a path that stopped sending holds the wait for the life of the process", len(found))
	}
	if found[0].Status != core.Violation {
		t.Errorf("REFDATA.NEVER_REACHES_READY: want a Violation, got %v (%s); the stopped path holds nothing from inside the serving period",
			found[0].Status, found[0].Detail)
	}
}

// TestNeverReachesReadyStoppedPathDoesNotDowngradeAnotherInstance: the old path stops at
// t=2s with datagrams from inside the new path's serving period still in its buffer. They
// belong to another channel instance, so they cannot complete the new path's set, and the
// new path's verdict neither waits for them nor reads them as loss. The new path sends
// one instrument of a set of two, so the verdict is a Violation.
//
// Keyed by Channel ID, the two paths shared one set, and these datagrams first held the
// verdict and then downgraded it to Unverifiable.
func TestNeverReachesReadyStoppedPathDoesNotDowngradeAnotherInstance(t *testing.T) {
	e, ac := reassignedPathEngine()

	manifest := func(ts uint64) []byte { return buildManifestFrameWithTS(wire.MagicTOB, ts, 1, 1, 2, 1) }
	// The old path sends to t=2s; its last eight datagrams (t=1.3s to t=2s) stay in its
	// buffer, and the ones from t=1.5s on are inside the new path's serving period.
	seq := uint64(1)
	for ts := uint64(0); ts <= 2*nsPerSec; ts += nsPerSec / 10 {
		processDatagramFrom(e, srcA, manifest(ts), core.PortRefData, seq)
		seq++
	}
	for ts := 3 * nsPerSec / 2; ts <= 100*nsPerSec; ts += nsPerSec / 2 {
		processDatagramFrom(e, srcB, manifest(ts), core.PortRefData, seq)
		seq++
		processDatagramFrom(e, srcB, buildInstrDefFrameWithTS(ts+nsPerSec/10, 100, 1, 1), core.PortRefData, seq)
		seq++
	}

	found := findingsFor(ac, "REFDATA.NEVER_REACHES_READY")
	if len(found) != 1 {
		t.Fatalf("REFDATA.NEVER_REACHES_READY: want 1 verdict before Flush, got %d", len(found))
	}
	if found[0].Status != core.Violation {
		t.Errorf("REFDATA.NEVER_REACHES_READY: want a Violation, got %v/%s (%s); another instance's buffer cannot complete this set",
			found[0].Status, found[0].Reason, found[0].Detail)
	}
}

// TestNeverReachesReadyLossOnAnotherPathDoesNotDowngrade: path A loses a datagram,
// and path B on the same Channel ID never completes its own set. A's definitions
// cannot complete B's set, so A's loss cannot explain B's verdict: it is a
// Violation, not Unverifiable.
func TestNeverReachesReadyLossOnAnotherPathDoesNotDowngrade(t *testing.T) {
	e, ac := reassignedPathEngine()

	manifest := func(ts uint64) []byte { return buildManifestFrameWithTS(wire.MagicTOB, ts, 1, 1, 2, 1) }
	// Path A: complete, but frame 2 is lost.
	processDatagramFrom(e, srcA, manifest(0), core.PortRefData, 1)
	processDatagramFrom(e, srcA, buildInstrDefFrameWithTS(nsPerSec/10, 100, 1, 1), core.PortRefData, 3)
	processDatagramFrom(e, srcA, buildInstrDefFrameWithTS(nsPerSec/5, 101, 1, 1), core.PortRefData, 4)
	e.Flush()
	if !e.dirtyOn(core.PortRefData, 1) {
		t.Fatal("setup: path A's loss did not taint its window")
	}
	// Path B: one instrument of a set of two, past the window.
	seq := uint64(1)
	for ts := uint64(0); ts <= 4*nsPerSec; ts += nsPerSec / 2 {
		processDatagramFrom(e, srcB, manifest(ts), core.PortRefData, seq)
		seq++
		processDatagramFrom(e, srcB, buildInstrDefFrameWithTS(ts+nsPerSec/10, 100, 1, 1), core.PortRefData, seq)
		seq++
	}
	e.Flush()
	e.EndRun()

	var bVerdict *core.Finding
	for i, f := range ac.findings {
		if f.RuleID == "REFDATA.NEVER_REACHES_READY" && f.Status != core.Pass {
			bVerdict = &ac.findings[i]
		}
	}
	if bVerdict == nil {
		t.Fatal("REFDATA.NEVER_REACHES_READY: no verdict for path B")
	}
	if bVerdict.Status != core.Violation {
		t.Errorf("REFDATA.NEVER_REACHES_READY: want a Violation for path B, got %v/%s (%s); path A's loss cannot complete B's set",
			bVerdict.Status, bVerdict.Reason, bVerdict.Detail)
	}
}
