package engine

import (
	"fmt"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// The market-by-price consumer: reconstructs each instrument's ladder from the
// delta stream, validates snapshot-group structure on the snapshot port, and diffs
// the two at every group.
//
// **Why this needs far less state than the market-by-order consumer.** That one
// carries `bookTrusted`, `bookCorruptedByDup` and an order-id set, because a
// snapshot there enumerates a resting-order population and a missed `OrderAdd`
// leaves the set incomplete in ways later messages cannot repair. Here a snapshot
// *replaces* the ladder outright, and every `LevelUpdate` states the complete
// resulting state of one level, so an instrument's trust has exactly two states:
// continuous since its last completed snapshot, or not. The simpler model is the
// spec's own, not a shortcut.

type mbpInstrKey struct {
	ch      uint8
	instrID uint32
}

// mbpJournalEntry is one applied delta, kept so the consumer can reconstruct its
// own state **as of any per-instrument seq** in the window.
//
// That is the whole reason a journal exists rather than a plain buffer. A
// snapshot is captured at some `K`, and by the time the group is classified the
// consumer has usually applied past it — the ports drain independently. Rewinding
// is not available: `Quantity = 0` carries no pre-image, so the spec says so
// outright. Replaying forward from the last adopted snapshot is.
type mbpJournalEntry struct {
	perSeq uint32
	// clear is set for a BookClear; the level fields are then its scope.
	clear     bool
	side      uint8
	scope     uint8
	price     int64
	qty       uint64
	fromPrice int64
}

// maxMBPJournal bounds the per-instrument journal.
//
// The spec requires a bounded buffer and a defined overflow policy, for the same
// reason it gives: worst case is one snapshot cycle of channel traffic, and an
// operator stretching the cycle for bandwidth is also stretching this. On
// overflow the instrument goes unverifiable until a baseline subsumes what was
// dropped, rather than the journal growing without limit.
//
// Sized against a real venue: a 78-instrument Phoenix capture peaked at 9,529
// entries on its busiest instrument, so a bound near it blinds the rule to
// exactly the markets worth checking.
const maxMBPJournal = 16384

type mbpInstr struct {
	book *mbpBook
	// lastSeq is the last applied Per-Instrument Seq; lastSeqSet distinguishes
	// "none yet this era" from "seq 0", which is not a legal delta value.
	lastSeq    uint32
	lastSeqSet bool
	// ready is true once a snapshot group for this instrument has completed, which
	// is what makes the reconstructed ladder comparable to the next one.
	ready bool
	// gapped is set by a per-instrument sequence gap and cleared by the next
	// completed snapshot. While set, the reconstruction is known-incomplete, so a
	// mismatch is Unverifiable rather than a publisher defect: loss is never
	// reported as non-conformance.
	gapped bool
	// sawSnapshotSinceDelta tracks whether a snapshot completed since the last
	// delta, so a delta restarting at 1 across that boundary is attributable.
	sawSnapshotSinceDelta bool
	// captureEpoch is the arrival-time capture-loss epoch of the last delta that
	// established or advanced this instrument's numbering. Behind the epoch
	// stamped on the delta in hand, it means the capture admitted a drop between
	// the two, so this instrument's gap is one the drop can account for; equal,
	// the drop has already been spent on it. See mbpDensityStatus.
	captureEpoch uint64
	// base is the last adopted snapshot's ladder, as of baseK; journal holds every
	// delta applied since, in arrival order. Together they reconstruct the
	// consumer's state at any seq in the window.
	base     map[mbpLevelKey]uint64
	baseK    uint32
	baseSet  bool
	journal  []mbpJournalEntry
	overflow bool
	// droppedMax is the highest Per-Instrument Seq the bound discarded. A dropped
	// delta never comes back, so overflow clears only once a baseline subsumes it.
	droppedMax uint32
}

// stateAsOf replays the base ladder plus every journalled delta at or below k.
func (in *mbpInstr) stateAsOf(k uint32) map[mbpLevelKey]uint64 {
	b := &mbpBook{levels: make(map[mbpLevelKey]uint64, len(in.base))}
	for key, v := range in.base {
		b.levels[key] = v
	}
	for _, e := range in.journal {
		if e.perSeq > k {
			break
		}
		if e.clear {
			b.applyBookClear(e.side, e.scope, e.fromPrice)
			continue
		}
		b.applyLevelUpdate(e.side, e.price, e.qty)
	}
	return b.levels
}

func (in *mbpInstr) record(e mbpJournalEntry) {
	if len(in.journal) >= maxMBPJournal {
		in.overflow = true
		if e.perSeq > in.droppedMax {
			in.droppedMax = e.perSeq
		}
		return
	}
	in.journal = append(in.journal, e)
}

// mbpOpenSnap is a snapshot group in flight on the snapshot port.
type mbpOpenSnap struct {
	key     mbpInstrKey
	snapID  uint32
	anchor  uint64
	total   uint32
	lastK   uint32
	depth   uint32
	levels  map[mbpLevelKey]uint64
	count   uint32
	dupKeys int
	// dirty is set by a snapshot-port sequence gap observed **during** this group,
	// which is the precise condition under which loss can account for a structural
	// anomaly in it: a dropped datagram can carry away a `SnapshotLevel` or the
	// `SnapshotEnd` itself. Gaps before the Begin do not set it — the Begin
	// re-established everything this group needs.
	dirty bool
	// lastSnapPortSeq is the snapshot-port sequence of the most recent frame seen
	// for this group. Tracked with an explicit `set` flag rather than a zero
	// sentinel because seq 0 is the first frame of an era, not "unset".
	lastSnapPortSeq    uint64
	lastSnapPortSeqSet bool
}

type mbpState struct {
	instr map[mbpInstrKey]*mbpInstr
	// open holds the group in flight, **keyed by channel**.
	//
	// The spec's non-interleaving rule scopes to one instrument stream, and a
	// channel is an independent state machine with its own snapshot cycle — the
	// frame header carries `Channel ID` precisely because a deployment may shard
	// instruments across channels and carry more than one of them on a port. A
	// single slot made two conformant channels sharing a snapshot port look like an
	// interleaving publisher, and silently discarded one of their groups.
	open map[uint8]*mbpOpenSnap
	// resetEra is the Reset Count this state was last wiped for, so the second and
	// third port to cross the same reset do not wipe again.
	resetEra    uint8
	resetEraSet bool
}

func newMBPState() *mbpState {
	return &mbpState{
		instr: make(map[mbpInstrKey]*mbpInstr),
		open:  make(map[uint8]*mbpOpenSnap),
	}
}

// get returns one instrument's tracker, creating it at the caller's current
// capture-loss epoch: a series that starts after a drop cannot show a gap the
// drop explains, which is the same reason ObserveCaptureLoss does not taint an
// instance first seen after it. Seeding a new tracker at zero instead would hand
// the excuse to every instrument that appears after any drop — including the
// whole set rebuilt behind a Reset Count.
func (s *mbpState) get(k mbpInstrKey, captureEpoch uint64) *mbpInstr {
	if in, ok := s.instr[k]; ok {
		return in
	}
	in := &mbpInstr{book: newMBPBook(), captureEpoch: captureEpoch}
	s.instr[k] = in
	return in
}

// onEraObserved records the Reset Count of an accepted frame and wipes
// per-instrument state when that era is newer than the one this state was built
// for. Per-Instrument Seq restarts at 1 and Snapshot ID is scoped to the era, so
// carrying either across a reset would judge new-era messages against discarded
// numbers.
//
// **Once per era, no matter which port sees it.** Reset Count is channel-wide but
// each of the three ports observes it independently, and the wipe used to run
// per-port: the second and third port to cross one reset destroyed state the first
// had already rebuilt, so a real per-instrument gap right after a reset was
// swallowed by the tool's own bookkeeping and any snapshot group in flight was
// silently dropped.
//
// **Called on every accepted frame, not only on a port's era advance** — which is
// where this diverges from `mboState.onResetCountForEra`. A port seeds its era from
// whatever `ResetCount` arrives first without that counting as an advance, so a
// capture whose mktdata frames all begin in era 1 while refdata still carries an
// era-0 frame gets no advance on mktdata at all, and refdata's later advance is
// then the *first* wipe — landing on state mktdata has already built. Seeding here
// closes that: the first era observed is recorded rather than applied, and only a
// genuinely newer one wipes.
//
// Returns true when it actually wiped.
func (s *mbpState) onEraObserved(era uint8) bool {
	if !s.resetEraSet {
		// First era of the run: nothing has been built against another one.
		s.resetEra, s.resetEraSet = era, true
		return false
	}
	// Only forward. An old-era straggler classified after another port advanced
	// must not wipe the new era's state back out.
	if eraRelation(s.resetEra, era) != eraNewer {
		return false
	}
	s.resetEra = era
	s.instr = make(map[mbpInstrKey]*mbpInstr)
	clear(s.open)
	return true
}

func (e *Engine) ensureMBP() {
	if e.mbp == nil {
		e.mbp = newMBPState()
	}
}

// mbpObserveEra feeds one accepted frame's Reset Count to the channel-wide reset
// bookkeeping, and taints the channel's snapshot series when a *different* port
// led the reset.
//
// That taint is what keeps the wipe's own consequences off the publisher's record:
// the group the wipe just dropped may still have frames in flight on the snapshot
// port, and without it each one is a false MBP.SNAP.GROUP_STRUCTURE orphan. The
// snapshot series clears the flag when it advances its own era. Skipped when the
// snapshot port led the reset, since it has already advanced and its new-era groups
// are clean (F4).
func (e *Engine) mbpObserveEra(port core.Port, ch uint8, era uint8) {
	if e.cfg.Feed != core.FeedMBP {
		return
	}
	e.ensureMBP()
	if !e.mbp.onEraObserved(era) || port == core.PortSnapshot {
		return
	}
	e.taintOn(core.PortSnapshot, ch)
}

// checkMBP validates the mktdata deltas in one frame.
func (e *Engine) checkMBP(f *wire.Frame, ch uint8) {
	e.ensureMBP()
	seq := f.Header.Sequence

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeLevelUpdate:
			if m.Length != 48 {
				continue // MSG.LENGTH_PER_TYPE already fired
			}
			k := mbpInstrKey{ch: ch, instrID: levelUpdateInstrumentID(m)}
			in := e.mbp.get(k, e.curCaptureEpoch)
			qty := levelUpdateQuantity(m)
			action := levelUpdateAction(m)

			// MBP.DELTA.ABSOLUTE_APPLY: `Quantity = 0` MUST pair with
			// `Action = 3` (Delete) and MUST NOT appear with any other value.
			// A publisher numbering the Action table from New instead of Unknown
			// puts every removal on the wire as a Change carrying zero — the exact
			// shape this catches, and one that no round-trip test can see.
			if qty == 0 && action != mbpActionDelete {
				e.Emit("MBP.DELTA.ABSOLUTE_APPLY", core.Violation, core.PortMktData, seq, ch, k.instrID,
					fmt.Sprintf("Quantity = 0 with Action = %d, which only Delete (3) may carry", action))
			}
			if qty != 0 && action == mbpActionDelete {
				e.Emit("MBP.DELTA.ABSOLUTE_APPLY", core.Violation, core.PortMktData, seq, ch, k.instrID,
					fmt.Sprintf("Action = Delete carrying Quantity = %d, which must be 0", qty))
			}

			perSeq := levelUpdatePerInstrSeq(m)
			if !e.checkMBPSeq(in, k, perSeq, seq, ch) {
				continue
			}
			side, price := levelUpdateSide(m), levelUpdatePrice(m)
			in.book.applyLevelUpdate(side, price, qty)
			in.record(mbpJournalEntry{perSeq: perSeq, side: side, price: price, qty: qty})

		case wire.TypeBookClear:
			if m.Length != 36 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: bookClearInstrumentID(m)}
			in := e.mbp.get(k, e.curCaptureEpoch)
			side, scope := bookClearClearSide(m), bookClearScope(m)

			// One price cannot bound both sides, so `Scope = 1` with
			// `Clear Side = Both` is malformed. Discard it rather than guess which
			// side was meant.
			if scope == mbpScopeFromPrice && side == mbpClearSideBoth {
				e.Emit("MBP.DELTA.ABSOLUTE_APPLY", core.Violation, core.PortMktData, seq, ch, k.instrID,
					"BookClear with Scope = 1 and Clear Side = Both is malformed: one price cannot bound both sides")
				// **The sequence check still runs.** `Per-Instrument Seq` is readable
				// whatever the scope/side combination says, and skipping the tracker made
				// the next well-formed delta a false `PERINSTR_DENSITY` finding on top of
				// this one — two findings for one defect, the second naming the wrong
				// problem. The mutation is discarded, because a malformed clear has no
				// defined one, and the instrument goes untrusted until its next completed
				// group so the reconstruction oracle does not report the same defect a
				// third time.
				e.checkMBPSeq(in, k, bookClearPerInstrSeq(m), seq, ch)
				in.gapped = true
				continue
			}
			perSeq := bookClearPerInstrSeq(m)
			if !e.checkMBPSeq(in, k, perSeq, seq, ch) {
				continue
			}
			from := bookClearFromPrice(m)
			in.book.applyBookClear(side, scope, from)
			in.record(mbpJournalEntry{perSeq: perSeq, clear: true, side: side, scope: scope, fromPrice: from})

		case wire.TypeInstrReset:
			if m.Length != 28 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: instrResetInstrumentID(m)}
			in := e.mbp.get(k, e.curCaptureEpoch)
			// The reset discards level state and requires a recovery snapshot
			// before deltas resume, so the reconstruction restarts from there.
			in.book = newMBPBook()
			in.ready = false
			in.gapped = false
			in.lastSeqSet = false
			in.sawSnapshotSinceDelta = false
			in.base, in.baseSet, in.journal = nil, false, nil
			in.overflow, in.droppedMax = false, 0
		}
	}
}

// checkMBPSeq validates the per-instrument sequence and updates the tracker.
//
// Both `LevelUpdate` and `BookClear` share one series, because both mutate the
// book and their relative order is significant.
//
// **Returns false when the message MUST NOT be applied.** The tracker advances
// only on a path the subscriber accepts; a duplicate or late delta advances
// nothing and mutates nothing, which is what the spec means by discarding it
// silently.
func (e *Engine) checkMBPSeq(in *mbpInstr, k mbpInstrKey, got uint32, seq uint64, ch uint8) bool {
	// Cleared on every *observed* delta, accepted or not: the flag exists only to
	// make a restart-at-1 immediately after a group attributable, and one delta has
	// now answered that question either way.
	sawSnapshot := in.sawSnapshotSinceDelta
	in.sawSnapshotSinceDelta = false

	if !in.lastSeqSet {
		in.lastSeq, in.lastSeqSet = got, true
		in.captureEpoch = e.curCaptureEpoch
		return true
	}

	// MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET: the series is NOT reset at snapshot
	// boundaries — it restarts only on a Reset Count change. Without that, a
	// subscriber that missed a snapshot but then saw seq 1 could not tell a fresh
	// post-snapshot delta from a late duplicate of an old one.
	//
	// Scoped to an actual *restart*, not to any backwards step. A delta carrying a
	// number at or below the tracker right after a group is the ordinary in-flight
	// case — the two ports are independent, so a delta captured before the snapshot
	// can be delivered after it — and the spec has the subscriber discard it as a
	// duplicate rather than complain.
	if sawSnapshot && got == 1 && in.lastSeq > 1 {
		e.Emit("MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET", core.Violation, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq restarted at 1 after a snapshot (tracker was %d); the series restarts only on a Reset Count change",
				in.lastSeq))
		// Reported and discarded: the publisher's numbering no longer corresponds to
		// the tracker, so nothing can be applied with confidence until the next
		// completed group re-establishes both.
		in.gapped = true
		return false
	}

	switch {
	case got == in.lastSeq+1:
		// Dense, as required — and a dense step is proof this instrument's chain
		// survived whatever the capture admitted losing, so the excuse below is
		// spent rather than held for a gap this drop cannot have caused.
		in.lastSeq = got
		in.captureEpoch = e.curCaptureEpoch
		return true
	case got > in.lastSeq+1:
		// A forward gap. Publishers MUST emit densely, but loss is
		// indistinguishable from a skip at this layer, so this is reported and the
		// instrument's reconstruction is marked untrustworthy until its next
		// snapshot rather than blamed for a later mismatch. The tracker advances to
		// the number actually on the wire: the following delta is dense against
		// *this* one, not against the number the gap skipped.
		st, reason := e.mbpDensityStatus(in)
		e.Emit("MBP.DELTA.PERINSTR_DENSITY", st, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq jumped %d -> %d (expected %d)", in.lastSeq, got, in.lastSeq+1),
			reason)
		in.gapped = true
		in.lastSeq = got
		return true
	default:
		// `<= lastSeq` is a duplicate or a late message, which is exactly what the
		// monotonic-within-era rule exists to make readable — and the spec has the
		// subscriber "discard silently", not report. Multicast duplicates, and a
		// snapshot adopted at `K` legitimately precedes deltas at or below `K` that
		// were already in flight.
		//
		// **Neither applied nor counted against the tracker.** Advancing it here
		// rewound the tracker on a late duplicate, which turned the very next dense
		// delta into a false `PERINSTR_DENSITY` finding and left the journal
		// non-monotonic, so `stateAsOf` replayed a stale quantity and the oracle
		// blamed the publisher for it. Dropping it outright is what the spec's
		// subscriber does, and it leaves the reconstruction exactly as-of `lastSeq` —
		// still comparable, so the instrument is not marked gapped either.
		return false
	}
}

// mbpGroupStatus is the status a `GROUP_STRUCTURE` finding carries when transport
// loss could account for it.
//
// **A conformance tool must not report ordinary transport reality as publisher
// misconduct** — that is the one failure mode that trains an operator to ignore
// the tool. One lost snapshot datagram used to yield one MUST finding per
// surviving level of the group it belonged to. `open` is nil for an orphan, where
// there is no group flag to consult and the era-wide window is all there is.
func (e *Engine) mbpGroupStatus(ch uint8, open *mbpOpenSnap) core.Status {
	if open != nil && open.dirty {
		return core.Unverifiable
	}
	if !e.gateDetectorSnap(ch) {
		return core.Unverifiable
	}
	return core.Violation
}

// mbpDensityStatus grades a per-instrument sequence gap.
//
// The gap above is graded a Violation even on a channel whose *frame* series has
// a hole, and that is deliberate: at this layer a publisher's skip and a lost
// datagram look identical, and the rule reports the gap rather than staying
// silent and letting a later reconstruction mismatch carry the blame for it.
//
// Admitted capture loss is the one case that is not a judgement call. The
// recorder says, in the file, that it failed to write datagrams in this window —
// so the missing numbers belong to the archive and not to the publisher, and
// grading them is how a replay comes to charge its own recorder's drops to the
// feed. One lost datagram breaks the per-instrument chain of every instrument it
// carried, so the misattribution amplifies: a segment admitting 663 drops earned
// 238 findings on this rule alone.
//
// **The excuse is per instrument and it is spent once.** A drop can account for
// the first gap each chain shows after it, and for nothing after that: the chain
// is proven intact again the moment the instrument takes one dense step. Gating
// on the port-wide window instead read every later gap in the segment as the
// recorder's too, which silences a MUST rule for the rest of the era on one
// admission — and the frame series going dense again is not the bound either,
// because an instrument that trades once a minute shows the break hundreds of
// frames after the drop that caused it.
//
// Consuming the epoch here rather than in the caller keeps the two halves of
// the decision — who owns this gap, and that nobody may own the next one —
// where they can only be read together.
func (e *Engine) mbpDensityStatus(in *mbpInstr) (core.Status, string) {
	if in.captureEpoch != e.curCaptureEpoch {
		in.captureEpoch = e.curCaptureEpoch
		return core.Unverifiable, core.ReasonCaptureLoss
	}
	return core.Violation, ""
}

// mbpReason renders the bounded metric reason that pairs with the status above.
func mbpReason(st core.Status) string {
	if st == core.Unverifiable {
		return core.ReasonLoss
	}
	return ""
}

// checkMBPSnapshot validates snapshot-group structure and runs the reconstruction
// diff when a group completes.
func (e *Engine) checkMBPSnapshot(f *wire.Frame, ch uint8, seq uint64) {
	e.ensureMBP()

	// An intra-group snapshot-port gap is more precise than the era-wide dirty
	// window: only loss after this group's Begin can have cost this group a message.
	if open := e.mbp.open[ch]; open != nil {
		if open.lastSnapPortSeqSet && seq > open.lastSnapPortSeq+1 {
			open.dirty = true
		}
		open.lastSnapPortSeq, open.lastSnapPortSeqSet = seq, true
	}

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeSnapshotBegin:
			if m.Length != 40 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: snapshotBeginInstrumentID(m)}
			// A Begin while another group is open **on the same channel** means the
			// publisher interleaved two instruments, which the spec forbids outright:
			// all frames carrying levels for one instrument MUST precede the first
			// frame for another. Gated, because a dropped SnapshotEnd produces exactly
			// this shape from a conformant publisher.
			if prev := e.mbp.open[ch]; prev != nil && prev.key != k {
				st := e.mbpGroupStatus(ch, prev)
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", st, core.PortSnapshot, seq, ch, k.instrID,
					fmt.Sprintf("SnapshotBegin for instrument %d while a group for %d is still open",
						k.instrID, prev.key.instrID), mbpReason(st))
			}
			e.mbp.open[ch] = &mbpOpenSnap{
				key:                k,
				snapID:             snapshotBeginSnapshotID(m),
				anchor:             snapshotBeginAnchorSeq(m),
				total:              snapshotBeginTotalOrders(m), // Total Orders reads as Total Levels
				lastK:              snapshotBeginLastInstrumentSeq(m),
				depth:              snapshotBeginDepthBound(m),
				levels:             make(map[mbpLevelKey]uint64),
				lastSnapPortSeq:    seq,
				lastSnapPortSeqSet: true,
			}

		case wire.TypeSnapshotLevel:
			if m.Length != 32 {
				continue
			}
			open := e.mbp.open[ch]
			if open == nil {
				st := e.mbpGroupStatus(ch, nil)
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", st, core.PortSnapshot, seq, ch, 0,
					"SnapshotLevel with no open SnapshotBegin", mbpReason(st))
				continue
			}
			if id := snapshotLevelSnapshotID(m); id != open.snapID {
				st := e.mbpGroupStatus(ch, open)
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", st, core.PortSnapshot, seq, ch, open.key.instrID,
					fmt.Sprintf("SnapshotLevel Snapshot ID %d does not match the open group's %d", id, open.snapID),
					mbpReason(st))
				continue
			}
			qty := snapshotLevelQuantity(m)
			// On this port an absent level is represented by absence. A zero
			// quantity here is malformed, not a removal — removals are a mktdata
			// concept.
			//
			// Ungated: loss drops whole datagrams, it does not rewrite a field, so this
			// is a publisher defect however dirty the port is.
			if qty == 0 {
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
					"SnapshotLevel carries Quantity = 0; an empty level is represented by absence")
			}
			key := mbpLevelKey{side: snapshotLevelSide(m), price: snapshotLevelPrice(m)}
			if _, dup := open.levels[key]; dup {
				open.dupKeys++
			}
			open.levels[key] = qty
			open.count++

		case wire.TypeSnapshotEnd:
			if m.Length != 20 {
				continue
			}
			e.finishMBPSnapshot(m, ch, seq)
		}
	}
}

func (e *Engine) finishMBPSnapshot(m wire.Message, ch uint8, seq uint64) {
	open := e.mbp.open[ch]
	instrID := snapshotEndInstrumentID(m)
	if open == nil {
		st := e.mbpGroupStatus(ch, nil)
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", st, core.PortSnapshot, seq, ch, instrID,
			"SnapshotEnd with no open SnapshotBegin", mbpReason(st))
		return
	}
	delete(e.mbp.open, ch)

	// A subscriber discards the group unless the End matches the Begin on all
	// three fields AND the level count matches exactly. Each is checked so the
	// report names which one failed.
	//
	// The three field mismatches and an under-count are all shapes a dropped
	// datagram produces from a conformant publisher — a lost SnapshotEnd leaves the
	// *next* group's End to be matched against this Begin — so each is gated. An
	// over-count and a repeated key are not: loss removes messages, it never adds
	// one.
	gated := e.mbpGroupStatus(ch, open)
	ok := true
	if instrID != open.key.instrID {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", gated, core.PortSnapshot, seq, ch, instrID,
			fmt.Sprintf("SnapshotEnd instrument %d does not match the group's %d", instrID, open.key.instrID),
			mbpReason(gated))
		ok = false
	}
	if a := snapshotEndAnchorSeq(m); a != open.anchor {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", gated, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("SnapshotEnd Anchor Seq %d does not match the group's %d", a, open.anchor),
			mbpReason(gated))
		ok = false
	}
	if id := snapshotEndSnapshotID(m); id != open.snapID {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", gated, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("SnapshotEnd Snapshot ID %d does not match the group's %d", id, open.snapID),
			mbpReason(gated))
		ok = false
	}
	if open.count != open.total {
		st := core.Violation
		if open.count < open.total {
			st = gated
		}
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", st, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("group declared Total Levels = %d but carried %d", open.total, open.count),
			mbpReason(st))
		ok = false
	}
	if open.dupKeys > 0 {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("group repeated %d (Side, Price) key(s); a level is identified by that tuple", open.dupKeys))
		ok = false
	}
	if !ok {
		return
	}
	// The group is well-formed: reported, because a rule that only ever speaks up to
	// complain cannot tell an operator how many groups it vetted. One finding per
	// completed group; a group that fails contributes one per field that failed, so
	// the `pass` series is the count of clean groups and not the complement of the
	// violation count.
	e.passed("MBP.SNAP.GROUP_STRUCTURE", core.PortSnapshot, seq, ch, open.key.instrID,
		fmt.Sprintf("group (Snapshot ID %d) well-formed: %d level(s) between Begin and End", open.snapID, open.count))

	in := e.mbp.get(open.key, e.curCaptureEpoch)
	db := open.depth
	in.book.depthBound = &db

	// MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT — the check the rest of this
	// exists to enable.
	//
	// **Compared as of the snapshot's own `Last Instrument Seq`, replayed from the
	// journal.** An earlier revision required `K == lastSeq` and skipped otherwise,
	// so it only compared when the consumer happened to sit exactly at the capture
	// point: 102 of 344 groups on a real capture, and structurally never for the
	// busiest instruments, because the ports drain independently and the deltas
	// after a capture are routinely classified before the group that precedes them.
	// Replaying forward from the last adopted ladder makes a group comparable
	// regardless of that ordering, which is what the spec's subscriber does with
	// its delta buffer.
	//
	// `Last Instrument Seq` is the discriminator, never `Anchor Seq`: the anchor is
	// a channel-wide mktdata number that moves on every other instrument's deltas
	// and on every Heartbeat.
	//
	// **Every branch reports.** A completed group is one opportunity for this rule, and
	// five of the seven ways it can end are ones the publisher had no part in — so each
	// is reported as unverifiable with the cause named, never as silence. The silence
	// was the defect: 5 of 38 groups compared here and a clean run looked exactly like
	// a run that compared nothing. See engine/denominator.go.
	const oracle = "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"
	switch {
	case in.gapped:
		// Loss. Never reported as non-conformance.
		e.unverified(oracle, core.ReasonLoss, core.PortSnapshot, seq, ch, open.key.instrID,
			"instrument's delta history has a gap since its last completed group")
	case in.overflow:
		// A journal we bounded, not a stream we mistrust — a distinct cause, because
		// stretching the snapshot cycle is what produces it and that is an operator
		// dial, not publisher behaviour.
		e.unverified(oracle, core.ReasonOverflow, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("delta journal exceeded its %d-entry bound before this group", maxMBPJournal))
	case !in.baseSet:
		// No adopted ladder to replay from yet; this group becomes it.
		e.unverified(oracle, core.ReasonColdStart, core.PortSnapshot, seq, ch, open.key.instrID,
			"first completed group for this instrument: adopted as the baseline, nothing to compare against")
	case open.lastK < in.baseK:
		// Captured before our own baseline: nothing to replay it against.
		e.unverified(oracle, core.ReasonReorder, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("group's Last Instrument Seq %d precedes the adopted baseline's %d", open.lastK, in.baseK))
	case !in.lastSeqSet || open.lastK > in.lastSeq:
		// **We have not applied through K yet.** Replaying "as of K" with a journal
		// that stops short of it silently yields the state as of wherever the
		// journal ends, and the diff then blames the publisher for deltas we simply
		// have not classified — the group and the deltas travel on separate ports.
		// Caught exactly this way: a group at K=579 diffed against a ladder missing
		// the delete that delta 579 carried.
		e.unverified(oracle, core.ReasonPending, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("group's Last Instrument Seq %d not yet applied from the delta port (at %d)",
				open.lastK, in.lastSeq))
	default:
		if d := diffMBPLevels(in.stateAsOf(open.lastK), open.levels); len(d) > 0 {
			e.Emit(oracle, core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
				fmt.Sprintf("at Last Instrument Seq %d the delta-reconstructed ladder differs from the snapshot in %d level(s): %s",
					open.lastK, len(d), describeMBPDiff(d)))
		} else {
			e.passed(oracle, core.PortSnapshot, seq, ch, open.key.instrID,
				fmt.Sprintf("delta-reconstructed ladder matches the snapshot at Last Instrument Seq %d (%d level(s))",
					open.lastK, len(open.levels)))
		}
	}

	// Adopt this group as the new baseline and drop the journal it subsumes.
	in.base = open.levels
	in.baseK = open.lastK
	in.baseSet = true
	kept := in.journal[:0]
	for _, ent := range in.journal {
		if ent.perSeq > open.lastK {
			kept = append(kept, ent)
		}
	}
	in.journal = kept
	// Clearing this unconditionally replayed a journal with a hole in it and
	// reported the deltas we discarded as publisher drift (malbeclabs/phoenix#181).
	in.overflow = in.droppedMax > open.lastK
	if !in.overflow {
		in.droppedMax = 0
	}
	// The live ladder is the new baseline plus whatever the journal still holds.
	in.book.levels = in.stateAsOf(^uint32(0))
	in.ready = true
	in.gapped = false
	in.sawSnapshotSinceDelta = true
	if open.lastK > in.lastSeq {
		in.lastSeq = open.lastK
		in.lastSeqSet = true
	}
}

// flushOpenMBPSnaps reports the snapshot groups still in flight at end-of-run.
//
// **Unverifiable, never a Violation — deliberately unlike MBO's
// `flushOpenSnaps`.** A capture that ends mid-group is the ordinary case: the
// observation window closed, which is not something the publisher did, and a
// truncated group is the shape every finite capture ends in. Silence is wrong too
// — a group counted as neither conformant nor not is exactly what an operator
// needs told — so it is reported as unverifiable and the reason names the cause.
func (e *Engine) flushOpenMBPSnaps() {
	if e.mbp == nil {
		return
	}
	for ch, open := range e.mbp.open {
		if open == nil {
			continue
		}
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Unverifiable, core.PortSnapshot, 0, ch, open.key.instrID,
			fmt.Sprintf("SnapshotBegin (Snapshot ID %d) never followed by a SnapshotEnd: %d of %d level(s) seen at end of stream",
				open.snapID, open.count, open.total), core.ReasonTruncated)
	}
	clear(e.mbp.open)
}

// describeMBPDiff renders up to three differing levels, so a report is actionable
// without dumping a whole ladder.
func describeMBPDiff(d []mbpLevelDiff) string {
	const max = 3
	out := ""
	for i, x := range d {
		if i == max {
			out += fmt.Sprintf(" (+%d more)", len(d)-max)
			break
		}
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("side=%d price=%d delta-book=%d snapshot=%d",
			x.key.side, x.key.price, x.fromBook, x.fromSnap)
	}
	return out
}
