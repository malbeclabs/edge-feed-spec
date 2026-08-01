package engine

// A price-keyed book, reconstructed independently from the delta stream so it can
// be diffed against the periodic snapshot stream.
//
// **Deliberately not `instrBook`.** That one is order-keyed — `applyOrderAdd`,
// `applyOrderExecute`, `applyOrderCancel` — because market-by-order carries the
// resting-order population. This feed carries the price-aggregated projection of
// the same book, so a level is identified by `(Side, Price)` and carries the
// absolute aggregate resting there. Sharing a type would mean modelling orders
// this feed never sees.

// mbpLevelKey identifies a level. Rank is deliberately absent: the spec makes
// `(Side, Price)` the addressing model and says a subscriber MUST NOT key book
// state on `Level Index`, which is informational and stale the moment any level
// is inserted beneath it.
type mbpLevelKey struct {
	side  uint8
	price int64
}

// mbpBook is one instrument's ladder.
type mbpBook struct {
	levels map[mbpLevelKey]uint64
	// depthBound is nil until a SnapshotBegin establishes one.
	//
	// **Defaults to unknown, not to 0.** `0` on the wire is a positive claim of a
	// complete book; defaulting to it would have a never-snapshotted instrument
	// assert completeness through the subscriber's own initialisation rather than
	// through anything the publisher sent — the exact failure `Depth Bound` exists
	// to prevent.
	depthBound *uint32
}

func newMBPBook() *mbpBook {
	return &mbpBook{levels: make(map[mbpLevelKey]uint64)}
}

// applyLevelUpdate applies one delta by quantity alone.
//
// `Action` MUST NOT gate this: every `LevelUpdate` states the complete resulting
// state of one level, so applying by quantity always produces the correct level
// regardless of what `Action` claims. An `Action` that disagrees is a publisher
// defect to count, never a reason to take a different code path — which is what
// keeps a wrong byte from corrupting a book.
func (b *mbpBook) applyLevelUpdate(side uint8, price int64, qty uint64) {
	k := mbpLevelKey{side: side, price: price}
	if qty == 0 {
		delete(b.levels, k)
		return
	}
	b.levels[k] = qty
}

// applyBookClear removes levels in bulk.
//
// `Scope = 1` clears from `From Price` **outward to the far end of the side**:
// for bids every level at or below it, for asks every level at or above it. That
// asymmetry is the whole point of the field — a single price bounds one side only,
// which is why `Scope = 1` with `Clear Side = Both` is malformed and rejected
// before reaching here.
func (b *mbpBook) applyBookClear(clearSide uint8, scope uint8, fromPrice int64) {
	for k := range b.levels {
		if clearSide != mbpClearSideBoth && k.side != clearSide {
			continue
		}
		if scope == mbpScopeFromPrice {
			switch k.side {
			case mbpClearSideBid:
				if k.price > fromPrice {
					continue
				}
			default: // ask
				if k.price < fromPrice {
					continue
				}
			}
		}
		delete(b.levels, k)
	}
}

// inside returns the best bid and best ask, and whether each exists.
func (b *mbpBook) inside() (bid int64, hasBid bool, ask int64, hasAsk bool) {
	for k := range b.levels {
		if k.side == mbpClearSideBid {
			if !hasBid || k.price > bid {
				bid, hasBid = k.price, true
			}
			continue
		}
		if !hasAsk || k.price < ask {
			ask, hasAsk = k.price, true
		}
	}
	return bid, hasBid, ask, hasAsk
}

// crossed reports whether the inside market is inverted.
//
// Strict `>`: a locked book (`bid == ask`) is routine on some venues, so the spec
// does not count locking as crossed.
func (b *mbpBook) crossed() bool {
	bid, hasBid, ask, hasAsk := b.inside()
	return hasBid && hasAsk && bid > ask
}

// clone snapshots the ladder, for diffing against a snapshot group captured at an
// earlier anchor while the live book keeps advancing.
func (b *mbpBook) clone() map[mbpLevelKey]uint64 {
	out := make(map[mbpLevelKey]uint64, len(b.levels))
	for k, v := range b.levels {
		out[k] = v
	}
	return out
}

// diffLevels returns the keys that differ between two ladders, with both values.
// Used to report a reconstruction mismatch concretely rather than as a count.
type mbpLevelDiff struct {
	key      mbpLevelKey
	fromBook uint64 // 0 = absent
	fromSnap uint64 // 0 = absent
}

func diffMBPLevels(book, snap map[mbpLevelKey]uint64) []mbpLevelDiff {
	var out []mbpLevelDiff
	for k, bv := range book {
		if sv, ok := snap[k]; !ok || sv != bv {
			out = append(out, mbpLevelDiff{key: k, fromBook: bv, fromSnap: snap[k]})
		}
	}
	for k, sv := range snap {
		if _, ok := book[k]; !ok {
			out = append(out, mbpLevelDiff{key: k, fromBook: 0, fromSnap: sv})
		}
	}
	return out
}
