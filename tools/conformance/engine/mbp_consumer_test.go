package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- message builders, offsets straight from the spec ---

func mbpLevelUpdate(instrID uint32, side, action uint8, perSeq uint32, price int64, qty uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID).U16(1).U8(side).U8(action) // 0-7
		b.U32(perSeq)                             // 8
		b.I64(price).U64(qty)                     // 12, 20
		b.U64(0)                                  // 28 Timestamp
		b.U16(0xFFFF).U16(0xFFFF)                 // 36 Order Count, 38 Level Index
		b.U8(0).U8(0).Pad(2)                      // 40 Reason, 41 Level Flags, 42 Reserved
	}
}

func mbpBookClear(instrID uint32, clearSide, scope uint8, perSeq uint32, fromPrice int64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID).U16(1).U8(clearSide).U8(scope) // 0-7
		b.U32(perSeq)                                 // 8
		b.I64(fromPrice)                              // 12
		b.U64(0)                                      // 20 Timestamp
		b.U8(0).Pad(3)                                // 28 Clear Reason, 29 Reserved
	}
}

func mbpSnapBegin(instrID uint32, anchor uint64, total, snapID, lastK, depth uint32) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID).U64(anchor) // 0, 4
		b.U32(total).U32(snapID)   // 12, 16
		b.U32(lastK)               // 20
		b.U64(0)                   // 24 Timestamp
		b.U32(depth)               // 32
	}
}

func mbpSnapLevel(snapID uint32, price int64, qty uint64, side uint8) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(snapID).I64(price).U64(qty) // 0, 4, 12
		b.U16(0xFFFF).U8(side).U8(0)      // 20, 22, 23
		b.Pad(4)                          // 24 Reserved
	}
}

func mbpSnapEnd(instrID uint32, anchor uint64, snapID uint32) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID).U64(anchor).U32(snapID)
	}
}

// mbpTape builds a multi-frame capture with **monotonic per-port sequences**.
//
// Every rule here is stateful, so a single frame cannot express a sequence gap or
// a snapshot/delta divergence. And the sequence has to advance per port: two
// frames sharing one sequence number are a duplicate, which the engine correctly
// quarantines — an easy way to write a test that passes for the wrong reason.
type mbpTape struct {
	steps []mbpStep
	seq   map[core.Port]uint64
}

func newTape() *mbpTape { return &mbpTape{seq: map[core.Port]uint64{}} }

func (tp *mbpTape) add(port core.Port, typ uint8, length uint8, flags uint16, body func(*wb.Body)) *mbpTape {
	raw := wb.Frame(wire.MagicMBP).Seq(tp.seq[port]).MsgFlags(typ, length, flags, body).Bytes()
	tp.seq[port]++
	tp.steps = append(tp.steps, mbpStep{port, raw})
	return tp
}

// delta appends one LevelUpdate on mktdata.
func (tp *mbpTape) delta(perSeq uint32, side, action uint8, price int64, qty uint64) *mbpTape {
	return tp.add(core.PortMktData, wire.TypeLevelUpdate, 48, 0,
		mbpLevelUpdate(1, side, action, perSeq, price, qty))
}

// clear appends one BookClear on mktdata.
func (tp *mbpTape) clear(perSeq uint32, clearSide, scope uint8, fromPrice int64) *mbpTape {
	return tp.add(core.PortMktData, wire.TypeBookClear, 36, 0,
		mbpBookClear(1, clearSide, scope, perSeq, fromPrice))
}

// group appends a complete snapshot group: Begin, the levels, End.
func (tp *mbpTape) group(instrID uint32, anchor uint64, snapID, lastK, depth uint32, levels ...[3]int64) *mbpTape {
	tp.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1,
		mbpSnapBegin(instrID, anchor, uint32(len(levels)), snapID, lastK, depth))
	for _, l := range levels {
		tp.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1,
			mbpSnapLevel(snapID, l[0], uint64(l[1]), uint8(l[2])))
	}
	return tp.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(instrID, anchor, snapID))
}

// bid/ask build a level triple for `group`.
func bid(price, qty int64) [3]int64 { return [3]int64{price, qty, mbpClearSideBid} }
func ask(price, qty int64) [3]int64 { return [3]int64{price, qty, mbpClearSideAsk} }

// violated runs the tape through one engine and reports whether ruleID was
// violated.
func (tp *mbpTape) violated(t *testing.T, ruleID string) bool {
	t.Helper()
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBP, SourceRegistry: stubRegistry{}}, ac)
	// **Flush at every port change.** The engine buffers per port and drains each
	// independently, so feeding an interleaved tape straight through would let the
	// mktdata deltas be classified before the snapshot group that precedes them —
	// and the oracle would then diff a ladder against a capture from the wrong
	// point. Flushing at the boundary is what makes "these deltas, then this
	// snapshot" mean that. It only drains, so calling it repeatedly is safe.
	var last core.Port
	first := true
	for _, s := range tp.steps {
		if !first && s.port != last {
			e.Flush()
		}
		first = false
		f, sf := wire.Decode(s.raw, wire.MagicMBP)
		e.Process(f, s.port, sf)
		last = s.port
	}
	e.Flush()
	for _, fn := range ac.findings {
		if fn.RuleID == ruleID && fn.Status == core.Violation {
			return true
		}
	}
	return false
}

type mbpStep = struct {
	port core.Port
	raw  []byte
}

// --- MBP.DELTA.PERINSTR_DENSITY ---

func TestMBPPerInstrDensity(t *testing.T) {
	dense := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 100, 5).
		delta(2, mbpClearSideBid, mbpActionChange, 100, 6)
	if dense.violated(t, "MBP.DELTA.PERINSTR_DENSITY") {
		t.Error("a dense series must not be reported")
	}

	// 1 -> 3 skips a number. Publishers MUST emit densely, so a subscriber cannot
	// tell this from loss — and either way the instrument needs re-snapshotting.
	skipped := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 100, 5).
		delta(3, mbpClearSideBid, mbpActionChange, 100, 6)
	if !skipped.violated(t, "MBP.DELTA.PERINSTR_DENSITY") {
		t.Error("a skipped Per-Instrument Seq must be reported")
	}

	// LevelUpdate and BookClear share one series, because both mutate the book and
	// their relative order is significant. A publisher numbering them separately
	// would look dense on each type and be a gap on the series.
	shared := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 100, 5).
		clear(2, mbpClearSideBid, mbpScopeWholeSide, 0).
		delta(3, mbpClearSideBid, mbpActionNew, 100, 5)
	if shared.violated(t, "MBP.DELTA.PERINSTR_DENSITY") {
		t.Error("one series spanning both message types must not be reported")
	}
	split := newTape().
		delta(1, mbpClearSideBid, mbpActionNew, 100, 5).
		clear(1, mbpClearSideBid, mbpScopeWholeSide, 0)
	if !split.violated(t, "MBP.DELTA.PERINSTR_DENSITY") {
		t.Error("a BookClear restarting the series must be reported")
	}
}

// --- MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET ---

// The series restarts only on a Reset Count change. If it restarted at snapshots,
// a subscriber that missed one and then saw seq 1 could not tell a fresh
// post-snapshot delta from a late duplicate of an old one.
func TestMBPPerInstrSeqSurvivesASnapshotBoundary(t *testing.T) {
	good := newTape().
		delta(5, mbpClearSideBid, mbpActionNew, 100, 5).
		group(1, 0, 1, 5, 0, bid(100, 5)).
		delta(6, mbpClearSideBid, mbpActionChange, 100, 6)
	if good.violated(t, "MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET") {
		t.Error("continuing the series across a snapshot must not be reported")
	}

	bad := newTape().
		delta(5, mbpClearSideBid, mbpActionNew, 100, 5).
		group(1, 0, 1, 5, 0, bid(100, 5)).
		delta(1, mbpClearSideBid, mbpActionChange, 100, 6)
	if !bad.violated(t, "MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET") {
		t.Error("restarting the series at a snapshot boundary must be reported")
	}
}

// --- MBP.DELTA.ABSOLUTE_APPLY ---

// `Quantity = 0` MUST pair with `Action = 3`. A publisher numbering the Action
// table from New instead of Unknown puts every removal out as a Change carrying
// zero — self-consistent, so invisible to a round-trip test. That bug shipped.
func TestMBPAbsoluteApplyPairsZeroWithDelete(t *testing.T) {
	if newTape().delta(1, mbpClearSideBid, mbpActionDelete, 100, 0).
		violated(t, "MBP.DELTA.ABSOLUTE_APPLY") {
		t.Error("Delete carrying zero is the conformant pairing")
	}
	if !newTape().delta(1, mbpClearSideBid, mbpActionChange, 100, 0).
		violated(t, "MBP.DELTA.ABSOLUTE_APPLY") {
		t.Error("Quantity = 0 with Action = Change must be reported")
	}
	if !newTape().delta(1, mbpClearSideBid, mbpActionDelete, 100, 7).
		violated(t, "MBP.DELTA.ABSOLUTE_APPLY") {
		t.Error("Delete carrying a non-zero quantity must be reported")
	}
	// One price cannot bound both sides.
	if !newTape().clear(1, mbpClearSideBoth, mbpScopeFromPrice, 100).
		violated(t, "MBP.DELTA.ABSOLUTE_APPLY") {
		t.Error("Scope = 1 with Clear Side = Both must be reported")
	}
	if newTape().clear(1, mbpClearSideBoth, mbpScopeWholeSide, 0).
		violated(t, "MBP.DELTA.ABSOLUTE_APPLY") {
		t.Error("Clear Side = Both with whole-side scope is legal")
	}
}

// --- MBP.SNAP.GROUP_STRUCTURE ---

func TestMBPSnapshotGroupStructure(t *testing.T) {
	if newTape().group(1, 0, 1, 0, 0, bid(100, 5), bid(99, 3)).
		violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("a well-formed group must not be reported")
	}

	// Declared 2, carried 1. A subscriber discards the group on that mismatch, so
	// this is the difference between a usable snapshot and a silently unusable one.
	short := newTape()
	short.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 0, 2, 1, 0, 0))
	short.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(1, 100, 5, mbpClearSideBid))
	short.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 0, 1))
	if !short.violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("Total Levels disagreeing with the levels carried must be reported")
	}

	// A zero quantity here is malformed: on this port an absent level is
	// represented by absence, and removal is a mktdata concept.
	if !newTape().group(1, 0, 1, 0, 0, bid(100, 0)).
		violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("SnapshotLevel with Quantity = 0 must be reported")
	}

	// A level bound to a different group must not be absorbed into this one.
	foreign := newTape()
	foreign.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 0, 1, 1, 0, 0))
	foreign.add(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1, mbpSnapLevel(9, 100, 5, mbpClearSideBid))
	foreign.add(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, 0, 1))
	if !foreign.violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("a SnapshotLevel with a foreign Snapshot ID must be reported")
	}

	// Interleaving two instruments is forbidden outright: all frames carrying
	// levels for one instrument MUST precede the first frame for another.
	inter := newTape()
	inter.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(1, 0, 1, 1, 0, 0))
	inter.add(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1, mbpSnapBegin(2, 0, 1, 2, 0, 0))
	if !inter.violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("interleaved snapshot groups must be reported")
	}

	// An empty book is a valid snapshot: Begin(total=0) straight to End.
	if newTape().group(1, 0, 1, 0, 0).violated(t, "MBP.SNAP.GROUP_STRUCTURE") {
		t.Error("an empty book is a valid snapshot")
	}
}

// --- MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT ---

// The check the rest of the consumer exists to enable. A publisher whose internal
// ladder has drifted from the deltas it emitted passes every structural and
// sequence check; it is caught only by rebuilding the book independently from the
// delta stream and diffing it against the snapshot stream.
func TestMBPReconstructedBookMatchesSnapshot(t *testing.T) {
	// Baseline at Last Instrument Seq 0, one delta moving the level to 9, then a
	// group captured at Last Instrument Seq 1 — the same point we have applied.
	tape := func(snapQty int64) *mbpTape {
		return newTape().
			group(1, 0, 1, 0, 0, bid(100, 5)).
			delta(1, mbpClearSideBid, mbpActionChange, 100, 9).
			group(1, 1, 2, 1, 0, bid(100, snapQty))
	}
	if tape(9).violated(t, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("a snapshot agreeing with the replayed deltas must not be reported")
	}
	if !tape(4).violated(t, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("a snapshot diverging from the replayed deltas must be reported")
	}

	// A level the deltas removed but the snapshot still carries is the same class
	// of drift, in the direction a missed delete produces.
	stale := newTape().
		group(1, 0, 1, 0, 0, bid(100, 5), bid(99, 3)).
		delta(1, mbpClearSideBid, mbpActionDelete, 99, 0).
		group(1, 1, 2, 1, 0, bid(100, 5), bid(99, 3))
	if !stale.violated(t, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("a level the deltas deleted but the snapshot keeps must be reported")
	}
}

// Comparison is gated on `Last Instrument Seq`, not `Anchor Seq`. Deltas routinely
// arrive between the publisher's capture and the snapshot's delivery, so a group
// captured at a different point is simply not comparable — reporting it would fire
// on nearly every instrument on nearly every rotation.
func TestMBPReconstructionSkipsANonComparableCapture(t *testing.T) {
	tape := newTape().
		group(1, 0, 1, 0, 0, bid(100, 5)).
		delta(1, mbpClearSideBid, mbpActionChange, 100, 9).
		delta(2, mbpClearSideBid, mbpActionChange, 100, 11).
		// Captured at seq 1 while we have applied 2: the snapshot is older.
		group(1, 1, 2, 1, 0, bid(100, 9))
	if tape.violated(t, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("a snapshot captured at a different point must not be compared")
	}
}

// Loss must never be reported as non-conformance: after a per-instrument gap the
// reconstruction is known-incomplete, so a mismatch is not the publisher's fault.
func TestMBPReconstructionIsUnverifiableAfterAGap(t *testing.T) {
	tape := newTape().
		group(1, 0, 1, 0, 0, bid(100, 5)).
		delta(1, mbpClearSideBid, mbpActionChange, 100, 9).
		delta(5, mbpClearSideBid, mbpActionChange, 100, 7). // gap: 1 -> 5
		group(1, 2, 2, 5, 0, bid(100, 42))
	if tape.violated(t, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("a mismatch after observed loss must not be blamed on the publisher")
	}
}
