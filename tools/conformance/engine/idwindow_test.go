package engine

import "testing"

// TestIDWindowStaysBounded: the regression for an MBO book that grew for the
// full length of an era. A window never holds more than its limit, whatever
// the number of adds.
func TestIDWindowStaysBounded(t *testing.T) {
	w := newIDWindow(8)
	for id := uint64(1); id <= 10_000; id++ {
		w.add(id)
		if w.size() > 8 || len(w.ring) > 8 {
			t.Fatalf("after %d adds: %d members, %d ring entries, want at most 8", id, w.size(), len(w.ring))
		}
	}
	for id := uint64(9_993); id <= 10_000; id++ {
		if !w.has(id) {
			t.Errorf("newest id %d must still be a member", id)
		}
	}
	if w.has(9_992) {
		t.Error("id 9992 must be evicted")
	}
}

// TestIDWindowEvictedRange: a miss in the range of evicted IDs is a possible
// eviction; a miss out of that range is not.
func TestIDWindowEvictedRange(t *testing.T) {
	w := newIDWindow(2)
	w.add(10)
	w.add(20)
	if w.mayHaveEvicted(10) {
		t.Fatal("no eviction yet, so no ID may be evicted")
	}
	w.add(30) // evicts 10
	w.add(40) // evicts 20
	for _, tc := range []struct {
		id   uint64
		want bool
	}{{5, false}, {10, true}, {15, true}, {20, true}, {25, false}, {30, false}, {50, false}} {
		if got := w.mayHaveEvicted(tc.id); got != tc.want {
			t.Errorf("mayHaveEvicted(%d) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestIDWindowRemoveAndReAdd: a removed ID is not an eviction, and a re-added
// ID survives the eviction of its older ring entry.
func TestIDWindowRemoveAndReAdd(t *testing.T) {
	w := newIDWindow(2)
	w.add(1)
	w.remove(1)
	w.add(2)
	w.add(3) // drops the stale entry for 1: not an eviction
	if w.mayHaveEvicted(1) {
		t.Error("a removed ID must not count as evicted")
	}

	w = newIDWindow(2)
	w.add(7)
	w.add(8)
	w.add(7) // the ring drops the stale entry for 7: not an eviction
	if !w.has(7) || !w.has(8) {
		t.Fatalf("re-add of 7 must keep 7 and 8: has(7)=%v has(8)=%v", w.has(7), w.has(8))
	}
	if w.mayHaveEvicted(7) {
		t.Error("a re-added ID must not count as evicted")
	}
	w.add(9) // the oldest current entry is 8
	if !w.has(7) || w.has(8) || !w.has(9) {
		t.Errorf("want {7, 9}: has(7)=%v has(8)=%v has(9)=%v", w.has(7), w.has(8), w.has(9))
	}
}
