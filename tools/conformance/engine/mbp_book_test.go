package engine

import "testing"

func TestMBPBookAppliesByQuantityAlone(t *testing.T) {
	b := newMBPBook()
	b.applyLevelUpdate(mbpClearSideBid, 100, 5)
	b.applyLevelUpdate(mbpClearSideBid, 100, 7) // absolute, not additive
	if got := b.levels[mbpLevelKey{mbpClearSideBid, 100}]; got != 7 {
		t.Fatalf("quantity = %d, want 7 (absolute, never a delta)", got)
	}
	b.applyLevelUpdate(mbpClearSideBid, 100, 0) // 0 removes
	if _, ok := b.levels[mbpLevelKey{mbpClearSideBid, 100}]; ok {
		t.Fatal("Quantity = 0 must remove the level")
	}
}

// `Scope = 1` clears outward to the far end of the side, and "outward" is opposite
// per side: down for bids, up for asks. Getting that backwards would clear the
// inside market instead of the tail.
func TestMBPBookClearFromPriceIsSideAsymmetric(t *testing.T) {
	mk := func() *mbpBook {
		b := newMBPBook()
		for _, p := range []int64{98, 99, 100} {
			b.applyLevelUpdate(mbpClearSideBid, p, 1)
		}
		for _, p := range []int64{101, 102, 103} {
			b.applyLevelUpdate(mbpClearSideAsk, p, 1)
		}
		return b
	}

	b := mk()
	b.applyBookClear(mbpClearSideBid, mbpScopeFromPrice, 99)
	// Bids at or BELOW 99 go; 100 stays; asks untouched.
	if _, ok := b.levels[mbpLevelKey{mbpClearSideBid, 100}]; !ok {
		t.Error("bid above From Price must survive")
	}
	for _, p := range []int64{98, 99} {
		if _, ok := b.levels[mbpLevelKey{mbpClearSideBid, p}]; ok {
			t.Errorf("bid %d at or below From Price must be cleared", p)
		}
	}
	if len(b.levels) != 4 { // 100 + three asks
		t.Errorf("levels = %d, want 4", len(b.levels))
	}

	b = mk()
	b.applyBookClear(mbpClearSideAsk, mbpScopeFromPrice, 102)
	// Asks at or ABOVE 102 go; 101 stays.
	if _, ok := b.levels[mbpLevelKey{mbpClearSideAsk, 101}]; !ok {
		t.Error("ask below From Price must survive")
	}
	for _, p := range []int64{102, 103} {
		if _, ok := b.levels[mbpLevelKey{mbpClearSideAsk, p}]; ok {
			t.Errorf("ask %d at or above From Price must be cleared", p)
		}
	}
}

func TestMBPBookClearWholeSideAndBoth(t *testing.T) {
	b := newMBPBook()
	b.applyLevelUpdate(mbpClearSideBid, 100, 1)
	b.applyLevelUpdate(mbpClearSideAsk, 101, 1)

	b.applyBookClear(mbpClearSideBid, mbpScopeWholeSide, 0)
	if len(b.levels) != 1 {
		t.Fatalf("levels = %d, want 1 (ask survives)", len(b.levels))
	}
	b.applyBookClear(mbpClearSideBoth, mbpScopeWholeSide, 0)
	if len(b.levels) != 0 {
		t.Fatalf("levels = %d, want 0", len(b.levels))
	}
}

// A locked book is routine on some venues, so the comparison is strict `>` and
// locking is not crossed.
func TestMBPBookCrossedIsStrictAndNeedsBothSides(t *testing.T) {
	one := func(bid, ask int64) *mbpBook {
		b := newMBPBook()
		b.applyLevelUpdate(mbpClearSideBid, bid, 1)
		b.applyLevelUpdate(mbpClearSideAsk, ask, 1)
		return b
	}
	if one(100, 101).crossed() {
		t.Error("normal book must not be crossed")
	}
	if one(100, 100).crossed() {
		t.Error("locked book must not count as crossed")
	}
	if !one(101, 100).crossed() {
		t.Error("inverted book must be crossed")
	}

	oneSided := newMBPBook()
	oneSided.applyLevelUpdate(mbpClearSideBid, 100, 1)
	if oneSided.crossed() {
		t.Error("a one-sided book cannot be crossed")
	}
}

// Depth is unknown until a SnapshotBegin says otherwise. Defaulting to 0 would
// make a never-snapshotted instrument assert completeness on its own.
func TestMBPBookDepthDefaultsToUnknown(t *testing.T) {
	if newMBPBook().depthBound != nil {
		t.Fatal("depth bound must start unknown, not 0")
	}
}

func TestDiffMBPLevelsReportsBothDirections(t *testing.T) {
	book := map[mbpLevelKey]uint64{
		{mbpClearSideBid, 100}: 5, // quantity differs
		{mbpClearSideBid, 99}:  1, // missing from snapshot
	}
	snap := map[mbpLevelKey]uint64{
		{mbpClearSideBid, 100}: 6,
		{mbpClearSideAsk, 101}: 2, // missing from book
	}
	got := diffMBPLevels(book, snap)
	if len(got) != 3 {
		t.Fatalf("diffs = %d, want 3: %+v", len(got), got)
	}
}
