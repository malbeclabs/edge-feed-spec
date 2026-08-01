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
}

type mbpState struct {
	// oracleRuns counts completed reconstruction comparisons, so a clean run can be
	// told apart from one where every instrument was skipped as unverifiable.
	oracleRuns int
	instr      map[mbpInstrKey]*mbpInstr
	// open is the single group in flight. The spec forbids interleaving two groups
	// for different instruments, so one slot is the correct shape — a map would
	// model something the wire does not permit.
	open *mbpOpenSnap
}

func newMBPState() *mbpState {
	return &mbpState{instr: make(map[mbpInstrKey]*mbpInstr)}
}

func (s *mbpState) get(k mbpInstrKey) *mbpInstr {
	if in, ok := s.instr[k]; ok {
		return in
	}
	in := &mbpInstr{book: newMBPBook()}
	s.instr[k] = in
	return in
}

// onResetCount wipes per-instrument state: Per-Instrument Seq restarts at 1 and
// Snapshot ID is scoped to the era, so carrying either across a reset would judge
// new-era messages against discarded numbers.
func (s *mbpState) onResetCount() {
	s.instr = make(map[mbpInstrKey]*mbpInstr)
	s.open = nil
}

func (e *Engine) ensureMBP() {
	if e.mbp == nil {
		e.mbp = newMBPState()
	}
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
			in := e.mbp.get(k)
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

			e.checkMBPSeq(in, k, levelUpdatePerInstrSeq(m), seq, ch)
			in.book.applyLevelUpdate(levelUpdateSide(m), levelUpdatePrice(m), qty)

		case wire.TypeBookClear:
			if m.Length != 36 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: bookClearInstrumentID(m)}
			in := e.mbp.get(k)
			side, scope := bookClearClearSide(m), bookClearScope(m)

			// One price cannot bound both sides, so `Scope = 1` with
			// `Clear Side = Both` is malformed. Discard it rather than guess which
			// side was meant.
			if scope == mbpScopeFromPrice && side == mbpClearSideBoth {
				e.Emit("MBP.DELTA.ABSOLUTE_APPLY", core.Violation, core.PortMktData, seq, ch, k.instrID,
					"BookClear with Scope = 1 and Clear Side = Both is malformed: one price cannot bound both sides")
				continue
			}
			e.checkMBPSeq(in, k, bookClearPerInstrSeq(m), seq, ch)
			in.book.applyBookClear(side, scope, bookClearFromPrice(m))

		case wire.TypeInstrReset:
			if m.Length != 28 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: instrResetInstrumentID(m)}
			in := e.mbp.get(k)
			// The reset discards level state and requires a recovery snapshot
			// before deltas resume, so the reconstruction restarts from there.
			in.book = newMBPBook()
			in.ready = false
			in.gapped = false
			in.lastSeqSet = false
		}
	}
}

// checkMBPSeq validates the per-instrument sequence and updates the tracker.
//
// Both `LevelUpdate` and `BookClear` share one series, because both mutate the
// book and their relative order is significant.
func (e *Engine) checkMBPSeq(in *mbpInstr, k mbpInstrKey, got uint32, seq uint64, ch uint8) {
	defer func() {
		in.lastSeq = got
		in.lastSeqSet = true
		in.sawSnapshotSinceDelta = false
	}()

	if !in.lastSeqSet {
		return
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
	if in.sawSnapshotSinceDelta && got == 1 && in.lastSeq > 1 {
		e.Emit("MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET", core.Violation, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq restarted at 1 after a snapshot (tracker was %d); the series restarts only on a Reset Count change",
				in.lastSeq))
		return
	}

	switch {
	case got == in.lastSeq+1:
		// Dense, as required.
	case got > in.lastSeq+1:
		// A forward gap. Publishers MUST emit densely, but loss is
		// indistinguishable from a skip at this layer, so this is reported and the
		// instrument's reconstruction is marked untrustworthy until its next
		// snapshot rather than blamed for a later mismatch.
		e.Emit("MBP.DELTA.PERINSTR_DENSITY", core.Violation, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq jumped %d -> %d (expected %d)", in.lastSeq, got, in.lastSeq+1))
		in.gapped = true
	default:
		// `<= lastSeq` is a duplicate or a late message, which is exactly what the
		// monotonic-within-era rule exists to make readable — and the spec has the
		// subscriber "discard silently", not report. Multicast duplicates, and a
		// snapshot adopted at `K` legitimately precedes deltas at or below `K` that
		// were already in flight.
		//
		// It is still not applied, and it still costs the reconstruction: a
		// duplicate mutates the ladder without advancing the tracker, so the book is
		// no longer as-of any point the publisher will snapshot at.
		in.gapped = true
	}
}

// checkMBPSnapshot validates snapshot-group structure and runs the reconstruction
// diff when a group completes.
func (e *Engine) checkMBPSnapshot(f *wire.Frame, ch uint8, seq uint64) {
	e.ensureMBP()

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeSnapshotBegin:
			if m.Length != 40 {
				continue
			}
			k := mbpInstrKey{ch: ch, instrID: snapshotBeginInstrumentID(m)}
			// A Begin while another group is open means the publisher interleaved
			// two instruments, which the spec forbids outright: all frames carrying
			// levels for one instrument MUST precede the first frame for another.
			if e.mbp.open != nil && e.mbp.open.key != k {
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, k.instrID,
					fmt.Sprintf("SnapshotBegin for instrument %d while a group for %d is still open",
						k.instrID, e.mbp.open.key.instrID))
			}
			e.mbp.open = &mbpOpenSnap{
				key:    k,
				snapID: snapshotBeginSnapshotID(m),
				anchor: snapshotBeginAnchorSeq(m),
				total:  snapshotBeginTotalOrders(m), // Total Orders reads as Total Levels
				lastK:  snapshotBeginLastInstrumentSeq(m),
				depth:  snapshotBeginDepthBound(m),
				levels: make(map[mbpLevelKey]uint64),
			}

		case wire.TypeSnapshotLevel:
			if m.Length != 32 {
				continue
			}
			open := e.mbp.open
			if open == nil {
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, 0,
					"SnapshotLevel with no open SnapshotBegin")
				continue
			}
			if id := snapshotLevelSnapshotID(m); id != open.snapID {
				e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
					fmt.Sprintf("SnapshotLevel Snapshot ID %d does not match the open group's %d", id, open.snapID))
				continue
			}
			qty := snapshotLevelQuantity(m)
			// On this port an absent level is represented by absence. A zero
			// quantity here is malformed, not a removal — removals are a mktdata
			// concept.
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
	open := e.mbp.open
	instrID := snapshotEndInstrumentID(m)
	if open == nil {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, instrID,
			"SnapshotEnd with no open SnapshotBegin")
		return
	}
	e.mbp.open = nil

	// A subscriber discards the group unless the End matches the Begin on all
	// three fields AND the level count matches exactly. Each is checked so the
	// report names which one failed.
	ok := true
	if instrID != open.key.instrID {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, instrID,
			fmt.Sprintf("SnapshotEnd instrument %d does not match the group's %d", instrID, open.key.instrID))
		ok = false
	}
	if a := snapshotEndAnchorSeq(m); a != open.anchor {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("SnapshotEnd Anchor Seq %d does not match the group's %d", a, open.anchor))
		ok = false
	}
	if id := snapshotEndSnapshotID(m); id != open.snapID {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("SnapshotEnd Snapshot ID %d does not match the group's %d", id, open.snapID))
		ok = false
	}
	if open.count != open.total {
		e.Emit("MBP.SNAP.GROUP_STRUCTURE", core.Violation, core.PortSnapshot, seq, ch, open.key.instrID,
			fmt.Sprintf("group declared Total Levels = %d but carried %d", open.total, open.count))
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

	in := e.mbp.get(open.key)
	db := open.depth
	in.book.depthBound = &db

	// MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT — the check the rest of this
	// exists to enable. Compared only when the reconstruction is trustworthy and
	// the two states are as-of the same point: `Last Instrument Seq` is the
	// discriminator, exactly as *Snapshot while ready* prescribes, because
	// `Anchor Seq` is a channel-wide number that moves on every other instrument's
	// deltas and on every Heartbeat.
	switch {
	case in.gapped || !in.lastSeqSet:
		// Loss, or nothing applied yet. Never reported as non-conformance.
	case !in.ready:
		// First completed group for this instrument: adopt it as the baseline
		// rather than diffing against a ladder built from a partial delta history.
	case open.lastK != in.lastSeq:
		// The publisher captured at a different point than we have applied. Routine
		// — deltas flow between capture and delivery — and not comparable.
	default:
		e.mbp.oracleRuns++
		if d := diffMBPLevels(in.book.clone(), open.levels); len(d) > 0 {
			e.Emit("MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.Violation, core.PortSnapshot, seq, ch,
				open.key.instrID,
				fmt.Sprintf("at Last Instrument Seq %d the delta-reconstructed ladder differs from the snapshot in %d level(s): %s",
					open.lastK, len(d), describeMBPDiff(d)))
		}
	}

	// **A group captured behind our tracker is not a usable baseline.**
	//
	// The spec's subscriber buffers deltas and, on `SnapshotEnd`, replays those
	// above the anchor onto the adopted ladder. This consumer has no such buffer —
	// it applies deltas as they arrive — so when a capture starts mid-stream it can
	// hold deltas past `K` with no baseline they were ever applied to. Adopting the
	// group then produces a ladder that is as-of `K` while the tracker says
	// otherwise, and the next delta reads as a forward gap: that is what produced a
	// "jumped 404 -> 447" against a capture whose wire sequence was provably dense,
	// and the level mismatch that followed it.
	//
	// Waiting costs one rotation and is honest. The next group is captured at or
	// after where we are, and from there ladder and tracker agree.
	if in.lastSeqSet && open.lastK < in.lastSeq {
		in.gapped = true
		in.sawSnapshotSinceDelta = true
		return
	}

	// **Adopt only when genuinely behind, and never rewind the tracker.**
	// *Snapshot while ready* makes `Last Instrument Seq` the discriminator: with
	// `K <= last_applied` the subscriber is current or has advanced past the
	// capture, which is the ordinary case — deltas routinely arrive between the
	// publisher's capture and the snapshot's delivery — and the group MAY simply be
	// ignored.
	//
	// Rewinding here is a bug that looks like a publisher defect. Ports drain
	// independently, so the deltas after a capture are frequently classified before
	// the group that precedes them; setting `lastSeq = K` then moves the tracker
	// backwards, and the very next delta reads as a forward gap. That produced a
	// "Per-Instrument Seq jumped 404 -> 447" against a capture whose wire sequence
	// was provably dense.
	if !in.ready || open.lastK > in.lastSeq {
		in.book.levels = open.levels
		if open.lastK > 0 {
			in.lastSeq = open.lastK
			in.lastSeqSet = true
		}
	}
	in.ready = true
	in.gapped = false
	in.sawSnapshotSinceDelta = true
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
