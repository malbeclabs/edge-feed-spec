package engine

// capture_loss_test.go — what an archive admits it never recorded.
//
// A pcapng segment carries the recorder's own loss accounting per packet
// (`epb_dropcount`), and these tests exist for one property: **a gap the capture
// admits it caused is never reported as publisher non-conformance.** That is the
// one error keeping the bytes was meant to prevent, and it does not stay
// proportional — one lost datagram breaks the per-instrument sequence chain of
// every instrument it carried, so a segment admitting 663 drops earned 316 MUST
// findings before the option reached the rule set.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// mbpDelta is one LevelUpdate on instrument 1 at the given per-instrument seq.
func mbpDelta(perSeq uint32, qty uint64) func(*wb.Body) {
	return mbpLevelUpdate(1, mbpClearSideBid, mbpActionNew, perSeq, 1000, qty)
}

// mbpFeeder returns an engine and a function that pushes one step through it.
func mbpFeeder(t *testing.T) (*Engine, *allCapture, func(mbpStep)) {
	t.Helper()
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBP, SourceRegistry: stubRegistry{}}, ac)
	return e, ac, func(s mbpStep) {
		f, sf := wire.Decode(s.raw, wire.MagicMBP)
		e.Process(srcA, f, s.port, sf)
	}
}

// findingsWithStatus returns one rule's findings at one status.
func findingsWithStatus(fs []core.Finding, rule string, st core.Status) []core.Finding {
	var out []core.Finding
	for _, f := range fs {
		if f.RuleID == rule && f.Status == st {
			out = append(out, f)
		}
	}
	return out
}

// --- MBP.DELTA.PERINSTR_DENSITY, the rule the loss amplifies through ---

// The per-instrument density rule reports a gap even on a channel whose frame
// series has a hole, deliberately: at that layer a publisher's skip and a lost
// datagram look identical, and the rule speaks up rather than let a later
// reconstruction mismatch carry the blame. Admitted capture loss is the one case
// that is not a judgement call — the file says the recorder failed to write
// datagrams in this window — so the missing numbers belong to the archive.
func TestAdmittedCaptureLossIsNotAPublisherGap(t *testing.T) {
	// The control: the same gap with nothing admitting it. This is the behaviour
	// the change must leave alone — a publisher that skips a number is still
	// reported, whatever the transport did.
	t.Run("unadmitted gap stays a violation", func(t *testing.T) {
		e, ac, feed := mbpFeeder(t)
		feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
		feed(mbpFrame(core.PortMktData, 1, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(2, 6)))
		// Zero is a no-op, not an admission: a recorder writes epb_dropcount = 0 on
		// every clean packet, and treating that as loss would taint every window.
		e.ObserveCaptureLoss(0)
		feed(mbpFrame(core.PortMktData, 3, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(4, 7)))
		e.Flush()

		if got := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation); len(got) != 1 {
			t.Errorf("got %d violation(s) for an unadmitted gap, want 1", len(got))
		}
	})

	// The same stream, with the recorder admitting it lost the datagram that
	// carried the missing number.
	t.Run("admitted gap is unverifiable", func(t *testing.T) {
		e, ac, feed := mbpFeeder(t)
		feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
		feed(mbpFrame(core.PortMktData, 1, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(2, 6)))
		e.ObserveCaptureLoss(1) // the recorder never wrote frame seq 2
		feed(mbpFrame(core.PortMktData, 3, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(4, 7)))
		e.Flush()

		if got := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation); len(got) != 0 {
			t.Errorf("a gap the capture admits it caused was graded a Violation: %s", got[0].Detail)
		}
		unver := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Unverifiable)
		if len(unver) != 1 {
			t.Fatalf("got %d unverifiable finding(s), want 1: the opportunity still has to be "+
				"accounted for, or the rule reports the same nothing a clean feed does", len(unver))
		}
		if unver[0].Reason != core.ReasonCaptureLoss {
			t.Errorf("reason %q, want %q: `loss` sends an operator to the network and "+
				"`capture_loss` sends them to the recorder", unver[0].Reason, core.ReasonCaptureLoss)
		}
	})
}

// The taint lives on the era's observation window, exactly as a transport gap's
// does, so it must not outlive it: a Reset Count change starts a clean window,
// and a real publisher gap in the new era is the publisher's.
func TestCaptureLossTaintDoesNotOutliveItsEra(t *testing.T) {
	e, ac, feed := mbpFeeder(t)
	// Era 0, then the recorder admits a drop in it.
	feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	e.ObserveCaptureLoss(3)
	// Era 1: a clean window, and a genuine 2 -> 9 gap in it.
	feed(mbpFrame(core.PortMktData, 0, 0, 1, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	feed(mbpFrame(core.PortMktData, 1, 0, 1, wire.TypeLevelUpdate, 48, 0, mbpDelta(2, 6)))
	feed(mbpFrame(core.PortMktData, 2, 0, 1, wire.TypeLevelUpdate, 48, 0, mbpDelta(9, 7)))
	e.Flush()

	if got := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation); len(got) != 1 {
		t.Errorf("got %d violation(s) for a real gap in a post-reset window, want 1: a stale "+
			"capture taint suppresses violations the new era shows plainly", len(got))
	}
}

// --- the lifetime of the excuse ---

// A drop can account for the first gap each series shows after it, and for
// nothing beyond that.
//
// The taint used to have no window at all: nothing cleared it short of an era
// advance, so one admitted drop silenced this MUST rule for the rest of the
// segment. A publisher that skips a number 500 frames later did so on a series
// the capture demonstrably kept whole in between, and reporting that as
// `unverifiable`/`capture_loss` both hides the defect and sends an operator to
// the recorder for it.
func TestOneAdmittedDropDoesNotSilenceTheRestOfTheSegment(t *testing.T) {
	e, ac, feed := mbpFeeder(t)
	feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	e.ObserveCaptureLoss(1)
	// The frames after it are contiguous and so are the per-instrument numbers:
	// whatever the recorder lost, this chain is whole across it.
	for i := uint32(2); i <= 40; i++ {
		feed(mbpFrame(core.PortMktData, uint64(i-1), 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(i, 5)))
	}
	// Then a real skip, on a gapless frame series.
	feed(mbpFrame(core.PortMktData, 40, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(60, 6)))
	e.Flush()

	got := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation)
	if len(got) != 1 {
		t.Errorf("got %d violation(s) for a skip 39 dense deltas after the drop, want 1: a "+
			"stale capture taint silences the rule for the rest of the segment", len(got))
	}
	for _, f := range findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Unverifiable) {
		t.Errorf("a gap the drop cannot explain was graded unverifiable: %s", f.Detail)
	}
}

// The other half of the same lifetime: the *reason* a gated rule reports.
//
// `loss` sends an operator to the network and `capture_loss` sends them to the
// recorder, and the substitution in Emit applied for as long as the taint stood
// — so a network gap hundreds of frames past the drop reported the recorder.
func TestTheCaptureLossLabelDoesNotOutliveTheDropItNames(t *testing.T) {
	const ch, instrID = uint8(1), uint32(42)
	e, ac := newMBOEngine()
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	e.ObserveCaptureLoss(1)
	// The drop's own frame, and the gap it accounts for.
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 2)
	// A frame gap with nothing admitting it, well past the drop.
	processSeq(e, buildOrderAddFrame(ch, instrID, 4), wire.MagicMBO, 20)
	processSeq(e, buildOrderAddFrame(ch, instrID, 9), wire.MagicMBO, 21)
	e.Flush()

	unver := findingsWithStatus(ac.findings, "DELTA.PERINSTR_DENSITY", core.Unverifiable)
	if len(unver) != 2 {
		t.Fatalf("got %d unverifiable finding(s), want 2 (one per gap): %v", len(unver), unver)
	}
	if unver[0].Reason != core.ReasonCaptureLoss {
		t.Errorf("the gap right after the admitted drop reported %q, want %q",
			unver[0].Reason, core.ReasonCaptureLoss)
	}
	if unver[1].Reason != core.ReasonLoss {
		t.Errorf("a later gap the recorder never admitted reported %q, want %q: pointing an "+
			"operator at the recorder for loss the wire caused", unver[1].Reason, core.ReasonLoss)
	}
}

// The excuse is per instrument, and the frame series going dense again is not
// what spends it.
//
// An instrument that updates once every few hundred frames shows its broken
// chain hundreds of frames after the drop that broke it — the frame series has
// long since been contiguous by then. Bounding the taint on frame contiguity
// alone would charge exactly those instruments for the recorder's drop, which is
// the misattribution this rule's gate exists to prevent; the bound is the
// instrument's own next step.
func TestTheExcuseWaitsForTheInstrumentThatWasBroken(t *testing.T) {
	const other = uint32(2)
	e, ac, feed := mbpFeeder(t)
	// Instrument 1 and instrument 2 are both live before the drop.
	feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	feed(mbpFrame(core.PortMktData, 1, 0, 0, wire.TypeLevelUpdate, 48, 0,
		mbpLevelUpdate(other, mbpClearSideBid, mbpActionNew, 1, 1000, 5)))
	e.ObserveCaptureLoss(1)
	// Instrument 1 carries the busy chain and goes dense again immediately, which
	// is what makes the frame series contiguous long before instrument 2 speaks.
	for i := uint32(2); i <= 30; i++ {
		feed(mbpFrame(core.PortMktData, uint64(i), 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(i, 5)))
	}
	// Instrument 2's first delta since the drop, with the number the lost
	// datagram was carrying missing from its chain.
	feed(mbpFrame(core.PortMktData, 31, 0, 0, wire.TypeLevelUpdate, 48, 0,
		mbpLevelUpdate(other, mbpClearSideBid, mbpActionNew, 3, 1000, 6)))
	e.Flush()

	for _, f := range findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation) {
		t.Errorf("the drop's own gap was charged to the publisher because a different "+
			"instrument's chain had gone dense: %s", f.Detail)
	}
	unver := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Unverifiable)
	if len(unver) != 1 {
		t.Fatalf("got %d unverifiable finding(s), want 1", len(unver))
	}
	if unver[0].InstrumentID != other || unver[0].Reason != core.ReasonCaptureLoss {
		t.Errorf("finding is instrument %d reason %q, want %d and %q",
			unver[0].InstrumentID, unver[0].Reason, other, core.ReasonCaptureLoss)
	}

	// And it is spent: a second gap on the same instrument, with nothing new
	// admitted, is the publisher's.
	feed(mbpFrame(core.PortMktData, 32, 0, 0, wire.TypeLevelUpdate, 48, 0,
		mbpLevelUpdate(other, mbpClearSideBid, mbpActionNew, 9, 1000, 7)))
	e.Flush()
	if got := findingsWithStatus(ac.findings, "MBP.DELTA.PERINSTR_DENSITY", core.Violation); len(got) != 1 {
		t.Errorf("got %d violation(s) for the instrument's second gap, want 1: one drop "+
			"cannot account for every gap a chain ever shows", len(got))
	}
}

// --- the reason, and where the taint reaches ---

// Every gated rule reports `loss` for "a datagram that could have carried what I
// needed is missing". On a replay that datagram may be one the recorder admits it
// never wrote, and the two want different investigations, so the label has to say
// which. MBO's density rule already downgrades on a frame gap, so this pins the
// naming rather than the status.
func TestCaptureLossNamesTheOwnerOfTheLoss(t *testing.T) {
	const ch, instrID = uint8(1), uint32(42)

	// A frame gap with nothing admitting it: transport loss, reported as `loss`.
	e, ac := newMBOEngine()
	processSeq(e, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	processSeq(e, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 3)
	e.Flush()
	unver := findingsWithStatus(ac.findings, "DELTA.PERINSTR_DENSITY", core.Unverifiable)
	if len(unver) == 0 {
		t.Fatal("a per-instrument jump over a frame gap must be Unverifiable")
	}
	if unver[0].Reason != core.ReasonLoss {
		t.Errorf("unadmitted transport loss reported reason %q, want %q", unver[0].Reason, core.ReasonLoss)
	}

	// The same jump on a *gapless* channel — normally a proven publisher skip —
	// with the recorder admitting the loss.
	e2, ac2 := newMBOEngine()
	processSeq(e2, buildOrderAddFrame(ch, instrID, 1), wire.MagicMBO, 1)
	e2.ObserveCaptureLoss(2)
	processSeq(e2, buildOrderAddFrame(ch, instrID, 3), wire.MagicMBO, 2)
	e2.Flush()
	if got := findingsWithStatus(ac2.findings, "DELTA.PERINSTR_DENSITY", core.Violation); len(got) != 0 {
		t.Errorf("a jump inside an admitted capture drop was graded a Violation: %s", got[0].Detail)
	}
	unver2 := findingsWithStatus(ac2.findings, "DELTA.PERINSTR_DENSITY", core.Unverifiable)
	if len(unver2) == 0 {
		t.Fatal("expected an Unverifiable finding for a jump inside an admitted capture drop")
	}
	if unver2[0].Reason != core.ReasonCaptureLoss {
		t.Errorf("admitted capture loss reported reason %q, want %q", unver2[0].Reason, core.ReasonCaptureLoss)
	}
}

// --- loss admitted after the last datagram ---

// An end-of-run finding is graded against the window the loss falls in, and at
// EOF no later frame can arrive to taint anything.
//
// Two things used to leave the last window ungraded. The run loop read the
// capture's total only after the end-of-run findings were produced, so drops
// admitted after the last mapped datagram reached the report and tainted
// nothing; and a snapshot group in flight reads its own flag, which only an
// intra-group gap on the snapshot port sets — and a SnapshotEnd the recorder
// admits it never wrote leaves no gap to see. The group simply never closed, and
// the publisher was reported for a message that reached the archive's own
// dropped-frame counter instead.
func TestAdmittedLossTaintsASnapshotGroupStillOpenAtEndOfRun(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(200), uint32(1)

	// The control: the same unfinished group with nothing admitting a loss is the
	// publisher's, and has to stay that way.
	t.Run("unadmitted", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 1), 1),
			snapEntry(buildSnapOrderFull(ch, snapID, 1001, 100), 2),
		})
		e.Flush()
		e.EndRun()
		if !hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
			t.Error("a group left open with nothing lost is a grouping violation")
		}
	})

	// The same stream, with the capture admitting a drop after the last datagram
	// it yielded — which is what the run loop now hands over before the
	// end-of-run findings are produced.
	t.Run("admitted after the last datagram", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 1), 1),
			snapEntry(buildSnapOrderFull(ch, snapID, 1001, 100), 2),
		})
		e.ObserveCaptureLoss(1)
		e.Flush()
		e.EndRun()

		if hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
			t.Error("a group the capture admits it lost the end of was graded a Violation: the " +
				"publisher is being reported for a datagram the archive says it never wrote")
		}
		if len(findingsWithStatus(ac.findings, "SNAP.BEGIN_ORDER_END_GROUPING", core.Unverifiable)) != 1 {
			t.Errorf("want one unverifiable grouping finding — the opportunity still has to be "+
				"accounted for — got %v", ac.findings)
		}
	})
}

// The recorder drops at its interface, so what it lost was never parsed: its
// channel, its source address and even its destination port are unknowable.
// Marking only the series whose datagram carried the admission would leave every
// other one reading a capture-inflicted gap as the publisher's — so the taint
// covers every instance on every port, which is the same direction taintPortWide
// takes for a datagram too short to name its channel.
func TestCaptureLossTaintsEveryPortAndChannel(t *testing.T) {
	e, _, feed := mbpFeeder(t)
	// One instance per port, on two different channels.
	feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	feed(mbpFrame(core.PortMktData, 0, 1, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	feed(mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 1, 7, 0, 0)))
	e.Flush()

	e.ObserveCaptureLoss(1)

	for _, tc := range []struct {
		port core.Port
		ch   uint8
	}{
		{core.PortMktData, 0},
		{core.PortMktData, 1},
		{core.PortSnapshot, 0},
	} {
		if !e.captureDirtyOn(tc.port, tc.ch) {
			t.Errorf("%v channel %d: window not marked as having admitted capture loss", tc.port, tc.ch)
		}
		if !e.dirtyOn(tc.port, tc.ch) {
			t.Errorf("%v channel %d: verifiability window not tainted, so a gated rule would "+
				"still grade a Violation", tc.port, tc.ch)
		}
	}
}

// Tier-1 structural rules are decidable from a single intact datagram, so
// admitted capture loss must not excuse one: a malformed frame that *was*
// recorded is evidence about the publisher whatever else went missing. Replaying
// the committed market-by-price capture with drops injected leaves its 619
// `FRAME.LENGTH_CONSISTENCY` and 6 `MSG.SNAPSHOT_FLAG_MATCHES_PORT` violations
// exactly where they were; this pins the property in one frame.
func TestCaptureLossDoesNotExcuseAStructuralViolation(t *testing.T) {
	e, ac, feed := mbpFeeder(t)
	feed(mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeLevelUpdate, 48, 0, mbpDelta(1, 5)))
	e.ObserveCaptureLoss(5)
	// A LevelUpdate declaring a length its type does not have.
	feed(mbpFrame(core.PortMktData, 1, 0, 0, wire.TypeLevelUpdate, 40, 0, mbpDelta(2, 6)))
	e.Flush()

	var structural int
	for _, f := range ac.findings {
		if f.Status == core.Violation && (f.RuleID == "MSG.LENGTH_PER_TYPE" || f.RuleID == "FRAME.LENGTH_CONSISTENCY") {
			structural++
		}
	}
	if structural == 0 {
		t.Errorf("a malformed frame inside an admitted capture drop produced no structural "+
			"violation; findings were %v", ruleIDs(ac.findings))
	}
}

// ruleIDs lists the distinct rules a finding set touched, for a failure message.
func ruleIDs(fs []core.Finding) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range fs {
		if _, ok := seen[f.RuleID]; ok {
			continue
		}
		seen[f.RuleID] = struct{}{}
		out = append(out, f.RuleID)
	}
	return out
}
