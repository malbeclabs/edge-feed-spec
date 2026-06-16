package engine

import (
	"container/heap"
	"hash/maphash"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// eraResult is the 4-way classification of an incoming Reset Count relative to
// the currently tracked Reset Count.
type eraResult int

const (
	eraSame      eraResult = iota // d == 0: same era, buffer normally
	eraNewer                      // d in [1..127]: advance era
	eraOlder                      // d in [129..255]: straggler, drop
	eraAmbiguous                  // d == 128: cannot determine direction, quarantine
)

// eraRelation classifies in relative to cur using modular uint8 arithmetic.
// The convention is the same as RFC 1982 serial number arithmetic (wrap at 128).
func eraRelation(cur, in uint8) eraResult {
	d := uint8(in - cur) // wraps modulo 256
	switch {
	case d == 0:
		return eraSame
	case d <= 127:
		return eraNewer
	case d == 128:
		return eraAmbiguous
	default: // d in [129..255]
		return eraOlder
	}
}

// eraSeq is the compound deduplication key: (era, seq).
type eraSeq struct {
	era uint8
	seq uint64
}

// intakeTuple is the unit buffered in the reorder window.
type intakeTuple struct {
	frame          *wire.Frame
	port           core.Port
	structFindings []wire.StructFinding
}

// bufferItem wraps an intakeTuple for the per-port min-heap.
type bufferItem struct {
	era     uint8
	seq     uint64
	arrival uint64 // monotonically increasing counter for stable tie-breaking
	tuple   intakeTuple
}

// portBuffer is a min-heap of bufferItems keyed by (seq, arrival).
// Because we advance eras by draining the current-era buffer first, all items
// in the heap at any one time share the same era; seq is the primary heap key.
// arrival breaks ties among equal seq values: the first-arrived frame is popped
// first, so "first accepted, duplicate dropped" semantics hold.
type portBuffer []*bufferItem

func (b portBuffer) Len() int { return len(b) }
func (b portBuffer) Less(i, j int) bool {
	if b[i].seq != b[j].seq {
		return b[i].seq < b[j].seq
	}
	return b[i].arrival < b[j].arrival
}
func (b portBuffer) Swap(i, j int) { b[i], b[j] = b[j], b[i] }
func (b *portBuffer) Push(x any)   { *b = append(*b, x.(*bufferItem)) }
func (b *portBuffer) Pop() any {
	old := *b
	n := len(old)
	x := old[n-1]
	old[n-1] = nil
	*b = old[:n-1]
	return x
}

// portTracker holds per-port sequencing state.
type portTracker struct {
	// lastSeq is nil until the first frame is classified (accepted).
	lastSeq *uint64
	// lastSendTS is the SendTS of the last accepted, forward-seq frame.
	// Updated only when seq advances, so it reflects the last monotonically
	// increasing point (not a backward-motion straggler).
	lastSendTS *uint64
	// era is the current Reset Count we accept.
	era uint8
	// eraInitialized is false until the first frame seeds the era. Before it is
	// set, any incoming ResetCount is accepted and seeds the era (avoids
	// quarantining ResetCount 128–255 on a fresh port).
	eraInitialized bool
	// payloadHashes maps (era,seq) → maphash value for dedup detection within the
	// bounded eviction window. A 64-bit collision would cause a divergent dup to be
	// silently dropped; the probability is ~1/2^64 per comparison, acceptable for a
	// conformance checker. Entries are evicted via hashRing (FIFO) when the ring
	// is full.
	payloadHashes map[eraSeq]uint64
	// hashRing is a FIFO ring of eraSeq keys used to evict old entries from
	// payloadHashes. Its capacity is set to 2×reorderWindow.
	hashRing     []eraSeq
	hashRingCap  int
	hashRingPos  int
	hashRingSize int // number of valid entries currently in the ring (≤ cap)
	// buf is the bounded reorder buffer for this port.
	buf portBuffer
	// arrivalCounter is incremented on each push and stored in bufferItem.arrival
	// to provide stable heap ordering for equal-seq duplicates.
	arrivalCounter uint64
	// dirtyWindow is set when a gap was declared while the reorder window was
	// non-empty (consumed by Task 18 gate.go).
	dirtyWindow bool
}

// newPortTracker constructs a portTracker with a given reorder window.
// The dedup ring is sized to 2×reorderWindow so it covers the full
// in-flight reorder + a cushion for retransmissions.
func newPortTracker(reorderWindow int) *portTracker {
	cap := reorderWindow * 2
	if cap < 16 {
		cap = 16
	}
	return &portTracker{
		payloadHashes: make(map[eraSeq]uint64, cap),
		hashRing:      make([]eraSeq, cap),
		hashRingCap:   cap,
	}
}

// hashRaw returns a stable hash of raw bytes using maphash with a fixed seed
// (reproducible within a process; we only compare hashes within the same port tracker).
var hashSeed = maphash.MakeSeed()

func hashRaw(b []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(hashSeed)
	_, _ = h.Write(b)
	return h.Sum64()
}

// observeResult is the outcome of observe().
type observeResult struct {
	isDup          bool // same (era,seq), identical bytes → silent drop
	isDivergentDup bool // same (era,seq), different bytes → FRAME.SEQ_DUP_DIVERGENT
	gapBefore      bool // true if seq jumped forward by more than 1 without a reset
}

// recordHash stores h under key in payloadHashes and evicts the oldest entry
// from the ring when the ring is at capacity. The ring guarantees payloadHashes
// never grows beyond hashRingCap entries within an era.
func (t *portTracker) recordHash(key eraSeq, h uint64) {
	if t.hashRingSize < t.hashRingCap {
		// Ring is not yet full: just fill the next slot.
		t.hashRing[t.hashRingPos] = key
		t.hashRingSize++
	} else {
		// Ring is full: evict the oldest slot before overwriting it.
		old := t.hashRing[t.hashRingPos]
		delete(t.payloadHashes, old)
		t.hashRing[t.hashRingPos] = key
	}
	t.hashRingPos = (t.hashRingPos + 1) % t.hashRingCap
	t.payloadHashes[key] = h
}

// observe is called by the classifier (in seq order, after the item is popped
// from the reorder buffer). By the time observe is called, pt.era matches the
// item's era (old-era items are classified before advanceEra in Process).
//
// Returns the classification outcome.
// observe uses item.era as the dedup key. Callers ensure pt.era == item.era.
func (t *portTracker) observe(seq uint64, sendTS uint64, raw []byte, era uint8) observeResult {
	key := eraSeq{era, seq}
	h := hashRaw(raw)

	// Check dedup: have we seen this (era,seq) before?
	if prev, ok := t.payloadHashes[key]; ok {
		if prev == h {
			return observeResult{isDup: true}
		}
		return observeResult{isDivergentDup: true}
	}

	// Record the hash for future dedup checks (with ring eviction).
	t.recordHash(key, h)

	// Update seq/ts tracking.
	var gap bool
	if t.lastSeq != nil {
		last := *t.lastSeq
		if seq > last+1 {
			gap = true
		}
		// seq <= last → backward motion; handled by caller (FRAME.SEQ_RESET_GAP).
	}

	// Advance lastSeq only when strictly increasing.
	seqAdvanced := t.lastSeq == nil || seq > *t.lastSeq
	if seqAdvanced {
		cp := seq
		t.lastSeq = &cp
	}

	// Update lastSendTS only when seq is strictly advancing, so the baseline
	// reflects the last monotonically-increasing seq and is not corrupted by
	// backward-motion frames (which are flagged as SEQ_RESET_GAP, not used as
	// a SendTS reference point).
	if seqAdvanced {
		cp := sendTS
		t.lastSendTS = &cp
	}

	return observeResult{gapBefore: gap}
}

// advanceEra resets per-era state when a newer Reset Count is seen. It does NOT
// drain the buffer — draining is done by the caller (Engine.enqueue) before this
// is called.
func (t *portTracker) advanceEra(newEra uint8) {
	t.era = newEra
	t.lastSeq = nil
	t.lastSendTS = nil
	// Clear all hash entries and reset the ring. A new era starts fresh.
	clear(t.payloadHashes)
	clear(t.hashRing)
	t.hashRingPos = 0
	t.hashRingSize = 0
	// Clear the dirty-window flag: the new era starts with a clean observation
	// window.  The refdata state machine (Task 14) reads this flag to decide
	// whether to downgrade a Violation to Unverifiable; resetting it here
	// prevents stale gap signals from a prior era from suppressing violations
	// that are clearly observable in the new era.
	t.dirtyWindow = false
}

// enqueueResult is the structured result of enqueue. The caller must process
// the fields in order to preserve correctness:
//
//  1. Classify preDrainItems under the OLD era (before advancing).
//  2. If advanceEra is true, call pt.advanceEra(newEra) after step 1.
//  3. Classify postDrainItems under the new (current) era.
type enqueueResult struct {
	preDrainItems  []*bufferItem // old-era drained items to classify BEFORE advancing
	advanceEra     bool
	newEra         uint8
	postDrainItems []*bufferItem // new-era items popped by window overflow AFTER advancing
	quarantine     bool          // true → drop as straggler, do not classify anything
}

// enqueue pushes an intake tuple into the per-port reorder buffer, honouring
// era semantics. See enqueueResult for the call sequence the caller must follow.
func (pt *portTracker) enqueue(
	item intakeTuple,
	reorderWindow int,
) enqueueResult {
	inEra := item.frame.Header.ResetCount
	inSeq := item.frame.Header.Sequence

	// Before the first frame has been seen on this port, seed the era from whatever
	// ResetCount arrives first (any value 0–255 is valid). This avoids quarantining
	// the initial frames when the publisher starts at a non-zero ResetCount.
	if !pt.eraInitialized {
		pt.era = inEra
		pt.eraInitialized = true
	}

	rel := eraRelation(pt.era, inEra)
	switch rel {
	case eraOlder, eraAmbiguous:
		// Straggler or ambiguous: drop without buffering.
		return enqueueResult{quarantine: true}

	case eraNewer:
		// Drain the current-era buffer BEFORE advancing. The caller must classify
		// preDrainItems first (with the old era still active on pt), then call
		// pt.advanceEra(newEra). This ensures old-era gap/seq detection fires
		// correctly under the old era's tracker state.
		return enqueueResult{
			preDrainItems: pt.drainAll(),
			advanceEra:    true,
			newEra:        inEra,
			// The new-era item is enqueued after the caller calls advanceEra.
			// We signal this by returning the item in a dedicated slot — but since
			// we can't push it yet (era not advanced), we package it as part of
			// the result and let the caller push it. We defer push via a sentinel:
			// postDrainItems is filled later in Process after advance.
		}

	case eraSame:
		// Normal case: push and possibly pop.
	}

	pt.arrivalCounter++
	bi := &bufferItem{era: inEra, seq: inSeq, arrival: pt.arrivalCounter, tuple: item}
	heap.Push(&pt.buf, bi)

	var popped []*bufferItem
	if pt.buf.Len() > reorderWindow {
		popped = append(popped, heap.Pop(&pt.buf).(*bufferItem))
	}
	return enqueueResult{postDrainItems: popped}
}

// drainAll empties the buffer and returns all items in ascending seq order.
func (pt *portTracker) drainAll() []*bufferItem {
	out := make([]*bufferItem, 0, pt.buf.Len())
	for pt.buf.Len() > 0 {
		out = append(out, heap.Pop(&pt.buf).(*bufferItem))
	}
	return out
}

// pushAndPop adds item to the buffer and returns the minimum-seq item if the
// buffer exceeds reorderWindow. Used after an era advance to enqueue the first
// new-era item.
func (pt *portTracker) pushAndPop(item intakeTuple, reorderWindow int) []*bufferItem {
	pt.arrivalCounter++
	bi := &bufferItem{
		era:     item.frame.Header.ResetCount,
		seq:     item.frame.Header.Sequence,
		arrival: pt.arrivalCounter,
		tuple:   item,
	}
	heap.Push(&pt.buf, bi)
	if pt.buf.Len() > reorderWindow {
		return []*bufferItem{heap.Pop(&pt.buf).(*bufferItem)}
	}
	return nil
}
