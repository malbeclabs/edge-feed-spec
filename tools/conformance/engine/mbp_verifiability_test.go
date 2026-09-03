package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// The tests in this file all exist for one property: **loss is never reported as
// non-conformance.** Each was a live false positive — a lost datagram, a reset
// crossed by a second port, two channels sharing a snapshot port, a late
// duplicate — that the tool reported as a publisher MUST violation. A conformance
// tool that cries wolf at ordinary transport reality is worse than no tool,
// because it teaches an operator to ignore the one signal it exists to give.

// mbpFrame builds one frame with explicit channel and reset count, which the
// `mbpTape` helper does not vary.
func mbpFrame(port core.Port, seq uint64, ch, reset uint8, typ, length uint8, flags uint16, body func(*wb.Body)) mbpStep {
	raw := wb.Frame(wire.MagicMBP).Seq(seq).Channel(ch).ResetCount(reset).
		MsgFlags(typ, length, flags, body).Bytes()
	return mbpStep{port, raw}
}

// runMBPSteps feeds a step list through one engine and returns it with every
// finding. Flushes at each port change for the reason `mbpTape.violated`
// documents, and calls EndRun so end-of-observation checks run as `run.go` has
// them run.
func runMBPSteps(t *testing.T, steps []mbpStep) (*Engine, []core.Finding) {
	t.Helper()
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBP, SourceRegistry: stubRegistry{}}, ac)
	var last core.Port
	first := true
	for _, s := range steps {
		if !first && s.port != last {
			e.Flush()
		}
		first = false
		f, sf := wire.Decode(s.raw, wire.MagicMBP)
		e.Process(srcA, f, s.port, sf)
		last = s.port
	}
	e.Flush()
	e.EndRun()
	return e, ac.findings
}

// countByStatus tallies one rule's findings by status.
func countByStatus(t *testing.T, fs []core.Finding, rule string) (viol, unver int) {
	t.Helper()
	for _, f := range fs {
		if f.RuleID != rule {
			continue
		}
		switch f.Status {
		case core.Violation:
			viol++
			t.Logf("  Violation: %s", f.Detail)
		case core.Unverifiable:
			unver++
			t.Logf("  Unverifiable (%s): %s", f.Reason, f.Detail)
		}
	}
	return viol, unver
}

// countByReason tallies one rule's unverifiable findings carrying one reason.
func countByReason(fs []core.Finding, rule, reason string) int {
	n := 0
	for _, f := range fs {
		if f.RuleID == rule && f.Status == core.Unverifiable && f.Reason == reason {
			n++
		}
	}
	return n
}

// dropFirst removes the first step whose frame leads with the given message type,
// which is what one lost datagram looks like: the message is gone AND the
// snapshot-port sequence has a hole.
func dropFirst(t *testing.T, steps []mbpStep, typ uint8) []mbpStep {
	t.Helper()
	out := make([]mbpStep, 0, len(steps))
	done := false
	for _, s := range steps {
		f, _ := wire.Decode(s.raw, wire.MagicMBP)
		if !done && len(f.Messages) > 0 && f.Messages[0].Type == typ {
			done = true
			continue
		}
		out = append(out, s)
	}
	if !done {
		t.Fatalf("no frame leading with type 0x%02X to drop", typ)
	}
	return out
}

// dropLast is dropFirst's mirror, for losing a datagram partway through a stream
// rather than at its head: a port's very first frame has nothing to gap from, so
// dropping it is undetectable here exactly as it is on the wire.
func dropLast(t *testing.T, steps []mbpStep, typ uint8) []mbpStep {
	t.Helper()
	idx := -1
	for i, s := range steps {
		f, _ := wire.Decode(s.raw, wire.MagicMBP)
		if len(f.Messages) > 0 && f.Messages[0].Type == typ {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no frame leading with type 0x%02X to drop", typ)
	}
	out := make([]mbpStep, 0, len(steps)-1)
	out = append(out, steps[:idx]...)
	return append(out, steps[idx+1:]...)
}

// ladder builds an n-level bid side, big enough that a per-level finding storm is
// unmistakable.
func ladder(n int) [][3]int64 {
	out := make([][3]int64, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, bid(int64(1000-i), 5))
	}
	return out
}

// --- one lost snapshot datagram ---

// A dropped `SnapshotBegin` orphans every surviving level of its group. Before the
// gate that was one MUST Violation per level: ~746 findings from one lost packet
// against a fully conformant publisher, on a group the size the real fixture
// carries.
func TestMBPLostSnapshotBeginIsUnverifiable(t *testing.T) {
	// A first, complete group so the snapshot port has a sequence to gap *from*:
	// losing a port's very first datagram is undetectable, here and on the wire.
	steps := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 1000, 5).
		group(1, 10, 7, 1, 0, ladder(2)...).
		group(1, 11, 8, 1, 0, ladder(20)...).steps

	_, fs := runMBPSteps(t, dropLast(t, steps, wire.TypeSnapshotBegin))
	viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE")
	if viol != 0 {
		t.Errorf("a lost SnapshotBegin produced %d Violation(s); loss is not publisher misconduct", viol)
	}
	if unver == 0 {
		t.Error("a lost SnapshotBegin must still be reported, as Unverifiable")
	}
}

// A dropped `SnapshotLevel` leaves the group short of its declared `Total Levels`.
// Under-count is loss-shaped; over-count is not.
func TestMBPLostSnapshotLevelIsUnverifiable(t *testing.T) {
	steps := newTape().group(1, 10, 7, 1, 0, ladder(6)...).steps

	_, fs := runMBPSteps(t, dropLast(t, steps, wire.TypeSnapshotLevel))
	viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE")
	if viol != 0 || unver != 1 {
		t.Errorf("lost SnapshotLevel: got %d Violation / %d Unverifiable, want 0 / 1", viol, unver)
	}
}

// The other half of the gate: what loss cannot manufacture stays a Violation even
// on a port that has already lost frames. Loss removes messages; it never adds one
// and never rewrites a field.
func TestMBPOverCountAndBadFieldsStayViolations(t *testing.T) {
	// dirtyTape builds a first group, then a second one whose body the caller
	// supplies, and loses a level from the *first* so the port is provably dirty by
	// the time the second arrives. The first group's own under-count is the gated
	// finding; the second group's defect is what these cases assert on.
	dirtyTape := func(second func(*mbpTape)) []mbpStep {
		tp := newTape().group(1, 10, 7, 1, 0, ladder(3)...)
		second(tp)
		return dropFirst(t, tp.steps, wire.TypeSnapshotLevel)
	}

	// Declared 1, carried 2. Loss removes messages; it never adds one.
	over := dirtyTape(func(tp *mbpTape) {
		tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 20, 1, 8, 0, 0))
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 2000, 5, mbpClearSideBid))
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 1999, 5, mbpClearSideBid))
		tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 20, 8))
	})
	_, fs := runMBPSteps(t, over)
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 1 {
		t.Errorf("over-count on a dirty port: got %d Violation(s), want 1", viol)
	}

	// A zero quantity is a rewritten field, which loss cannot do either.
	zero := dirtyTape(func(tp *mbpTape) {
		tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 20, 1, 8, 0, 0))
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 2000, 0, mbpClearSideBid))
		tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 20, 8))
	})
	_, zfs := runMBPSteps(t, zero)
	if viol, _ := countByStatus(t, zfs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 1 {
		t.Errorf("SnapshotLevel Quantity = 0 on a dirty port: got %d Violation(s), want 1", viol)
	}

	// A repeated (Side, Price) key likewise: the tuple identifies a level, and a
	// group that states two quantities for one level is malformed however lossy the
	// port was.
	dup := dirtyTape(func(tp *mbpTape) {
		tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 20, 2, 8, 0, 0))
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 2000, 5, mbpClearSideBid))
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 2000, 7, mbpClearSideBid))
		tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 20, 8))
	})
	_, dfs := runMBPSteps(t, dup)
	if viol, _ := countByStatus(t, dfs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 1 {
		t.Errorf("repeated (Side, Price) key on a dirty port: got %d Violation(s), want 1", viol)
	}
}

// --- the reset era, observed three times ---

// Reset Count is channel-wide but each port observes it independently. The wipe
// has to be idempotent per era, or the second port to cross one reset destroys
// state the first has already rebuilt — and a real publisher gap right after a
// reset is swallowed by the tool's own bookkeeping.
func TestMBPResetWipeIsIdempotentPerEra(t *testing.T) {
	lu := func(perSeq uint32, qty uint64) func(*wb.Body) {
		return mbpLevelUpdate(1, mbpClearSideBid, mbpActionNew, perSeq, 1000, qty)
	}
	hb := func(b *wb.Body) { b.U32(0).U64(0).Pad(4) }

	steps := []mbpStep{
		// refdata sees era 0 first, so its era-1 frame below is a genuine advance.
		mbpFrame(core.PortRefData, 0, 0, 0, wire.TypeHeartbeat, 16, 0, hb),
		// mktdata crosses into era 1 first and establishes the tracker at 2.
		mbpFrame(core.PortMktData, 0, 0, 1, wire.TypeLevelUpdate, 48, 0, lu(1, 5)),
		mbpFrame(core.PortMktData, 1, 0, 1, wire.TypeLevelUpdate, 48, 0, lu(2, 6)),
		// refdata now crosses the SAME reset. This must not wipe again.
		mbpFrame(core.PortRefData, 1, 0, 1, wire.TypeHeartbeat, 16, 0, hb),
		// A real 2 -> 9 publisher gap.
		mbpFrame(core.PortMktData, 2, 0, 1, wire.TypeLevelUpdate, 48, 0, lu(9, 7)),
	}
	_, fs := runMBPSteps(t, steps)
	if viol, _ := countByStatus(t, fs, "MBP.DELTA.PERINSTR_DENSITY"); viol != 1 {
		t.Errorf("a real gap after a cross-port reset: got %d violation(s), want 1", viol)
	}
}

// A group in flight when a *different* port crosses the reset is dropped by the
// wipe, so its remaining frames are orphans. That is the tool's own bookkeeping,
// not the publisher's doing.
func TestMBPGroupSurvivesAnotherPortsReset(t *testing.T) {
	hb := func(b *wb.Body) { b.U32(0).U64(0).Pad(4) }
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 1, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 1, 7, 0, 0)),
		mbpFrame(core.PortRefData, 0, 0, 0, wire.TypeHeartbeat, 16, 0, hb),
		mbpFrame(core.PortRefData, 1, 0, 1, wire.TypeHeartbeat, 16, 0, hb),
		mbpFrame(core.PortSnapshot, 1, 0, 1, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 2, 0, 1, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7)),
	}
	_, fs := runMBPSteps(t, steps)
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 0 {
		t.Errorf("a conformant group spanning another port's reset produced %d Violation(s)", viol)
	}
}

// --- channels sharing one snapshot port ---

// The non-interleaving rule is about one instrument stream. A channel is an
// independent state machine with its own snapshot cycle, and the frame header
// carries `Channel ID` precisely because a deployment may carry more than one on a
// port — so two conformant channels interleaving groups there are conformant.
func TestMBPTwoChannelsShareOneSnapshotPort(t *testing.T) {
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 1, 7, 0, 0)),
		mbpFrame(core.PortSnapshot, 1, 1, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 20, 1, 8, 0, 0)),
		mbpFrame(core.PortSnapshot, 2, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 3, 1, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 2000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 4, 0, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7)),
		mbpFrame(core.PortSnapshot, 5, 1, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(2, 20, 8)),
	}
	e, fs := runMBPSteps(t, steps)
	if viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 0 || unver != 0 {
		t.Errorf("two conformant channels on one snapshot port: got %d Violation / %d Unverifiable, want 0 / 0",
			viol, unver)
	}
	// And neither group was discarded: both instruments hold an adopted baseline.
	for _, k := range []mbpInstrKey{{ch: 0, instrID: 1}, {ch: 1, instrID: 2}} {
		if in, ok := e.mbp.instr[k]; !ok || !in.baseSet {
			t.Errorf("channel %d instrument %d: group was dropped rather than adopted", k.ch, k.instrID)
		}
	}
}

// Interleaving two instruments **on one channel** is what the spec forbids, and it
// still fires.
func TestMBPInterleavedGroupsOnOneChannelStillFire(t *testing.T) {
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 1, 7, 0, 0)),
		mbpFrame(core.PortSnapshot, 1, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 20, 1, 8, 0, 0)),
	}
	_, fs := runMBPSteps(t, steps)
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol == 0 {
		t.Error("a Begin while the same channel's group is open must be reported")
	}
}

// A capture that ends mid-group is the ordinary case, not publisher misconduct —
// but it is not nothing either, and silence let the real fixture's 39th group
// disappear unremarked.
func TestMBPUnclosedGroupAtEndOfRun(t *testing.T) {
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 2, 7, 0, 0)),
		mbpFrame(core.PortSnapshot, 1, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
	}
	_, fs := runMBPSteps(t, steps)
	viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE")
	if viol != 0 || unver != 1 {
		t.Errorf("truncated group at end of run: got %d Violation / %d Unverifiable, want 0 / 1", viol, unver)
	}
}

// --- the per-instrument tracker ---

// A late duplicate must not rewind the tracker. It used to: the next legitimate
// delta then reported a gap that never happened, and the journal went
// non-monotonic so the replay applied a stale quantity and the oracle blamed the
// publisher for it.
func TestMBPLateDuplicateDoesNotRewindTracker(t *testing.T) {
	tp := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 1000, 5).
		delta(2, mbpClearSideBid, mbpActionChange, 1000, 6).
		delta(3, mbpClearSideBid, mbpActionChange, 1000, 7).
		delta(1, mbpClearSideBid, mbpActionNew, 1000, 5). // late duplicate of seq 1
		delta(4, mbpClearSideBid, mbpActionChange, 1000, 8)

	e, fs := runMBPSteps(t, tp.steps)
	if viol, _ := countByStatus(t, fs, "MBP.DELTA.PERINSTR_DENSITY"); viol != 0 {
		t.Errorf("a dense series interrupted by a duplicate produced %d density violation(s)", viol)
	}

	in := e.mbp.get(mbpInstrKey{ch: 0, instrID: 1})
	if in.lastSeq != 4 {
		t.Errorf("tracker is at %d, want 4 (the duplicate must not move it)", in.lastSeq)
	}
	var js []uint32
	for _, ent := range in.journal {
		js = append(js, ent.perSeq)
	}
	for i := 1; i < len(js); i++ {
		if js[i] <= js[i-1] {
			t.Fatalf("journal is not monotonic: %v — stateAsOf would replay a stale quantity", js)
		}
	}
	// The discarded duplicate carried quantity 5; the accepted series ends at 8.
	if got := in.book.levels[mbpLevelKey{side: mbpClearSideBid, price: 1000}]; got != 8 {
		t.Errorf("ladder holds quantity %d, want 8 (the duplicate must not be applied)", got)
	}
	// And the instrument stays comparable: a discarded duplicate costs nothing.
	if in.gapped {
		t.Error("a duplicate the subscriber discards must not mark the instrument gapped")
	}
}

// A malformed `BookClear` is one defect and must produce one finding. Its
// `Per-Instrument Seq` is readable whatever its scope/side combination says, so
// skipping the tracker made the next well-formed delta a second, wrongly-named
// finding.
func TestMBPMalformedBookClearAdvancesTracker(t *testing.T) {
	tp := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 1000, 5).
		clear(2, mbpClearSideBoth, mbpScopeFromPrice, 999). // malformed
		delta(3, mbpClearSideBid, mbpActionChange, 1000, 6)

	_, fs := runMBPSteps(t, tp.steps)
	if viol, _ := countByStatus(t, fs, "MBP.DELTA.ABSOLUTE_APPLY"); viol != 1 {
		t.Errorf("malformed BookClear: got %d ABSOLUTE_APPLY violation(s), want 1", viol)
	}
	if viol, _ := countByStatus(t, fs, "MBP.DELTA.PERINSTR_DENSITY"); viol != 0 {
		t.Errorf("malformed BookClear produced %d spurious density violation(s)", viol)
	}
}

// --- registry consistency ---

// A finding whose rule is not registered for the feed it fired against has no
// `rule_info` row, and the Grafana join drops it silently. `InstrumentReset` is
// byte-identical across the two feeds, both specs state the anchor requirement,
// and tier1's case is not feed-gated — so the registry has to agree.
func TestMBPResetAnchorRuleIsRegisteredForMBP(t *testing.T) {
	ir := func(b *wb.Body) {
		b.U32(1).U16(1).Pad(2)
		b.U64(999) // New Anchor Seq, against frame seq 0
		b.U64(0)
	}
	_, fs := runMBPSteps(t, []mbpStep{
		mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeInstrReset, 28, 0, ir),
	})

	for _, f := range fs {
		if f.RuleID != "RESET.ANCHOR_SEQ_IS_CURRENT_FRAME" {
			continue
		}
		meta, ok := core.Lookup(f.RuleID)
		if !ok {
			t.Fatal("rule is not in the registry")
		}
		for _, fd := range meta.Feeds {
			if fd == f.Feed {
				return
			}
		}
		t.Fatalf("emitted with feed=%s but the registry lists %v", f.Feed, meta.Feeds)
	}
	t.Fatal("the rule never fired under feed=mbp")
}

// --- the two ends of a capture, which pull in opposite directions ---

// A running publisher's snapshot port is always mid-group, so a capture's first
// levels belong to a group whose `SnapshotBegin` predates the recorder. Both
// preserved Phoenix captures open this way; that was their whole count (#65).
func TestMBPCaptureHeadOrphansAreUnverifiable(t *testing.T) {
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 1, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1001, 5, mbpClearSideAsk)),
		mbpFrame(core.PortSnapshot, 2, 0, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7)),
		// The run's first Begin. Everything from here on is judged normally.
		mbpFrame(core.PortSnapshot, 3, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 20, 1, 8, 0, 0)),
		mbpFrame(core.PortSnapshot, 4, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(8, 1000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 5, 0, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 20, 8)),
	}
	_, fs := runMBPSteps(t, steps)
	viol, unver := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE")
	if viol != 0 || unver != 3 {
		t.Errorf("head partial group: got %d Violation / %d Unverifiable, want 0 / 3", viol, unver)
	}
	// Visible in `unverifiable_by_reason`, never absent: a silently shrinking
	// denominator is this forgiveness's own failure mode.
	if got := countByReason(fs, "MBP.SNAP.GROUP_STRUCTURE", core.ReasonColdStart); got != 3 {
		t.Errorf("head orphans carried %d cold_start reason(s), want 3", got)
	}
}

// The other end, and why the head rule stops at the first Begin. An
// `InstrumentReset` discards that instrument's open snapshot group (spec,
// *Instrument Reset* step 1), so the group's tail belongs to nothing and is the
// publisher's defect — malbeclabs/phoenix#163, which scored clean before this.
// **A blanket forgive of orphans fails here**, while looking like success on both
// preserved captures.
func TestMBPPostResetGroupTailStaysAViolation(t *testing.T) {
	reset := func(instrID uint32) func(*wb.Body) {
		return func(b *wb.Body) {
			b.U32(instrID).U16(1).Pad(2)
			b.U64(0) // New Anchor Seq, matching the frame's own sequence
			b.U64(0)
		}
	}
	tail := func(resetInstr uint32) []mbpStep {
		return []mbpStep{
			mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 10, 3, 7, 0, 0)),
			mbpFrame(core.PortSnapshot, 1, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
			mbpFrame(core.PortMktData, 0, 0, 0, wire.TypeInstrReset, 28, 0, reset(resetInstr)),
			mbpFrame(core.PortSnapshot, 2, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1001, 5, mbpClearSideBid)),
			mbpFrame(core.PortSnapshot, 3, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1002, 5, mbpClearSideBid)),
			mbpFrame(core.PortSnapshot, 4, 0, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 10, 7)),
		}
	}

	_, fs := runMBPSteps(t, tail(1))
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.GROUP_STRUCTURE"); viol != 3 {
		t.Errorf("post-reset group tail: got %d Violation(s), want 3 (two levels and the End)", viol)
	}

	// A reset for a *different* instrument discards nothing: step 1 is scoped to `I`.
	_, other := runMBPSteps(t, tail(2))
	if viol, unver := countByStatus(t, other, "MBP.SNAP.GROUP_STRUCTURE"); viol != 0 || unver != 0 {
		t.Errorf("another instrument's reset: got %d Violation / %d Unverifiable, want 0 / 0", viol, unver)
	}
}

// An orphan level names no instrument. Reporting 0 read as "instrument 0" and
// misattributed a real capture's burst to SOL during phoenix#163's diagnosis (#53).
// An orphan `SnapshotEnd` beside it *does* carry the field, so the flag has to
// discriminate rather than blanket every orphan as unknown.
func TestMBPOrphanLevelNamesNoInstrument(t *testing.T) {
	steps := []mbpStep{
		mbpFrame(core.PortSnapshot, 0, 0, 0, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(7, 1000, 5, mbpClearSideBid)),
		mbpFrame(core.PortSnapshot, 1, 0, 0, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(42, 10, 7)),
	}
	_, fs := runMBPSteps(t, steps)
	var without, with int
	for _, f := range fs {
		if f.RuleID != "MBP.SNAP.GROUP_STRUCTURE" {
			continue
		}
		if f.NoInstrumentID {
			without++
			if f.InstrumentID != 0 {
				t.Errorf("a finding carrying no instrument id still reports %d", f.InstrumentID)
			}
			continue
		}
		with++
		if f.InstrumentID != 42 {
			t.Errorf("orphan SnapshotEnd reported instrument %d, want 42 from its own field", f.InstrumentID)
		}
	}
	if without != 1 || with != 1 {
		t.Fatalf("got %d finding(s) with no id and %d with one, want 1 and 1", without, with)
	}
}
