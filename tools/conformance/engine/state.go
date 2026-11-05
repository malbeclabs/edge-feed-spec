package engine

import (
	"container/heap"
	"hash/maphash"
	"net/netip"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// eraOrder records, for one source address and one Channel ID, the order in
// which the Reset Count values of that channel arrived. The three ports of the
// channel share one eraOrder, because the Reset Count is shared across them.
//
// **It orders eras by arrival, never by value.** The market-by-price and
// market-by-order specs say a subscriber detects a reset by testing the Reset
// Count for inequality, and "never compare for ordering". A comparison by value cannot see a run of more
// than 127 resets: a port that sees none of them reads the next value as older,
// and a tracker that drops older values then drops every later datagram of that
// port. A publisher that resets its channel 229 times while it sends nothing on
// the snapshot port makes this happen, and the snapshot series then reads as
// transport loss at its full datagram rate.
//
// The order is still necessary. A datagram sent before a reset can arrive after
// the first datagram of the new era. On its own port that is a straggler of the
// era the port left (see enqueue). On another port it is a frame of an era the
// channel left, which must not discard the state of the current one (see
// Engine.mbpObserveEra). The arrival order says which era is which, and a Reset
// Count value that the channel used before and uses again is a new era.
type eraOrder struct {
	// pos holds, for each Reset Count value, the ordinal of the most recent era
	// that started with that value. Ordinals start at 1 and only increase.
	pos  map[uint8]uint64
	next uint64
	// mbpWiped is the ordinal of the era the market-by-price state of this
	// channel was last discarded for; see Engine.mbpObserveEra.
	mbpWiped    uint64
	mbpWipedSet bool
}

func newEraOrder() *eraOrder { return &eraOrder{pos: make(map[uint8]uint64)} }

// enter returns the ordinal of the era a port enters when it starts to accept
// Reset Count era. from is the ordinal of the era the port leaves, 0 for a port
// that has not accepted a datagram yet.
//
// If another port of the channel entered the same value after from, the port
// joins that era. Otherwise the value starts a new era: the channel has not
// used it since the port's own era started, so a match further back is an
// earlier use of the same value and not this era.
func (o *eraOrder) enter(era uint8, from uint64) uint64 {
	if p, ok := o.pos[era]; ok && p > from {
		return p
	}
	o.next++
	o.pos[era] = o.next
	return o.next
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
	// captureEpoch is Engine.captureLossEpoch as it stood when this frame
	// arrived. Stamped at intake and not read until the frame is classified,
	// because the reorder buffer separates the two: an admission that arrives
	// while a frame is buffered would otherwise be attributed to the frame ahead
	// of it in the window, which arrived before the drop and cannot be the one
	// the drop explains.
	captureEpoch uint64
}

// bufferItem wraps an intakeTuple for the per-series min-heap.
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

// instanceKey identifies one channel instance: "one path's view of one channel,
// keyed (source IP address, Channel ID, destination port)" (GLOSSARY.md), which
// is the unit that owns a sequence series and a Reset Count. Two publishers
// serving one channel share a group and a port and are told apart by transport,
// so the source address is part of the identity and not a detail — see
// "Redundant Channel Instances" in market-by-price/spec.md.
type instanceKey struct {
	src  netip.Addr
	port core.Port
	ch   uint8
}

// portTracker holds the sequencing state of one channel instance on one port.
type portTracker struct {
	// src is the instance's own half of its key, kept here so that classification
	// can name the publisher it is judging. classify() receives the tracker and
	// not the key, and it runs from two callers — Process, which has the address,
	// and Flush, which iterates the map and would otherwise have to thread it
	// through every drain path.
	src netip.Addr

	// lastSeq is nil until the first frame is classified (accepted).
	lastSeq *uint64
	// lastSendTS is the SendTS of the last accepted, forward-seq frame.
	// Updated only when seq advances, so it reflects the last monotonically
	// increasing point (not a backward-motion straggler).
	lastSendTS *uint64
	// refdataEra is the Reset Count the refdata set-state machine last acted on
	// for THIS channel instance. Distinct from era below, which gates datagram
	// sequencing: the two are read at different points and a shared field would
	// couple a quarantined straggler to a set reset.
	refdataEra uint8
	// refdataEraSeeded is false until the first refdata datagram of this instance.
	refdataEraSeeded bool
	// era is the current Reset Count we accept.
	era uint8
	// eraInitialized is false until the first frame seeds the era. Before it is
	// set, any incoming ResetCount is accepted and seeds the era.
	eraInitialized bool
	// order is the arrival order of the channel's eras, shared with the other
	// ports of this source address and Channel ID (Engine.instanceTrack). eraPos
	// is the ordinal of era in it, and prevPos the ordinal of the era this port
	// left last (0 for none).
	order   *eraOrder
	eraPos  uint64
	prevPos uint64
	// stragglers counts the datagrams of the era this port left that it dropped
	// since it last accepted one. A datagram delayed past a reset is a straggler.
	// A run of them longer than the reorder window is not: it is the channel's
	// current series, and the port enters its era (see enqueue).
	stragglers int
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
	// buf is the bounded reorder buffer for this series.
	buf portBuffer
	// unclassified holds the items a drain has already taken out of buf and has not
	// handed to the classifier yet — the tail of the batch the engine is walking.
	//
	// **A drain empties the heap before the first item is classified**, so buf alone
	// answers "nothing is pending" while a whole serving period's definitions are still
	// in flight. A check that decides on the set the engine has read so far has to see
	// them; see Engine.classifyDrained and refdataUnclassifiedWithin.
	unclassified []*bufferItem
	// lastArrivalSendTS is the greatest SendTS this instance has delivered, counted on
	// arrival rather than on classification, quarantined stragglers included. It tells a
	// path that stopped sending from one that is behind: see refdataUnclassifiedWithin.
	lastArrivalSendTS    uint64
	lastArrivalSendTSSet bool
	// arrivalCounter is incremented on each push and stored in bufferItem.arrival
	// to provide stable heap ordering for equal-seq duplicates.
	arrivalCounter uint64
	// dirtyWindow is set when a gap was declared while the reorder window was
	// non-empty (consumed by Task 18 gate.go).
	dirtyWindow bool
	// taints counts the times dirtyWindow was set, and is never reset. dirtyWindow
	// stays true for the era, so it cannot say whether a gap fell *between* two
	// observations. A rule that compares two observations stores this count with
	// the first one: a different count at the second one means a gap between them.
	taints uint64
	// captureDirty is set when the *capture* admitted losing datagrams while this
	// instance's window was open (see Engine.ObserveCaptureLoss). It never gates
	// anything on its own: the taint that stops a gated rule from grading a
	// Violation is dirtyWindow, and this only names the owner of the loss so the
	// finding reports capture_loss rather than loss.
	//
	// Shorter-lived than dirtyWindow, and it has to be. dirtyWindow answers "could
	// a gap explain this?", which stays true for the era once a gap is seen;
	// this answers "is the recorder's admitted drop what that gap was?", and the
	// drop can only be the answer for the first frame after it. classify
	// recomputes it per frame from captureEpochSeen, so a network gap later in
	// the same era is reported as the network's. ObserveCaptureLoss also sets it
	// directly, for the end-of-run findings that no further frame follows.
	captureDirty bool
	// captureEpochSeen is the arrival-time capture-loss epoch of the last frame
	// that advanced this series (intakeTuple.captureEpoch). A frame stamped
	// higher arrived after an admission this series has not yet been given a gap
	// for; a frame stamped equal has already spent it.
	captureEpochSeen uint64
	// lastHbSendTS is the SendTS of this instance's most-recent Heartbeat, the
	// baseline for HEARTBEAT.CADENCE. Per instance because each publisher
	// heartbeats on its own schedule: one baseline for a port let one arm's
	// heartbeats cover another arm's total outage. Deliberately not cleared on an
	// era advance — the cadence expectation spans a publisher reset.
	lastHbSendTS    uint64
	lastHbSendTSSet bool
	// lastSeen orders instances for eviction; see Engine.instanceTrack.
	lastSeen uint64
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
		order:         newEraOrder(),
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
		// seq-last form (rather than last+1) avoids a u64 overflow when last is
		// MaxUint64; unreachable for a seq starting at 0, but keeps the comparison
		// total. A forward jump of more than 1 is a gap.
		if seq > last && seq-last > 1 {
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
	t.prevPos = t.eraPos
	t.eraPos = t.order.enter(newEra, t.eraPos)
	t.stragglers = 0
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
	t.captureDirty = false
}

// taint marks the window dirty. Every write of dirtyWindow = true goes through it,
// so taints counts each one.
func (t *portTracker) taint() {
	t.dirtyWindow = true
	t.taints++
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

// enqueue pushes an intake tuple into this series' reorder buffer, honouring
// era semantics. See enqueueResult for the call sequence the caller must follow.
func (pt *portTracker) enqueue(
	item intakeTuple,
	reorderWindow int,
) enqueueResult {
	inEra := item.frame.Header.ResetCount
	inSeq := item.frame.Header.Sequence

	// Before the first frame has been seen on this port, seed the era from whatever
	// ResetCount arrives first (any value 0–255 is valid). If another port of the
	// channel already entered that value, this port joins the same era.
	if !pt.eraInitialized {
		pt.era = inEra
		pt.eraInitialized = true
		pt.eraPos = pt.order.enter(inEra, 0)
	}

	if inEra != pt.era {
		// Any change of the Reset Count is a reset, except a datagram of the era
		// this port left last. That one was delayed past the reset: drop it without
		// buffering. A run of them longer than the reorder window is not a delay,
		// so the run ends and the port enters that era as a reset.
		//
		// Only the era left last. A value the channel used earlier and uses again
		// is a new era (eraOrder), and a datagram delayed past two resets is
		// rare enough that reading it as a reset costs less than the alternative:
		// a match against any earlier use of the value dropped the new era of a
		// channel that had cycled through its 256 values.
		if p, ok := pt.order.pos[inEra]; ok && pt.prevPos != 0 && p == pt.prevPos &&
			pt.stragglers < max(reorderWindow, 1) {
			pt.stragglers++
			return enqueueResult{quarantine: true}
		}
		// Drain the current-era buffer BEFORE advancing. The caller must classify
		// preDrainItems first (with the old era still active on pt), then call
		// pt.advanceEra(newEra). This ensures old-era gap/seq detection fires
		// correctly under the old era's tracker state. The new-era item is pushed
		// by the caller after the advance (pushAndPop).
		return enqueueResult{
			preDrainItems: pt.drainAll(),
			advanceEra:    true,
			newEra:        inEra,
		}
	}
	pt.stragglers = 0

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
