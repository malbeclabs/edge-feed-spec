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
//	removedOrders map[uint64]struct{}    — orders that have been fully cancelled or filled
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
//	TRADE.EXEC_GROUPING — non-zero TradeID must have a matching OrderExecute in window
//
// Exemptions:
//   - Hidden orders (OrderFlags bit 2 == 1): not tracked in live/removed; their
//     IDs are recorded in hiddenIDs to prevent false dangling violations on
//     subsequent execute/cancel messages for the same ID.
//   - Trade with TradeID == 0: exempt from EXEC_GROUPING.

import "errors"

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
	removed map[uint64]struct{}
	// hiddenIDs holds the set of order IDs seen in hidden OrderAdd messages
	// (OrderFlags bit 2 = 1). Hidden orders share ID slots by spec and are not
	// tracked in live/removed, but execute/cancel for a hidden-add ID must not
	// be flagged as dangling.
	hiddenIDs map[uint64]struct{}
	// execTradeIDs holds the set of Trade IDs seen in OrderExecute messages
	// within the current gapless window. Used by TRADE.EXEC_GROUPING.
	execTradeIDs map[uint64]struct{}
}

func newInstrBook() *instrBook {
	return &instrBook{
		live:         make(map[uint64]liveOrder),
		removed:      make(map[uint64]struct{}),
		hiddenIDs:    make(map[uint64]struct{}),
		execTradeIDs: make(map[uint64]struct{}),
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
		bk.hiddenIDs[orderID] = struct{}{}
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
	delete(bk.removed, orderID)
	return nil
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
		if _, hidden := bk.hiddenIDs[orderID]; hidden {
			// Hidden: qty conservation is exempt; remove if full fill.
			if fullFill {
				bk.removed[orderID] = struct{}{}
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
			bk.removed[orderID] = struct{}{}
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
			bk.removed[orderID] = struct{}{}
		}
		return errOverfill
	}
	if fullFill && execQty != lo.remaining {
		// Remove even though the quantities disagree: the full-fill flag signals
		// the order is done, so keeping it live would corrupt downstream state.
		delete(bk.live, orderID)
		bk.removed[orderID] = struct{}{}
		return errFullFillDisagree
	}

	lo.remaining -= execQty

	if fullFill || lo.remaining == 0 {
		delete(bk.live, orderID)
		bk.removed[orderID] = struct{}{}
		return nil
	}

	bk.live[orderID] = lo
	return nil
}

// applyOrderCancel removes the resting order from the live set.
// Hidden orders are handled by the referential layer; here we silently ignore an
// ID that is not in the live map (the referential layer will report dangling/after-
// removal as appropriate).
func (bk *instrBook) applyOrderCancel(orderID uint64) error {
	if _, live := bk.live[orderID]; live {
		delete(bk.live, orderID)
		bk.removed[orderID] = struct{}{}
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
