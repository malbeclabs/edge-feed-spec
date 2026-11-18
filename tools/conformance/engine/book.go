package engine

// book.go — Per-instrument live order-id set (Tasks 20 + 24, Phase 1 + 4).
//
// Tracks, per (channelID, instrumentID), the set of live order IDs and the set
// of removed order IDs.  The full L3 book (quantity conservation checks) is
// implemented here as part of Task 24 (Phase 4 delta book builder).
//
// State:
//
//	liveOrder    { side, sourceID, price, remaining, orderFlags, enterTS }
//	liveOrders   map[uint64]liveOrder    — orders currently open
//	removed      *idWindow               — orders that have been fully cancelled or filled
//	hiddenIDs    *idWindow               — IDs seen in hidden OrderAdd messages
//	execTradeIDs *idWindow               — Trade IDs seen in OrderExecute messages
//
// The three idWindow sets have a fixed size (maxRemovedIDs, maxHiddenIDs,
// maxExecTradeIDs per instrument). Without the limit they grow for the full
// length of a Reset Count era. A miss on an ID that the window possibly evicted
// is Unverifiable with ReasonOverflow, not a violation (see idwindow.go).
//
// Lifecycle transitions:
//
//	OrderAdd    → insert into liveOrders (side, sourceID, price, remaining=qty, orderFlags, enterTS)
//	OrderCancel → move from liveOrders → removedOrders
//	OrderExecute (full-fill, ExecFlags bit0=1) → move from liveOrders → removedOrders
//	OrderExecute (partial,   ExecFlags bit0=0) → leave in liveOrders, decrement remaining
//
// Referential checks (all gated on gateConsumer):
//
//	REF.EXEC_DANGLING_ORDER       — execute on ID not in live or removed set
//	REF.CANCEL_DANGLING_ORDER     — cancel  on ID not in live or removed set
//	REF.DUPLICATE_LIVE_ORDERADD   — add     on ID already in live set
//	REF.OPERATION_AFTER_REMOVAL   — execute/cancel on ID in removed set
//	REF.SIDE_PRICE_CONSISTENCY    — aggressor side vs resting side mismatch
//	FIELD.SOURCE_ID_CONSISTENCY   — source ID drift across lifecycle
//
// Trade (0x04) rules:
//
//	TRADE.EXEC_GROUPING — non-zero TradeID must have a matching OrderExecute in execTradeIDs
//
// Exemptions:
//   - Hidden orders (OrderFlags bit 2 == 1): not tracked in live/removed; their
//     IDs are recorded in hiddenIDs to prevent false dangling violations on
//     subsequent execute/cancel messages for the same ID.
//   - Trade with TradeID == 0: exempt from EXEC_GROUPING.

import (
	"errors"
	"maps"
	"slices"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// The per-instrument limits of the idWindow sets. A full window costs about 68
// bytes for each ID of its limit (measured: the map, and a queue array of up to
// twice the limit), so one instrument at all three limits uses about 11 MiB.
// These are variables, not constants, so that tests can make a window evict
// after a few IDs.
//
// maxRemovedIDs decides how long REF.OPERATION_AFTER_REMOVAL can see an order
// after it leaves the book: 16,384 removals on that instrument. A publisher
// defect of this type (a second cancel, an execute after a cancel) usually
// occurs within a few messages of the removal. It is the window that fills on
// every feed, because every order leaves the book, so it sets the memory of a
// validator on a feed with many instruments, together with maxExecTradeIDs on a
// feed that sends Trade IDs, which is the normal case. Measured on a 10-minute capture of
// a busy feed: 346 instruments, 5.4 million removals, and the busiest instrument
// at 131,072 removals after 6 minutes. At a limit of 131,072 (8.5 MiB each) the
// removed windows of those instruments reach 2.9 GiB; at 16,384 (1.1 MiB each)
// they reach 370 MiB, and the busiest instrument keeps about 45 seconds of
// removals.
//
// The same limit sets how long REF.CANCEL_DANGLING_ORDER and
// REF.EXEC_DANGLING_ORDER can decide. A cancel or an execute for an order whose
// add never arrived is a violation only while its ID is above every ID that the
// removed window evicted. Once evictions pass that ID, the message gets
// Unverifiable with reason=overflow, because the ID may be a removed order that
// the window evicted. On the busiest instrument of that capture this happens
// about 45 seconds after the missing add (about 6 minutes at 131,072); on an
// average instrument about 10.5 minutes after it (about 84 at 131,072). The
// evicted range only grows until the next Reset Count, so for the rest of the
// era every dangling ID below the highest evicted ID is Unverifiable. That is
// the cost of this limit: these rules decide a dangling order soon after its
// add, and lose the rest to overflow, which unverifiable_total{reason="overflow"}
// counts. A larger limit buys minutes of that window at 8 times the memory.
//
// maxHiddenIDs is larger. A hidden order rests in the book, so a hidden order
// that is older than 131,072 newer hidden orders on the same instrument makes
// its execute or cancel Unverifiable. The window fills only on a feed that
// sends hidden orders.
//
// maxExecTradeIDs is the same as maxRemovedIDs. It fills on every feed that
// groups executions into trades (a non-zero Trade ID on OrderExecute), because
// each execute adds its Trade ID, so on such a feed it costs as much as the
// removed window: 1.1 MiB per instrument at 16,384. A Trade arrives next to its
// OrderExecute messages, so a limit at or below maxRemovedIDs costs
// TRADE.EXEC_GROUPING no coverage. At the earlier 65,536 it cost 4.25 MiB per
// instrument.
var (
	maxRemovedIDs   = 1 << 14
	maxHiddenIDs    = 1 << 17
	maxExecTradeIDs = 1 << 14
)

// Typed errors returned by the book mutation methods.
// Task 25 will switch on these to emit the corresponding findings.
var (
	// errOverfill is returned by applyOrderExecute when execQty > remaining for a
	// non-hidden resting order (maps to REF.EXEC_OVERFILL).
	errOverfill = errors.New("exec qty exceeds remaining")

	// errFullFillDisagree is returned by applyOrderExecute when fullFill=true but
	// execQty != remaining for a non-hidden resting order
	// (maps to REF.FULLFILL_FLAG_DISAGREEMENT).
	errFullFillDisagree = errors.New("full-fill flag set but exec qty != remaining")
)

// liveOrder holds the per-order metadata stored when an OrderAdd is processed.
// Extended in Task 24 to carry the full resting-order record needed by the
// Phase-4 reconstruction oracle.
type liveOrder struct {
	side       uint8
	sourceID   uint16
	price      int64
	remaining  uint64
	orderFlags uint8
	enterTS    uint64
}

// restingOrder is the read-only view returned by instrBook.Orders().
// It mirrors liveOrder but is exported for the oracle (Task 26) to diff.
type restingOrder struct {
	side       uint8
	price      int64
	remaining  uint64
	orderFlags uint8
	enterTS    uint64
}

// instrBook is the per-(channel, instrument) live order-id state.
type instrBook struct {
	// live holds orders currently in the book (added, not yet cancelled/filled).
	live map[uint64]liveOrder
	// removed holds IDs of orders that have been cancelled or fully filled.
	// Kept as a set to distinguish "dangling" (never seen) from "after removal".
	// It keeps the most recent maxRemovedIDs IDs.
	removed *idWindow
	// hiddenIDs holds the set of order IDs seen in hidden OrderAdd messages
	// (OrderFlags bit 2 = 1). Hidden orders share ID slots by spec and are not
	// tracked in live/removed, but execute/cancel for a hidden-add ID must not
	// be flagged as dangling. It keeps the most recent maxHiddenIDs IDs.
	hiddenIDs *idWindow
	// execTradeIDs holds the most recent maxExecTradeIDs Trade IDs seen in
	// OrderExecute messages. Used by TRADE.EXEC_GROUPING.
	execTradeIDs *idWindow
	// zeroed holds the IDs of live orders that an OrderAdd or an OrderExecute left
	// at zero remaining. Only those two can, so BATCH.ATOMICITY_CONSISTENCY reads
	// these IDs at a BatchBoundary instead of every live order of every book.
	zeroed []uint64
	// log holds the deltas this book applied in per-instrument seq order, so
	// that a snapshot group can seed or audit the book after the book moved past
	// the group's Last Instrument Seq (see deltaLog).
	log deltaLog
}

func newInstrBook() *instrBook {
	return &instrBook{
		live:         make(map[uint64]liveOrder),
		removed:      newIDWindow(maxRemovedIDs),
		hiddenIDs:    newIDWindow(maxHiddenIDs),
		execTradeIDs: newIDWindow(maxExecTradeIDs),
	}
}

// applyOrderAdd inserts a resting order into the book.
// Hidden orders (OrderFlags bit 2 = 1) are recorded in hiddenIDs only; they are
// not tracked in live/removed (spec: hidden orders share ID slots).
// Duplicate-live-id detection is left to the referential layer (Task 20); here we
// overwrite silently so subsequent ops work correctly.
func (bk *instrBook) applyOrderAdd(orderID uint64, side uint8, price int64, qty uint64, orderFlags uint8, enterTS uint64) error {
	return bk.applyOrderAddFull(orderID, 0, side, price, qty, orderFlags, enterTS)
}

// applyOrderAddFull is like applyOrderAdd but also stores the sourceID.
// Called by the engine layer which has the full message context.
func (bk *instrBook) applyOrderAddFull(orderID uint64, sourceID uint16, side uint8, price int64, qty uint64, orderFlags uint8, enterTS uint64) error {
	if isHidden(orderFlags) {
		bk.hiddenIDs.add(orderID)
		return nil
	}
	bk.live[orderID] = liveOrder{
		side:       side,
		sourceID:   sourceID,
		price:      price,
		remaining:  qty,
		orderFlags: orderFlags,
		enterTS:    enterTS,
	}
	if qty == 0 {
		bk.zeroed = append(bk.zeroed, orderID)
	}
	bk.removed.remove(orderID)
	return nil
}

// seed replaces the live set with the orders of a complete snapshot group, as a
// subscriber builds its book in market-by-order/spec.md, Cold Start step 5. A
// snapshot carries no Source ID, so a seeded order has Source ID 0, which
// FIELD.SOURCE_ID_CONSISTENCY reads as unknown. The removed, hidden and Trade ID
// windows stay: they record what the deltas did, and the snapshot does not
// contradict that.
func (bk *instrBook) seed(orders map[uint64]snapOrderRecord) {
	clear(bk.live)
	bk.zeroed = bk.zeroed[:0]
	for _, id := range slices.Sorted(maps.Keys(orders)) {
		o := orders[id]
		_ = bk.applyOrderAddFull(id, 0, o.side, o.price, o.qty, o.orderFlags, o.enterTS)
	}
}

// applyOrderExecute decrements the resting order's remaining qty by execQty and
// removes the order if fullFill is true or remaining reaches zero.
//
// Returns:
//   - errOverfill when execQty > remaining (non-hidden orders only).
//   - errFullFillDisagree when fullFill=true but execQty != remaining (non-hidden
//     orders only; this checks the disagreement BEFORE decrementing).
//   - nil on success.
//
// Hidden orders (in hiddenIDs but not in live) skip all quantity-conservation
// checks and return nil.
func (bk *instrBook) applyOrderExecute(orderID uint64, execQty uint64, fullFill bool) error {
	lo, live := bk.live[orderID]
	if !live {
		// Not in live — check hidden.
		if bk.hiddenIDs.has(orderID) {
			// Hidden: qty conservation is exempt; remove if full fill.
			if fullFill {
				bk.removed.add(orderID)
			}
			return nil
		}
		// Not live, not hidden — let the referential layer handle the dangling error.
		// No qty-conservation error to return here.
		return nil
	}

	if isHidden(lo.orderFlags) {
		// Hidden order in live map (shouldn't normally happen, but be safe).
		if fullFill {
			delete(bk.live, orderID)
			bk.removed.add(orderID)
		}
		return nil
	}

	// Quantity-conservation checks for non-hidden orders.
	// Even when an anomaly is detected, apply the lifecycle transition dictated by
	// fullFill so that subsequent operations see consistent state: a full-fill (even
	// a mismatched one) should move the order to removed, not leave it live.
	if execQty > lo.remaining {
		if fullFill {
			delete(bk.live, orderID)
			bk.removed.add(orderID)
		}
		return errOverfill
	}
	if fullFill && execQty != lo.remaining {
		// Remove even though the quantities disagree: the full-fill flag signals
		// the order is done, so keeping it live would corrupt downstream state.
		delete(bk.live, orderID)
		bk.removed.add(orderID)
		return errFullFillDisagree
	}

	lo.remaining -= execQty

	if fullFill || lo.remaining == 0 {
		delete(bk.live, orderID)
		bk.removed.add(orderID)
		return nil
	}

	bk.live[orderID] = lo
	if lo.remaining == 0 {
		bk.zeroed = append(bk.zeroed, orderID)
	}
	return nil
}

// applyOrderCancel removes the resting order from the live set.
// Hidden orders are handled by the referential layer; here we silently ignore an
// ID that is not in the live map (the referential layer will report dangling/after-
// removal as appropriate).
func (bk *instrBook) applyOrderCancel(orderID uint64) error {
	if _, live := bk.live[orderID]; live {
		delete(bk.live, orderID)
		bk.removed.add(orderID)
	}
	return nil
}

// Orders returns a snapshot copy of the live order book as a map of
// orderID → restingOrder. The caller may freely mutate the returned map without
// affecting the book's internal state.
func (bk *instrBook) Orders() map[uint64]restingOrder {
	snap := make(map[uint64]restingOrder, len(bk.live))
	for id, lo := range bk.live {
		snap[id] = restingOrder{
			side:       lo.side,
			price:      lo.price,
			remaining:  lo.remaining,
			orderFlags: lo.orderFlags,
			enterTS:    lo.enterTS,
		}
	}
	return snap
}

// bookState is the engine-level holder of per-(channel, instrument) book state.
type bookState struct {
	books map[instrTrackerKey]*instrBook
}

func newBookState() *bookState {
	return &bookState{
		books: make(map[instrTrackerKey]*instrBook),
	}
}

// book returns (lazily creating) the instrBook for (ch, instrID).
func (bs *bookState) book(ch uint8, instrID uint32) *instrBook {
	key := instrTrackerKey{ch, instrID}
	bk, ok := bs.books[key]
	if !ok {
		bk = newInstrBook()
		bs.books[key] = bk
	}
	return bk
}

// onResetCount wipes all per-instrument book state (era boundary).
func (bs *bookState) onResetCount() {
	clear(bs.books)
}

// onManifestBump prunes books for instruments removed from the manifest on channel ch.
func (bs *bookState) onManifestBump(ch uint8, survivingInstrIDs map[uint32]struct{}) {
	for key := range bs.books {
		if key.channelID != ch {
			continue
		}
		if _, ok := survivingInstrIDs[key.instrumentID]; !ok {
			delete(bs.books, key)
		}
	}
}

// mayHaveForgotten reports whether orderID can be an ID that removed or
// hiddenIDs evicted. The book then cannot tell an operation after removal, an
// operation on a hidden order and a dangling operation apart.
func (bk *instrBook) mayHaveForgotten(orderID uint64) bool {
	return bk.removed.mayHaveEvicted(orderID) || bk.hiddenIDs.mayHaveEvicted(orderID)
}

// onInstrumentReset clears the book for a single instrument.
func (bs *bookState) onInstrumentReset(ch uint8, instrID uint32) {
	key := instrTrackerKey{ch, instrID}
	delete(bs.books, key)
}

// isHidden returns true when OrderFlags bit 2 is set (hidden order).
// Hidden orders share order-ID slots by spec and are exempt from DUPLICATE_LIVE_ORDERADD.
func isHidden(orderFlags uint8) bool {
	return orderFlags&0x04 != 0
}

// isFullFill returns true when ExecFlags bit 0 is set (full fill / last partial).
func isFullFill(execFlags uint8) bool {
	return execFlags&0x01 != 0
}

// applyDelta changes the book as one OrderAdd, OrderCancel or OrderExecute
// changes it. checkMBORef applies each delta with it, and a seed or an audit
// applies the logged deltas after a snapshot with it, so both change a book in
// the same way.
//
// An OrderExecute or an OrderCancel for an order that is not live changes
// nothing. The referential checks report it.
func (bk *instrBook) applyDelta(m wire.Message) error {
	switch m.Type {
	case wire.TypeOrderAdd:
		return bk.applyOrderAddFull(orderAddOrderID(m), orderAddSourceID(m), orderAddSide(m),
			orderAddPrice(m), orderAddQuantity(m), orderAddOrderFlags(m), orderAddEnterTimestamp(m))
	case wire.TypeOrderCancel:
		return bk.applyOrderCancel(orderCancelOrderID(m))
	case wire.TypeOrderExecute:
		id := orderExecuteOrderID(m)
		if _, live := bk.live[id]; !live {
			return nil
		}
		return bk.applyOrderExecute(id, orderExecuteExecQuantity(m), isFullFill(orderExecuteExecFlags(m)))
	}
	return nil
}

// seedAndReplay replaces the live set with the orders of a snapshot group taken
// at per-instrument seq k, then applies the logged deltas after k. The result is
// the book at the last logged delta, built from the snapshot. The caller checks
// log.covers first.
//
// A seed also records, in each logged delta, whether its order is in the seeded
// book. The book was not trusted when the delta first arrived, so the record of
// that time can be wrong, and the oracle reads it (replayedSnapshot).
func (bk *instrBook) seedAndReplay(orders map[uint64]snapOrderRecord, k uint32, log *deltaLog) {
	bk.seed(orders)
	log.eachAfter(k, func(d *loggedDelta) {
		m := d.message()
		_, d.found = bk.live[deltaOrderID(m)]
		_ = bk.applyDelta(m)
	})
}

// maxDeltaLog limits the deltas that the log of one instrument keeps. A group
// close removes the deltas at or below its Last Instrument Seq, so the log of
// an instrument holds the deltas since its last snapshot group, and only a busy
// instrument reaches the limit. An entry is 60 bytes, so a full log is 240 KiB. Measured on a 30-minute capture of two
// busy feeds, 68,277 groups: when a group closed, the book was at most 1,878
// deltas past its Last Instrument Seq, 200 to 342 at the 99th percentile. It is
// a variable so that tests can use a small limit.
var maxDeltaLog = 4096

// maxLoggedBody is the largest delta body: an OrderExecute is 56 bytes, 52
// after its message header.
const maxLoggedBody = 52

// loggedDelta is one delta of a deltaLog: the message, its per-instrument seq,
// and whether its order was live in the book before the delta.
type loggedDelta struct {
	seq    uint32
	typ    uint8
	length uint8
	n      uint8
	found  bool
	body   [maxLoggedBody]byte
}

func (d *loggedDelta) message() wire.Message {
	return wire.Message{Type: d.typ, Length: d.length, Body: d.body[:d.n]}
}

// deltaLog keeps the most recent deltas of one instrument, with contiguous
// per-instrument seqs. market-by-order/spec.md, Cold Start steps 5 and 6, has a
// subscriber apply a snapshot group, then the deltas after its Last Instrument
// Seq (K). The engine applies deltas as they arrive, so on a busy instrument the
// book is past K when the group closes. The log keeps the deltas after K, and a
// seed or an audit applies them to the snapshot book.
//
// A delta with a seq that does not follow the last entry is not one more step of
// the same history. A later seq (a jump) starts a new log. An earlier or equal
// seq (a duplicate or a late delta) is not logged.
//
// The entries are a ring. It grows to maxDeltaLog entries, then each new entry
// replaces the oldest.
type deltaLog struct {
	buf   []loggedDelta
	start int // index in buf of the oldest entry
	n     int // number of entries
}

func (l *deltaLog) at(i int) *loggedDelta { return &l.buf[(l.start+i)%len(l.buf)] }

// record logs delta m with per-instrument seq seq. found is whether its order
// was live in the book before m.
func (l *deltaLog) record(seq uint32, m wire.Message, found bool) {
	if len(m.Body) > maxLoggedBody {
		l.clear()
		return
	}
	if l.n > 0 {
		last := l.at(l.n - 1).seq
		if seq <= last {
			return
		}
		if seq != last+1 {
			l.clear()
		}
	}
	if l.n == len(l.buf) {
		if len(l.buf) < maxDeltaLog {
			l.grow()
		} else {
			l.start = (l.start + 1) % len(l.buf)
			l.n--
		}
	}
	d := l.at(l.n)
	*d = loggedDelta{seq: seq, typ: m.Type, length: m.Length, n: uint8(len(m.Body)), found: found}
	copy(d.body[:], m.Body)
	l.n++
}

// grow doubles the ring, up to maxDeltaLog entries, with the entries oldest first.
func (l *deltaLog) grow() {
	nb := make([]loggedDelta, min(max(2*len(l.buf), 16), maxDeltaLog))
	for i := range l.n {
		nb[i] = *l.at(i)
	}
	l.buf, l.start = nb, 0
}

// covers reports whether the log holds every delta from k+1 to last, the
// per-instrument seq of the book. A book at k needs no delta.
func (l *deltaLog) covers(k, last uint32) bool {
	if last == k {
		return true
	}
	return last > k && l.n > 0 && l.at(0).seq <= k+1 && l.at(l.n-1).seq == last
}

// eachAfter calls fn for each logged delta with a seq above k, oldest first.
func (l *deltaLog) eachAfter(k uint32, fn func(*loggedDelta)) {
	for i := range l.n {
		if d := l.at(i); d.seq > k {
			fn(d)
		}
	}
}

// trimThrough removes the deltas at or below k. A later snapshot group of the
// instrument has a Last Instrument Seq of at least k, so it does not need them.
func (l *deltaLog) trimThrough(k uint32) {
	if l.n == 0 || l.at(0).seq > k {
		return
	}
	drop := min(int(k-l.at(0).seq)+1, l.n)
	l.start = (l.start + drop) % len(l.buf)
	l.n -= drop
}

func (l *deltaLog) clear() {
	l.start, l.n = 0, 0
}
