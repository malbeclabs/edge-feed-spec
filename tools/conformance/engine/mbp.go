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
	instr map[mbpInstrKey]*mbpInstr
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
	if in.sawSnapshotSinceDelta && got <= in.lastSeq {
		e.Emit("MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET", core.Violation, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq went %d -> %d across a snapshot boundary; the series must not reset",
				in.lastSeq, got))
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
		// `<= lastSeq` is a repeat or a backwards step, and unlike a forward gap it
		// is unambiguously the publisher's: loss can only ever *remove* messages, so
		// it cannot manufacture a number the series already used. Frame-level
		// duplication is caught separately by FRAME.SEQ_DUP_DIVERGENT, so a fresh
		// frame carrying a stale per-instrument number is a publisher defect.
		//
		// It also poisons the reconstruction, because applying it mutates the ladder
		// without advancing the tracker — the ladder is then neither as-of `lastSeq`
		// nor as-of anything the publisher will snapshot at.
		e.Emit("MBP.DELTA.PERINSTR_DENSITY", core.Violation, core.PortMktData, seq, ch, k.instrID,
			fmt.Sprintf("Per-Instrument Seq went backwards %d -> %d; the series is monotonic within an era",
				in.lastSeq, got))
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
		if d := diffMBPLevels(in.book.clone(), open.levels); len(d) > 0 {
			e.Emit("MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.Violation, core.PortSnapshot, seq, ch,
				open.key.instrID,
				fmt.Sprintf("at Last Instrument Seq %d the delta-reconstructed ladder differs from the snapshot in %d level(s): %s",
					open.lastK, len(d), describeMBPDiff(d)))
		}
	}

	// Adopt the snapshot as the authoritative ladder either way: it is the
	// publisher's own state, and it is what a subscriber would hold.
	in.book.levels = open.levels
	in.ready = true
	in.gapped = false
	in.sawSnapshotSinceDelta = true
	if open.lastK > 0 {
		in.lastSeq = open.lastK
		in.lastSeqSet = true
	}
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
