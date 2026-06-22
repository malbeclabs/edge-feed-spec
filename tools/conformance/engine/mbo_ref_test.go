package engine

// mbo_ref_test.go — Tests for Task 20: MBO order-id set + referential rules.
//
// Rules tested:
//   REF.EXEC_DANGLING_ORDER      — execute on never-added order id
//   REF.CANCEL_DANGLING_ORDER    — cancel on never-added order id
//   REF.DUPLICATE_LIVE_ORDERADD  — add reusing a live order id
//   REF.OPERATION_AFTER_REMOVAL  — execute/cancel on already-removed order
//   REF.SIDE_PRICE_CONSISTENCY   — aggressor side vs resting side mismatch
//   FIELD.SOURCE_ID_CONSISTENCY  — source id drift across lifecycle
//   TRADE.EXEC_GROUPING          — trade with non-zero id and no matching execute
//   FIELD.ORDERADD_PRICE_BOUND   — orderadd price < 0 when priceBound != 0
//   REF.EXEC_PRICE_BOUND         — execute exec-price < 0 when priceBound != 0
//
// Gating: all referential rules are Unverifiable when gateConsumer is false
// (preceding per-instrument gap, no refdata, or mktdata channel gap).

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- Frame builders for referential tests ---

// buildOrderAddFull builds an OrderAdd frame with explicit orderID, side, sourceID, price, flags.
//
// OrderAdd is 52 bytes total (4 header + 48 body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6]    = Side (u8)
//	Body[7]    = Order Flags (u8)
//	Body[8:12] = Per-Instrument Seq (u32 LE)
//	Body[12:20]= Order ID (u64 LE)
//	Body[20:28]= Enter Timestamp (u64 LE)
//	Body[28:36]= Price (i64 LE)
//	Body[36:44]= Quantity (u64 LE)
//	Body[44:48]= Reserved
func buildOrderAddFull(ch uint8, instrID uint32, perSeq uint32, orderID uint64, side uint8, sourceID uint16, price int64, flags uint8) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID)  // body[0]  Instrument ID
			b.U16(sourceID) // body[4]  Source ID
			b.U8(side)      // body[6]  Side
			b.U8(flags)     // body[7]  Order Flags
			b.U32(perSeq)   // body[8]  Per-Instrument Seq
			b.U64(orderID)  // body[12] Order ID
			b.U64(1000)     // body[20] Enter Timestamp
			b.I64(price)    // body[28] Price
			b.U64(1)        // body[36] Quantity
			b.Pad(4)        // body[44] Reserved
		}).
		Bytes()
}

// buildOrderCancelFull builds an OrderCancel frame.
//
// OrderCancel is 32 bytes total (4 header + 28 body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6:8]  = Reserved
//	Body[8:12] = Per-Instrument Seq (u32 LE)
//	Body[12:20]= Order ID (u64 LE)
//	Body[20:28]= Reserved
func buildOrderCancelFull(ch uint8, instrID uint32, perSeq uint32, orderID uint64, sourceID uint16) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderCancel, 32, func(b *wb.Body) {
			b.U32(instrID)  // body[0]  Instrument ID
			b.U16(sourceID) // body[4]  Source ID
			b.Pad(2)        // body[6]  Reserved
			b.U32(perSeq)   // body[8]  Per-Instrument Seq
			b.U64(orderID)  // body[12] Order ID
			b.U64(0)        // body[20] Reserved
		}).
		Bytes()
}

// buildOrderExecuteFull builds an OrderExecute frame.
//
// OrderExecute is 56 bytes total (4 header + 52 body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6]    = Aggressor Side (u8)
//	Body[7]    = Exec Flags (u8)  bit0=full-fill
//	Body[8:12] = Per-Instrument Seq (u32 LE)
//	Body[12:20]= Order ID (u64 LE)
//	Body[20:28]= Trade ID (u64 LE)
//	Body[28:36]= Reserved (u64)
//	Body[36:44]= Exec Price (i64 LE)
//	Body[44:52]= Exec Quantity (u64 LE)
func buildOrderExecuteFull(ch uint8, instrID uint32, perSeq uint32, orderID uint64, aggressorSide uint8, execFlags uint8, tradeID uint64, execPrice int64, sourceID uint16) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderExecute, 56, func(b *wb.Body) {
			b.U32(instrID)      // body[0]  Instrument ID
			b.U16(sourceID)     // body[4]  Source ID
			b.U8(aggressorSide) // body[6]  Aggressor Side
			b.U8(execFlags)     // body[7]  Exec Flags
			b.U32(perSeq)       // body[8]  Per-Instrument Seq
			b.U64(orderID)      // body[12] Order ID
			b.U64(tradeID)      // body[20] Trade ID
			b.U64(0)            // body[28] Reserved
			b.I64(execPrice)    // body[36] Exec Price
			b.U64(1)            // body[44] Exec Quantity
		}).
		Bytes()
}

// buildTradeFull builds a Trade (0x04) frame.
//
// Trade body is 48 bytes (spec layout from marketbyorder-parser):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:6]  = Source ID (u16 LE)
//	Body[6]    = Aggressor Side (u8)
//	Body[7]    = Trade Flags (u8)
//	Body[8:16] = Source Timestamp (i64 LE, nanoseconds — NOT per-instrument seq)
//	Body[16:24]= Trade Price Raw (i64 LE)
//	Body[24:32]= Trade Qty Raw (u64 LE)
//	Body[32:40]= Trade ID (u64 LE)
//	Body[40:48]= Cumulative Volume Raw (u64 LE)
//
// The fixture encodes 0 for all fields except InstrumentID and TradeID.
// perSeq is unused (Trade has no per-instrument sequence number) and kept only
// for signature compatibility with test helpers that track market-data sequences.
func buildTradeFull(ch uint8, instrID uint32, _ uint32, tradeID uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeTrade, 52, func(b *wb.Body) {
			b.U32(instrID) // body[0:4]   Instrument ID
			b.U16(0)       // body[4:6]   Source ID
			b.U8(0)        // body[6]     Aggressor Side
			b.U8(0)        // body[7]     Trade Flags
			b.U64(0)       // body[8:16]  Source Timestamp (nanoseconds)
			b.U64(0)       // body[16:24] Trade Price Raw
			b.U64(0)       // body[24:32] Trade Qty Raw
			b.U64(tradeID) // body[32:40] Trade ID
			b.U64(0)       // body[40:48] Cumulative Volume Raw
		}).
		Bytes()
}

// reachedReadyMBO bootstraps MBO refdata for channel ch with one instrument,
// registering it with the given priceBound.
// Returns the next refdata sequence to use.
func reachedReadyMBO(e *Engine, ch uint8, instrID uint32, priceBound uint8, startSeq uint64) uint64 {
	seq := startSeq
	// ManifestSummary: valid=1, manifestSeq=1, count=1.
	rawMf := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	fMf, sfMf := wire.Decode(rawMf, wire.MagicMBO)
	fMf.Header.Sequence = seq
	e.Process(fMf, core.PortRefData, sfMf)
	seq++

	// InstrumentDefinition (80-byte MBO): priceBound at body[73].
	rawDef := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrID)   // body[0]  Instrument ID
			b.Pad(69)        // body[4..72] opaque
			b.U8(priceBound) // body[73] Price Bound
			b.U16(1)         // body[74] Manifest Seq = 1
			b.Pad(2)         // body[76] reserved → total 76 body bytes → 80-byte msg
		}).
		Bytes()
	fDef, sfDef := wire.Decode(rawDef, wire.MagicMBO)
	fDef.Header.Sequence = seq
	e.Process(fDef, core.PortRefData, sfDef)
	seq++

	// Second ManifestSummary to close cycle.
	rawMf2 := wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	fMf2, sfMf2 := wire.Decode(rawMf2, wire.MagicMBO)
	fMf2.Header.Sequence = seq
	e.Process(fMf2, core.PortRefData, sfMf2)
	seq++
	return seq
}

// runMktdataSeq feeds a raw frame to the engine on the mktdata port with the given seq.
func runMktdataSeq(e *Engine, raw []byte, seq uint64) {
	f, sf := wire.Decode(raw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortMktData, sf)
}

// seedGaplessHistory feeds a series of OrderAdd frames (perInstrSeq 1..n) to
// establish a gapless per-instrument history AND bootstraps refdata for the instrument.
// Returns the next mktdata seq to use.
// These frames use high orderIDs (90001+) that won't conflict with test case orderIDs.
func seedGaplessHistory(e *Engine, ch uint8, instrID uint32, n uint32, startMktSeq uint64) uint64 {
	// Bootstrap refdata so gateConsumer condition 1 (ready) is true.
	reachedReadyMBO(e, ch, instrID, 0 /*priceBound=0*/, 1)

	seq := startMktSeq
	for i := uint32(1); i <= n; i++ {
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, i, uint64(90000+i), 0, 1, 100, 0), seq)
		seq++
	}
	e.Flush()
	return seq
}

// --- Tests ---

// TestExecDanglingOrder: OrderExecute for a never-added order id → REF.EXEC_DANGLING_ORDER.
// Must be Unverifiable when a preceding per-instrument gap exists.
func TestExecDanglingOrder(t *testing.T) {
	const ch, instrID = uint8(1), uint32(100)

	t.Run("violation_gapless", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Seed gapless history so gateConsumer returns true on next message.
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Execute on unknown order id — gapless history → Violation.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 4, 99, 1, 0, 0, 100, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.EXEC_DANGLING_ORDER") {
			t.Error("expected REF.EXEC_DANGLING_ORDER Violation on gapless history")
		}
	})

	t.Run("unverifiable_per_instr_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed seq 1 then skip seq 2 (per-instrument gap 1→3).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 90001, 0, 1, 100, 0), 1)
		// Per-instrument gap: jump from 1 to 3.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 3, 99, 1, 0, 0, 100, 1), 2)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.EXEC_DANGLING_ORDER") {
			if fn.Status == core.Violation {
				t.Errorf("per-instrument gap must produce Unverifiable, got Violation: %s", fn.Detail)
			}
		}
	})

	t.Run("unverifiable_mktdata_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Feed perInstrSeq=1, then skip mktdata seq 2, feed perInstrSeq=2.
		// gateDetector returns false because mktdata channel has a gap.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 90001, 0, 1, 100, 0), 1)
		// Skip mktdata seq=2; jump to seq=3 to set dirtyWindow.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 2, 99, 1, 0, 0, 100, 1), 3)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.EXEC_DANGLING_ORDER") {
			if fn.Status == core.Violation {
				t.Errorf("mktdata gap must produce Unverifiable, got Violation: %s", fn.Detail)
			}
		}
	})
}

// TestCancelDanglingOrder: OrderCancel for a never-added order id → REF.CANCEL_DANGLING_ORDER.
func TestCancelDanglingOrder(t *testing.T) {
	const ch, instrID = uint8(1), uint32(101)

	t.Run("violation_gapless", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 4, 99, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.CANCEL_DANGLING_ORDER") {
			t.Error("expected REF.CANCEL_DANGLING_ORDER Violation on gapless history")
		}
	})

	t.Run("unverifiable_per_instr_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 90001, 0, 1, 100, 0), 1)
		// Per-instrument gap: jump from 1 to 3.
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 3, 99, 1), 2)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.CANCEL_DANGLING_ORDER") {
			if fn.Status == core.Violation {
				t.Errorf("per-instrument gap must produce Unverifiable: %s", fn.Detail)
			}
		}
	})
}

// TestDuplicateLiveOrderAdd: OrderAdd reusing a live order id → REF.DUPLICATE_LIVE_ORDERADD.
func TestDuplicateLiveOrderAdd(t *testing.T) {
	const ch, instrID = uint8(1), uint32(102)
	const orderID = uint64(42)

	t.Run("violation_gapless", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add orderID=42 (new, live after this).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Add same orderID=42 again → duplicate live.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 5, orderID, 0, 1, 100, 0), seq)
		e.Flush()

		if !hasViolation(ac, "REF.DUPLICATE_LIVE_ORDERADD") {
			t.Error("expected REF.DUPLICATE_LIVE_ORDERADD Violation")
		}
	})

	t.Run("hidden_order_exempt", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add orderID=42 with OrderFlags bit2 set (hidden).
		const hiddenFlags = uint8(0x04)
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, hiddenFlags), seq)
		seq++
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 5, orderID, 0, 1, 100, hiddenFlags), seq)
		e.Flush()

		if hasViolation(ac, "REF.DUPLICATE_LIVE_ORDERADD") {
			t.Error("hidden orders must not trigger REF.DUPLICATE_LIVE_ORDERADD")
		}
	})

	t.Run("unverifiable_per_instr_gap", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// First add establishes the order.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, orderID, 0, 1, 100, 0), 1)
		// Per-instrument gap: jump from 1 to 3.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 3, orderID, 0, 1, 100, 0), 2)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.DUPLICATE_LIVE_ORDERADD") {
			if fn.Status == core.Violation {
				t.Errorf("per-instrument gap must produce Unverifiable: %s", fn.Detail)
			}
		}
	})
}

// TestHiddenOrderCollision: execute/cancel for a hidden-order ID must not produce
// false-positive REF.* violations even when the same ID was previously used by a
// visible order (visible cancel → removed set, then hidden add reuses the slot).
func TestHiddenOrderCollision(t *testing.T) {
	const ch, instrID = uint8(1), uint32(102)
	const orderID = uint64(77)
	const hiddenFlags = uint8(0x04)

	// Case 1: visible add → visible cancel → hidden add → execute for hidden order.
	// The execute targets the same ID that is now in the removed set (from the visible
	// cancel) AND in hiddenIDs. Should NOT emit REF.OPERATION_AFTER_REMOVAL.
	t.Run("hidden_execute_after_visible_removed", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Visible add then cancel → ID goes to removed set.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, 0), seq)
		seq++
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, orderID, 1), seq)
		seq++
		clearFindings(ac)

		// Hidden add with same ID → tainted; execute for that ID.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 6, orderID, 0, 1, 100, hiddenFlags), seq)
		seq++
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 7, orderID, 2, 0x01, 0, 50, 1), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.OPERATION_AFTER_REMOVAL") {
			t.Errorf("hidden-order execute after visible removal must not emit REF.OPERATION_AFTER_REMOVAL: %s", fn.Detail)
		}
		for _, fn := range findingsFor(ac, "REF.EXEC_DANGLING_ORDER") {
			t.Errorf("hidden-order execute must not emit REF.EXEC_DANGLING_ORDER: %s", fn.Detail)
		}
	})

	// Case 2: visible add → visible cancel → hidden add → cancel for hidden order.
	// Should NOT emit REF.OPERATION_AFTER_REMOVAL or REF.CANCEL_DANGLING_ORDER.
	t.Run("hidden_cancel_after_visible_removed", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Visible add then cancel → ID goes to removed set.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, 0), seq)
		seq++
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, orderID, 1), seq)
		seq++
		clearFindings(ac)

		// Hidden add with same ID → tainted; cancel for that ID.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 6, orderID, 0, 1, 100, hiddenFlags), seq)
		seq++
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 7, orderID, 1), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.OPERATION_AFTER_REMOVAL") {
			t.Errorf("hidden-order cancel after visible removal must not emit REF.OPERATION_AFTER_REMOVAL: %s", fn.Detail)
		}
		for _, fn := range findingsFor(ac, "REF.CANCEL_DANGLING_ORDER") {
			t.Errorf("hidden-order cancel must not emit REF.CANCEL_DANGLING_ORDER: %s", fn.Detail)
		}
	})

	// Case 3: hidden add → visible add → visible full-fill execute.
	// The visible live-order lifecycle must NOT be suppressed by the prior hidden add.
	// Specifically: the execute must remove the visible order from live, and a second
	// visible add of the same ID must NOT emit REF.DUPLICATE_LIVE_ORDERADD.
	t.Run("visible_lifecycle_not_suppressed_by_hidden_add", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Hidden add → taint the ID.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, hiddenFlags), seq)
		seq++
		// Visible add of same ID (allowed by spec since hidden slot != visible slot).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 5, orderID, 1, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Full-fill execute of the visible order (buy aggressor=1 hits resting ask side=1 → ok).
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 6, orderID, 1, 0x01, 0, 50, 1), seq)
		seq++
		e.Flush()

		// The visible execute must not produce REF.EXEC_DANGLING_ORDER (order was live).
		for _, fn := range findingsFor(ac, "REF.EXEC_DANGLING_ORDER") {
			t.Errorf("visible order execute must not emit REF.EXEC_DANGLING_ORDER: %s", fn.Detail)
		}
		clearFindings(ac)

		// After the full-fill, a second visible add of the same ID is a fresh order —
		// must NOT emit REF.DUPLICATE_LIVE_ORDERADD (order was moved to removed).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 7, orderID, 1, 1, 100, 0), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "REF.DUPLICATE_LIVE_ORDERADD") {
			t.Errorf("visible add after full-fill must not emit REF.DUPLICATE_LIVE_ORDERADD: %s", fn.Detail)
		}
	})
}

// TestOperationAfterRemoval: execute/cancel on an already-removed order id.
func TestOperationAfterRemoval(t *testing.T) {
	const ch, instrID = uint8(1), uint32(103)
	const orderID = uint64(55)

	t.Run("cancel_after_cancel_violation", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add orderID=55.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 1, 100, 0), seq)
		seq++
		// Cancel it — moves to removed.
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, orderID, 1), seq)
		seq++
		clearFindings(ac)

		// Cancel again → operation after removal.
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 6, orderID, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.OPERATION_AFTER_REMOVAL") {
			t.Error("expected REF.OPERATION_AFTER_REMOVAL Violation on cancel after cancel")
		}
	})

	t.Run("execute_after_full_fill_violation", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add orderID=55.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 1, 1, 100, 0), seq)
		seq++
		// Full-fill execute (ExecFlags bit0=1) — moves to removed.
		const fullFillFlags = uint8(0x01)
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID, 1, fullFillFlags, 0, 100, 1), seq)
		seq++
		clearFindings(ac)

		// Execute again → operation after removal.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 6, orderID, 1, 0, 0, 100, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.OPERATION_AFTER_REMOVAL") {
			t.Error("expected REF.OPERATION_AFTER_REMOVAL Violation on execute after full-fill")
		}
	})
}

// TestSidePriceConsistency: aggressor side vs resting side mismatch.
func TestSidePriceConsistency(t *testing.T) {
	const ch, instrID = uint8(1), uint32(104)
	const orderID = uint64(77)

	// Resting order on the BID (side=0). A buy aggressor (aggressorSide=1) hits asks (side=1),
	// not bids. So buy aggressor + bid resting = mismatch.
	t.Run("violation_buy_aggressor_hits_bid", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add resting order on the BID (side=0).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0 /*bid*/, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Execute with buy aggressor (1) — should hit ask (side=1), not bid (side=0).
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID, 1 /*buy aggressor*/, 0, 0, 100, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.SIDE_PRICE_CONSISTENCY") {
			t.Error("expected REF.SIDE_PRICE_CONSISTENCY Violation: buy aggressor vs bid resting")
		}
	})

	// Resting order on the ASK (side=1). A sell aggressor (aggressorSide=2) hits bids (side=0),
	// not asks. So sell aggressor + ask resting = mismatch.
	t.Run("violation_sell_aggressor_hits_ask", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID2 = uint64(78)
		// Add resting order on the ASK (side=1).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID2, 1 /*ask*/, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Execute with sell aggressor (2) — should hit bid (side=0), not ask (side=1).
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID2, 2 /*sell aggressor*/, 0, 0, 100, 1), seq)
		e.Flush()

		if !hasViolation(ac, "REF.SIDE_PRICE_CONSISTENCY") {
			t.Error("expected REF.SIDE_PRICE_CONSISTENCY Violation: sell aggressor vs ask resting")
		}
	})

	t.Run("conformant_buy_aggressor_hits_ask", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID3 = uint64(79)
		// Add resting order on the ASK (side=1).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID3, 1 /*ask*/, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Execute with buy aggressor (1) hitting ask (side=1) — correct.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID3, 1 /*buy aggressor*/, 0, 0, 100, 1), seq)
		e.Flush()

		if hasViolation(ac, "REF.SIDE_PRICE_CONSISTENCY") {
			t.Error("buy aggressor hitting ask must not emit REF.SIDE_PRICE_CONSISTENCY")
		}
	})

	t.Run("unknown_aggressor_skip", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID4 = uint64(80)
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID4, 0, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Aggressor side=0 (unknown) → skip the check.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID4, 0 /*unknown*/, 0, 0, 100, 1), seq)
		e.Flush()

		if hasViolation(ac, "REF.SIDE_PRICE_CONSISTENCY") {
			t.Error("unknown aggressor side must not emit REF.SIDE_PRICE_CONSISTENCY")
		}
	})
}

// TestSourceIDConsistency: sourceID must not drift across lifecycle.
func TestSourceIDConsistency(t *testing.T) {
	const ch, instrID = uint8(1), uint32(105)
	const orderID = uint64(88)

	t.Run("violation_source_id_drift_on_cancel", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Add with sourceID=10.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 0, 10, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Cancel with sourceID=20 (different from 10).
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, orderID, 20), seq)
		e.Flush()

		found := false
		for _, fn := range findingsFor(ac, "FIELD.SOURCE_ID_CONSISTENCY") {
			found = true
			_ = fn
		}
		if !found {
			t.Error("expected FIELD.SOURCE_ID_CONSISTENCY finding on sourceID drift during cancel")
		}
	})

	t.Run("conformant_same_source_id", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		const orderID2 = uint64(89)
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID2, 0, 10, 100, 0), seq)
		seq++
		clearFindings(ac)

		// Cancel with same sourceID=10.
		runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, orderID2, 10), seq)
		e.Flush()

		if hasViolation(ac, "FIELD.SOURCE_ID_CONSISTENCY") {
			t.Error("same sourceID must not emit FIELD.SOURCE_ID_CONSISTENCY")
		}
	})
}

// TestTradeExecGrouping: Trade with non-zero TradeID must have a matching OrderExecute.
func TestTradeExecGrouping(t *testing.T) {
	const ch, instrID = uint8(1), uint32(106)

	t.Run("violation_no_matching_execute", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Trade with non-zero TradeID=999 but no OrderExecute with that TradeID.
		runMktdataSeq(e, buildTradeFull(ch, instrID, 4, 999), seq)
		e.Flush()

		found := false
		for _, fn := range findingsFor(ac, "TRADE.EXEC_GROUPING") {
			found = true
			_ = fn
		}
		if !found {
			t.Error("expected TRADE.EXEC_GROUPING finding when no matching OrderExecute")
		}
	})

	t.Run("conformant_matching_execute", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)

		const orderID = uint64(42)
		// Add an order first.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, orderID, 1, 1, 100, 0), seq)
		seq++
		clearFindings(ac)

		// OrderExecute with tradeID=777.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 5, orderID, 1, 0, 777, 100, 1), seq)
		seq++
		// Trade with same tradeID=777 → should match.
		runMktdataSeq(e, buildTradeFull(ch, instrID, 6, 777), seq)
		e.Flush()

		if hasViolation(ac, "TRADE.EXEC_GROUPING") {
			t.Error("Trade with matching OrderExecute must not emit TRADE.EXEC_GROUPING Violation")
		}
	})

	t.Run("trade_id_zero_exempt", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		seq := seedGaplessHistory(e, ch, instrID, 3, 1)
		clearFindings(ac)

		// Trade with TradeID=0 → exempt.
		runMktdataSeq(e, buildTradeFull(ch, instrID, 4, 0), seq)
		e.Flush()

		for _, fn := range findingsFor(ac, "TRADE.EXEC_GROUPING") {
			t.Errorf("TradeID=0 must be exempt from TRADE.EXEC_GROUPING: %v", fn.Detail)
		}
	})

	t.Run("unverifiable_no_refdata", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// No refdata set up: gated=false because refdata is nil.
		// Trade with non-zero TradeID=999 and no matching OrderExecute.
		// Should be Unverifiable (not Violation) because book is not trusted.
		runMktdataSeq(e, buildTradeFull(ch, instrID, 0, 999), 1)
		e.Flush()

		for _, fn := range findingsFor(ac, "TRADE.EXEC_GROUPING") {
			if fn.Status == core.Violation {
				t.Errorf("no-refdata must produce Unverifiable, got Violation: %s", fn.Detail)
			}
		}
	})

	t.Run("unverifiable_book_not_trusted", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// Set up refdata but don't seed gapless history (book is not trusted).
		reachedReadyMBO(e, ch, instrID, 0, 1)
		clearFindings(ac)

		// Send an OrderAdd with perSeq=5 (cold start — book not trusted since seq!=1).
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 5, 90001, 0, 1, 100, 0), 100)
		// Trade with non-zero TradeID=999 and no matching OrderExecute.
		// bookTrusted=false because first delta was not seq=1 → gated=false.
		runMktdataSeq(e, buildTradeFull(ch, instrID, 0, 999), 101)
		e.Flush()

		for _, fn := range findingsFor(ac, "TRADE.EXEC_GROUPING") {
			if fn.Status == core.Violation {
				t.Errorf("untrusted book must produce Unverifiable, got Violation: %s", fn.Detail)
			}
		}
	})
}

// TestOrderAddPriceBound: FIELD.ORDERADD_PRICE_BOUND fires when price < 0 and priceBound != 0.
func TestOrderAddPriceBound(t *testing.T) {
	const ch, instrID = uint8(1), uint32(110)

	t.Run("violation_negative_price_bound1", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 1 /*priceBound=1*/, 1)
		clearFindings(ac)

		// OrderAdd with negative price.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 42, 0, 1, -100, 0), 100)
		e.Flush()

		if !hasViolation(ac, "FIELD.ORDERADD_PRICE_BOUND") {
			t.Error("expected FIELD.ORDERADD_PRICE_BOUND Violation for negative price with priceBound=1")
		}
	})

	t.Run("violation_negative_price_bound2", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 2 /*priceBound=2*/, 1)
		clearFindings(ac)

		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 42, 0, 1, -50, 0), 100)
		e.Flush()

		if !hasViolation(ac, "FIELD.ORDERADD_PRICE_BOUND") {
			t.Error("expected FIELD.ORDERADD_PRICE_BOUND Violation for negative price with priceBound=2")
		}
	})

	t.Run("silent_before_ready", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		// No refdata setup — engine.refdata is nil.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 42, 0, 1, -100, 0), 1)
		e.Flush()

		if hasViolation(ac, "FIELD.ORDERADD_PRICE_BOUND") {
			t.Error("FIELD.ORDERADD_PRICE_BOUND must be silent before refdata is ready")
		}
	})

	t.Run("conformant_positive_price", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 1, 1)
		clearFindings(ac)

		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 42, 0, 1, 100, 0), 100)
		e.Flush()

		if hasViolation(ac, "FIELD.ORDERADD_PRICE_BOUND") {
			t.Error("positive price must not emit FIELD.ORDERADD_PRICE_BOUND")
		}
	})

	t.Run("silent_priceBound0", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 0 /*priceBound=0*/, 1)
		clearFindings(ac)

		// Negative price but priceBound=0 → no constraint.
		runMktdataSeq(e, buildOrderAddFull(ch, instrID, 1, 42, 0, 1, -100, 0), 100)
		e.Flush()

		if hasViolation(ac, "FIELD.ORDERADD_PRICE_BOUND") {
			t.Error("priceBound=0 must not emit FIELD.ORDERADD_PRICE_BOUND")
		}
	})
}

// TestExecPriceBound: REF.EXEC_PRICE_BOUND fires when exec price < 0 and priceBound != 0.
func TestExecPriceBound(t *testing.T) {
	const ch, instrID = uint8(1), uint32(111)

	t.Run("violation_negative_exec_price", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 2 /*priceBound=2*/, 1)
		clearFindings(ac)

		// Execute with negative exec price.
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 1, 99, 0, 0, 0, -50, 1), 100)
		e.Flush()

		if !hasViolation(ac, "REF.EXEC_PRICE_BOUND") {
			t.Error("expected REF.EXEC_PRICE_BOUND Violation for negative exec price with priceBound=2")
		}
	})

	t.Run("silent_before_ready", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 1, 99, 0, 0, 0, -50, 1), 1)
		e.Flush()

		if hasViolation(ac, "REF.EXEC_PRICE_BOUND") {
			t.Error("REF.EXEC_PRICE_BOUND must be silent before refdata is ready")
		}
	})

	t.Run("conformant_positive_exec_price", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 1, 1)
		clearFindings(ac)

		runMktdataSeq(e, buildOrderExecuteFull(ch, instrID, 1, 99, 0, 0, 0, 100, 1), 100)
		e.Flush()

		if hasViolation(ac, "REF.EXEC_PRICE_BOUND") {
			t.Error("positive exec price must not emit REF.EXEC_PRICE_BOUND")
		}
	})
}
