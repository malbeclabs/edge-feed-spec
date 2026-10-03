package engine

import (
	"net/netip"
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// The publisher a finding names, which is the whole point of carrying the source
// address: a validator subscribes to a GROUP and receives every path merged, so
// without it a verdict can only be reported against the feed as a whole.
//
// These tests pin both directions. Naming the wrong publisher is worse than
// naming none — it charges one path for its peer's fault — so the gate that
// keeps channel-scoped rules anonymous is tested as hard as the attribution
// itself.

// TestInstanceScopedFindingNamesTheFaultyPublisher is the case the column
// exists for. Two publishers serve one channel on one group; A goes silent for
// 3.05 s against a 1 s heartbeat expectation while B publishes right through it.
//
// HEARTBEAT.CADENCE is StateCounters, so the finding is about one path's series
// and must carry A's address and never B's. B did nothing wrong, and a verdict
// that cannot tell them apart is reported against the feed — which is exactly
// the state this replaces.
func TestInstanceScopedFindingNamesTheFaultyPublisher(t *testing.T) {
	frames := twoStreams(hbFrame{src: srcA, ch: chArmA}, hbFrame{src: srcB, ch: chArmA}, 0, 5000)
	_, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	var violations int
	for _, f := range cadenceOn(cap, chArmA) {
		if f.Status != core.Violation {
			continue
		}
		violations++
		if f.SourceAddr != srcA {
			t.Errorf("HEARTBEAT.CADENCE violation carries source_addr %v, want %v: the silent publisher is A, and attributing its gap to B charges a path for its peer's fault",
				f.SourceAddr, srcA)
		}
	}
	if violations == 0 {
		t.Fatal("no HEARTBEAT.CADENCE violation was recorded, so the attribution is untested")
	}
}

// TestEveryFindingIsAttributedExactlyAsTheCatalogSays pins the gate as an
// invariant over a whole replay rather than rule by rule: a finding carries an
// address if and only if its rule's state kind is instance-scoped.
//
// Written this way because the failure it guards against is a rule DRIFTING
// across the line — moved between state kinds, or emitted from a new path — and
// a test naming one rule cannot see that happen to another.
func TestEveryFindingIsAttributedExactlyAsTheCatalogSays(t *testing.T) {
	frames := twoStreams(hbFrame{src: srcA, ch: chArmA}, hbFrame{src: srcB, ch: chArmB}, 0, 5000)
	_, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	if len(cap.findings) == 0 {
		t.Fatal("the replay produced no findings, so the invariant is untested")
	}
	for _, f := range cap.findings {
		meta, ok := core.Lookup(f.RuleID)
		if !ok {
			t.Errorf("finding for unknown rule %q", f.RuleID)
			continue
		}
		want := meta.State.InstanceScoped()
		if got := f.SourceAddr.IsValid(); got != want {
			t.Errorf("%s (state kind %v): carries an address = %v, want %v",
				f.RuleID, meta.State, got, want)
		}
		if !f.SourceAddr.IsValid() {
			continue
		}
		// And it must be the RIGHT one. The two publishers own a channel each in this
		// replay, so the channel the finding names says which address it should carry —
		// checking only that the address is one of the two would pass with them swapped,
		// which is the failure this whole change is about.
		wantAddr := map[uint8]netip.Addr{chArmA: srcA, chArmB: srcB}[f.ChannelID]
		if !wantAddr.IsValid() {
			t.Errorf("%s: finding on unexpected channel %d", f.RuleID, f.ChannelID)
			continue
		}
		if f.SourceAddr != wantAddr {
			t.Errorf("%s on channel %d: source_addr %v, want %v — the finding is charged to the wrong publisher",
				f.RuleID, f.ChannelID, f.SourceAddr, wantAddr)
		}
	}
}

// TestChannelScopedRuleStaysAnonymousMidDatagram is the subtle half, and the
// reason attribution is decided by the catalog rather than by where emit was
// called from.
//
// A channel-scoped rule can fire from inside per-datagram classification —
// REFDATA.NEVER_REACHES_READY does exactly that — at a point where the engine
// does know whose datagram it is holding. Carrying the address there would name
// the path that happened to deliver the settling datagram for a verdict decided
// over a set every path contributed to.
func TestChannelScopedRuleStaysAnonymousMidDatagram(t *testing.T) {
	for _, ruleID := range []string{
		"REFDATA.NEVER_REACHES_READY", // StateRefdata, and fires mid-run by design
		"MBP.SNAP.GROUP_STRUCTURE",    // StateSnapshotGroup, over a book both paths fill
	} {
		meta, ok := core.Lookup(ruleID)
		if !ok {
			t.Fatalf("rule %q is not in the catalog", ruleID)
		}
		if meta.State.InstanceScoped() {
			t.Fatalf("%s is instance-scoped in the catalog; this test asserts the opposite", ruleID)
		}
	}

	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedMBP, ReorderWindow: 1}, cap)

	// Put a datagram through so the engine is holding a publisher address, then
	// emit a channel-scoped rule as the mid-run paths do.
	// Flush drains the reorder buffer, which is what actually classifies the
	// datagram and so what sets the address the gate has to refuse to use.
	eng.Process(srcA, makeHB(wire.MagicMBP, chArmA, 1, 0, 10*nsPerSec), core.PortMktData, nil)
	eng.Flush()
	if !eng.curSrc.IsValid() {
		t.Fatal("the engine is not holding a source address, so the gate is untested")
	}
	eng.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, 0, chArmA, 0, "")

	found := false
	for _, f := range cap.findingsFor("MBP.SNAP.GROUP_STRUCTURE") {
		found = true
		if f.SourceAddr.IsValid() {
			t.Errorf("MBP.SNAP.GROUP_STRUCTURE carries source_addr %v, want none: the book it judges is filled by every path of the channel",
				f.SourceAddr)
		}
	}
	if !found {
		t.Fatal("no MBP.SNAP.GROUP_STRUCTURE finding was recorded")
	}
}

// TestEndRunLeavesNoStaleAddress pins the clear in EndRun. Its checks run over
// merged state with no datagram in front of them, and every rule they emit is
// channel-scoped today — so the clear buys nothing until a rule moves into one
// of those paths, at which point it is the difference between no address and the
// last one classification happened to leave behind.
func TestEndRunLeavesNoStaleAddress(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, cap)
	eng.Process(srcA, makeHB(wire.MagicTOB, chArmA, 1, 0, 10*nsPerSec), core.PortMktData, nil)
	eng.Flush()
	if !eng.curSrc.IsValid() {
		t.Fatal("the engine is not holding a source address after Flush, so the clear is untested")
	}

	eng.EndRun()

	if eng.curSrc.IsValid() {
		t.Errorf("EndRun left source address %v behind; its checks have no datagram to attribute to", eng.curSrc)
	}
	// And a finding emitted after it carries nothing, whatever its scope.
	eng.Emit("HEARTBEAT.CADENCE", core.Violation, core.PortMktData, 0, chArmA, 0, "")
	for _, f := range cap.findingsFor("HEARTBEAT.CADENCE") {
		if f.SourceAddr.IsValid() {
			t.Errorf("a finding emitted after EndRun carries source_addr %v, want none", f.SourceAddr)
		}
	}
}

// A guard on the zero value itself: netip.Addr{} must not render as an address.
// The prom reporter keys the empty label on exactly this, and a zero Addr that
// stringified to something like "invalid IP" would become a metric label.
func TestZeroAddrIsNotAnAddress(t *testing.T) {
	var zero netip.Addr
	if zero.IsValid() {
		t.Fatal("the zero netip.Addr reports itself valid")
	}
}

// TestResetStartViolationNamesTheResettingPublisher is a regression test for a
// misattribution the catalog gate alone does NOT prevent, and the reason Process
// sets the address on entry rather than leaving it to classify.
//
// FRAME.MKTDATA_SEQ_START is StateCounters, so it carries an address — but it is
// emitted from Process, BEFORE the datagram that triggered it is classified. An
// era advance normally classifies the old-era buffer first, which happens to set
// the right address; when that buffer is already empty it classifies nothing, and
// the rule read whatever address classification last saw. On a group two
// publishers share, that is the other one.
//
// Observed: A restarts its sequence badly and the violation was recorded against
// B, which had done nothing but publish last.
func TestResetStartViolationNamesTheResettingPublisher(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, cap)

	// Two publishers on one channel. Flush drains both buffers in (channel,
	// source) order, so B is the last address classification sees — and A's
	// buffer is left empty, which is what removes the accidental correction.
	eng.Process(srcA, makeHB(wire.MagicMBO, chArmA, 50, 0, 10*nsPerSec), core.PortMktData, nil)
	eng.Process(srcB, makeHB(wire.MagicMBO, chArmA, 1, 0, 11*nsPerSec), core.PortMktData, nil)
	eng.Flush()

	// Now A advances its Reset Count and restarts at a non-zero sequence, which is
	// the violation. Its buffer is empty, so nothing is classified before the emit.
	eng.Process(srcA, makeHB(wire.MagicMBO, chArmA, 77, 1, 12*nsPerSec), core.PortMktData, nil)

	found := cap.findingsFor("FRAME.MKTDATA_SEQ_START")
	if len(found) == 0 {
		t.Fatal("no FRAME.MKTDATA_SEQ_START finding, so the attribution is untested")
	}
	for _, f := range found {
		if f.SourceAddr != srcA {
			t.Errorf("FRAME.MKTDATA_SEQ_START carries source_addr %v, want %v: A reset badly and B is being charged for it",
				f.SourceAddr, srcA)
		}
	}
}
