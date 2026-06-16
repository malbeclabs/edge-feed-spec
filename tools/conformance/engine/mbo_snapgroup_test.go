package engine

// mbo_snapgroup_test.go — Tests for Task 21: MBO snapshot-group accumulator rules.
//
// Rules exercised:
//   SNAP.BEGIN_ORDER_END_GROUPING         — interleaved instruments / missing Begin / missing End
//   SNAP.TOTAL_ORDERS_COUNT_MATCH         — over-count (always Violation) / under-count (Unverifiable on gap)
//   SNAP.END_FIELDS_MATCH_BEGIN           — End fields must equal Begin fields
//   SNAP.ORDER_SNAPSHOT_ID_MATCH          — SnapshotOrder snapshot id must match open group
//   SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID   — duplicate order id within a group
//   SNAP.EMPTY_BOOK_WELL_FORMED           — order arrived when TotalOrders==0
//   SNAP.ORDER_PRICE_BOUND                — negative price with priceBound ∈ {1,2}

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- Frame builders for snapshot-group tests ---

// buildSnapBeginFull builds a SnapshotBegin (0x20, 36 bytes) frame.
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:12] = Anchor Seq (u64 LE)
//	Body[12:16]= Total Orders (u32 LE)
//	Body[16:20]= Snapshot ID (u32 LE)
//	Body[20:24]= Last Instrument Seq (u32 LE)
//	Body[24:32]= padding
func buildSnapBeginFull(ch uint8, instrID uint32, anchorSeq uint64, totalOrders, snapshotID, lastInstrSeq uint32) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) {
			b.U32(instrID)      // body[0]  Instrument ID
			b.U64(anchorSeq)    // body[4]  Anchor Seq
			b.U32(totalOrders)  // body[12] Total Orders
			b.U32(snapshotID)   // body[16] Snapshot ID
			b.U32(lastInstrSeq) // body[20] Last Instrument Seq
			b.Pad(8)            // body[24] padding
		}).
		Bytes()
}

// buildSnapOrderFull builds a SnapshotOrder (0x21, 44 bytes) frame.
//
//	Body[0:4]  = Snapshot ID (u32 LE)
//	Body[4:12] = Order ID (u64 LE)
//	Body[12]   = Side (u8)
//	Body[13]   = Order Flags (u8)
//	Body[14:16]= padding
//	Body[16:24]= Enter Timestamp (u64 LE)
//	Body[24:32]= Price (i64 LE)
//	Body[32:40]= Quantity (u64 LE)
func buildSnapOrderFull(ch uint8, snapshotID uint32, orderID uint64, price int64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeSnapshotOrder, 44, func(b *wb.Body) {
			b.U32(snapshotID) // body[0]  Snapshot ID
			b.U64(orderID)    // body[4]  Order ID
			b.U8(0)           // body[12] Side=bid
			b.U8(0)           // body[13] Order Flags
			b.Pad(2)          // body[14] padding
			b.U64(1000)       // body[16] Enter Timestamp
			b.I64(price)      // body[24] Price
			b.U64(1)          // body[32] Quantity
		}).
		Bytes()
}

// buildSnapEndFull builds a SnapshotEnd (0x22, 20 bytes) frame.
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:12] = Anchor Seq (u64 LE)
//	Body[12:16]= Snapshot ID (u32 LE)
func buildSnapEndFull(ch uint8, instrID uint32, anchorSeq uint64, snapshotID uint32) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeSnapshotEnd, 20, func(b *wb.Body) {
			b.U32(instrID)    // body[0]  Instrument ID
			b.U64(anchorSeq)  // body[4]  Anchor Seq
			b.U32(snapshotID) // body[12] Snapshot ID
		}).
		Bytes()
}

// snapEntry creates a snapshot-port entry.
func snapEntry(raw []byte, seq uint64) streamEntry {
	return streamEntry{port: core.PortSnapshot, raw: raw, seq: seq}
}

// feedMktdataSeq feeds a raw frame to the engine on the mktdata port with the given seq.
func feedMktdataSeq(e *Engine, raw []byte, seq uint64) {
	f, sf := wire.Decode(raw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortMktData, sf)
}

// --- TestSnapGroupWellFormed: a complete Begin→N×Order→End passes silently ---

func TestSnapGroupWellFormed(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(200), uint32(1)
	e, ac := newMBOEngineW1()

	// Feed a few mktdata frames so highestMktdataSeq is set.
	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Well-formed group: Begin(total=2) → Order(id=1) → Order(id=2) → End.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 1001, 100), 2),
		snapEntry(buildSnapOrderFull(ch, snapID, 1002, 200), 3),
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 4),
	})

	groupRules := []string{
		"SNAP.BEGIN_ORDER_END_GROUPING",
		"SNAP.TOTAL_ORDERS_COUNT_MATCH",
		"SNAP.END_FIELDS_MATCH_BEGIN",
		"SNAP.ORDER_SNAPSHOT_ID_MATCH",
		"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID",
		"SNAP.EMPTY_BOOK_WELL_FORMED",
	}
	for _, ruleID := range groupRules {
		if hasViolation(ac, ruleID) {
			t.Errorf("well-formed group must not emit %s Violation", ruleID)
		}
	}
}

// --- TestSnapGroupEmptyBook: Begin(total=0) → End (no Orders) is well-formed ---

func TestSnapGroupEmptyBook(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(201), uint32(2)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Empty book: Begin(total=0) → End immediately.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 0, snapID, 1), 1),
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 2),
	})

	if hasViolation(ac, "SNAP.EMPTY_BOOK_WELL_FORMED") {
		t.Error("empty-book (TotalOrders=0) with no orders must not fire SNAP.EMPTY_BOOK_WELL_FORMED")
	}
	if hasViolation(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		t.Error("TotalOrders=0, received=0 must not fire SNAP.TOTAL_ORDERS_COUNT_MATCH")
	}
}

// --- TestSnapGroupInterleave: new Begin without End → SNAP.BEGIN_ORDER_END_GROUPING ---

func TestSnapGroupInterleave(t *testing.T) {
	const ch, instrID = uint8(1), uint32(202)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin group 1 then Begin group 2 without End in between.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, 10, 1), 1),
		// No End here — starts a new Begin, triggering interleave.
		snapEntry(buildSnapBeginFull(ch, instrID, 2, 1, 11, 2), 2),
	})

	if !hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		t.Error("expected SNAP.BEGIN_ORDER_END_GROUPING Violation for interleaved Begin without End")
	}
}

// --- TestSnapGroupOrderWithoutBegin: SnapshotOrder without a Begin → SNAP.BEGIN_ORDER_END_GROUPING ---

func TestSnapGroupOrderWithoutBegin(t *testing.T) {
	const ch = uint8(1)
	e, ac := newMBOEngineW1()

	// No Begin — feed a SnapshotOrder directly.
	runStream(e, []streamEntry{
		snapEntry(buildSnapOrderFull(ch, 99, 1001, 100), 1),
	})

	if !hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		t.Error("expected SNAP.BEGIN_ORDER_END_GROUPING Violation for SnapshotOrder without Begin")
	}
}

// --- TestSnapGroupEndWithoutBegin: SnapshotEnd without a Begin → SNAP.BEGIN_ORDER_END_GROUPING ---

func TestSnapGroupEndWithoutBegin(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(203), uint32(5)
	e, ac := newMBOEngineW1()

	// No Begin — feed a SnapshotEnd directly.
	runStream(e, []streamEntry{
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 1),
	})

	if !hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		t.Error("expected SNAP.BEGIN_ORDER_END_GROUPING Violation for SnapshotEnd without Begin")
	}
}

// --- TestSnapGroupOverCount: received > total → always Violation ---

func TestSnapGroupOverCount(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(204), uint32(20)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// total=1 but we send 2 orders.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 2001, 100), 2),
		snapEntry(buildSnapOrderFull(ch, snapID, 2002, 200), 3), // over-count
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 4),
	})

	if !hasViolation(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		t.Error("expected SNAP.TOTAL_ORDERS_COUNT_MATCH Violation for over-count")
	}
}

// --- TestSnapGroupOverCountAlwaysViolation: over-count stays Violation even on gap ---

func TestSnapGroupOverCountAlwaysViolation(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(205), uint32(21)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Snapshot port has a gap (seq 1 → seq 3), then over-count.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
		// skip snap seq 2 (gap)
		snapEntry(buildSnapOrderFull(ch, snapID, 3001, 100), 3),
		snapEntry(buildSnapOrderFull(ch, snapID, 3002, 200), 4), // over-count
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 5),
	})

	// Even with a gap, over-count must be Violation (not Unverifiable).
	for _, fn := range findingsFor(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		if fn.Status != core.Violation {
			t.Errorf("over-count must always be Violation even with snapshot-port gap, got %v", fn.Status)
		}
	}
	if !hasViolation(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		t.Error("expected SNAP.TOTAL_ORDERS_COUNT_MATCH Violation for over-count with gap")
	}
}

// --- TestSnapGroupUnderCount: received < total on gapless port → Violation ---

func TestSnapGroupUnderCount(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(206), uint32(30)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// total=3, only 1 order received, gapless snapshot port.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 3, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 4001, 100), 2),
		// Missing orders 2 and 3.
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 3),
	})

	if !hasViolation(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		t.Error("expected SNAP.TOTAL_ORDERS_COUNT_MATCH Violation for under-count on gapless port")
	}
}

// --- TestSnapGroupUnderCountUnverifiableOnGap: received < total with snapshot gap → Unverifiable ---

func TestSnapGroupUnderCountUnverifiableOnGap(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(207), uint32(31)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// total=3, only 1 order, snapshot port has a gap → Unverifiable.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 3, snapID, 1), 1),
		// skip snap seq 2 (gap makes the group dirty)
		snapEntry(buildSnapOrderFull(ch, snapID, 5001, 100), 3),
		// Missing orders 2 and 3 (could be in the gap).
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 4),
	})

	gotUnverifiable := false
	for _, fn := range findingsFor(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		if fn.Status == core.Violation {
			t.Errorf("under-count with snapshot-port gap must be Unverifiable, got Violation: %s", fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			gotUnverifiable = true
		}
	}
	if !gotUnverifiable {
		t.Error("expected at least one SNAP.TOTAL_ORDERS_COUNT_MATCH Unverifiable finding for under-count on gapped port")
	}
}

// --- TestSnapGroupEndFieldsMismatch: End's fields differ from Begin → SNAP.END_FIELDS_MATCH_BEGIN ---

func TestSnapGroupEndFieldsMismatch(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(208), uint32(40)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin anchor=1, snapID=40; End has wrong snapID=99.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 6001, 100), 2),
		// End with wrong snapshot ID.
		snapEntry(buildSnapEndFull(ch, instrID, 1, 99), 3),
	})

	if !hasViolation(ac, "SNAP.END_FIELDS_MATCH_BEGIN") {
		t.Error("expected SNAP.END_FIELDS_MATCH_BEGIN Violation when End snapshot ID differs from Begin")
	}
}

// --- TestSnapGroupEndAnchorMismatch: End has wrong anchor seq → SNAP.END_FIELDS_MATCH_BEGIN ---

func TestSnapGroupEndAnchorMismatch(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(209), uint32(41)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin anchor=1, End anchor=999.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 7001, 100), 2),
		// End with wrong anchor seq.
		snapEntry(buildSnapEndFull(ch, instrID, 999, snapID), 3),
	})

	if !hasViolation(ac, "SNAP.END_FIELDS_MATCH_BEGIN") {
		t.Error("expected SNAP.END_FIELDS_MATCH_BEGIN Violation when End anchor differs from Begin")
	}
}

// --- TestSnapGroupOrderSnapIDMismatch: SnapshotOrder snapID differs from open group ---

func TestSnapGroupOrderSnapIDMismatch(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(210), uint32(50)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin snapID=50, but SnapshotOrder carries snapID=99.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, 99 /*wrong snapID*/, 8001, 100), 2),
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 3),
	})

	if !hasViolation(ac, "SNAP.ORDER_SNAPSHOT_ID_MATCH") {
		t.Error("expected SNAP.ORDER_SNAPSHOT_ID_MATCH Violation when SnapshotOrder snapID != open group snapID")
	}
}

// --- TestSnapGroupDupOrderID: duplicate order id in group → SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID ---

func TestSnapGroupDupOrderID(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(211), uint32(60)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Send two orders with the same order ID.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 9001, 100), 2),
		snapEntry(buildSnapOrderFull(ch, snapID, 9001, 200), 3), // dup order ID
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 4),
	})

	if !hasViolation(ac, "SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID") {
		t.Error("expected SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID Violation for duplicate order id in group")
	}
}

// --- TestSnapGroupEmptyBookViolation: TotalOrders=0 but an order arrives ---

func TestSnapGroupEmptyBookViolation(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(212), uint32(70)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// TotalOrders=0 but an order arrives anyway.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 0, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 11001, 100), 2), // forbidden
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 3),
	})

	if !hasViolation(ac, "SNAP.EMPTY_BOOK_WELL_FORMED") {
		t.Error("expected SNAP.EMPTY_BOOK_WELL_FORMED Violation for order in zero-total group")
	}
}

// --- TestSnapGroupOrderPriceBound: negative price with priceBound ∈ {1,2} ---

func TestSnapGroupOrderPriceBound(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(213), uint32(80)

	t.Run("violation_negative_price_with_bound", func(t *testing.T) {
		e, ac := newMBOEngineW1()

		// Bootstrap refdata with priceBound=2 (Non-negative).
		reachedReadyMBO(e, ch, instrID, 2 /*priceBound*/, 1)

		feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 10)
		e.Flush()
		clearFindings(ac)

		// SnapshotOrder with negative price — priceBound=2 forbids it.
		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, instrID, 10, 1, snapID, 1), 1),
			snapEntry(buildSnapOrderFull(ch, snapID, 12001, -100 /*negative*/), 2),
			snapEntry(buildSnapEndFull(ch, instrID, 10, snapID), 3),
		})

		if !hasViolation(ac, "SNAP.ORDER_PRICE_BOUND") {
			t.Error("expected SNAP.ORDER_PRICE_BOUND Violation for negative price with priceBound=2")
		}
	})

	t.Run("no_violation_positive_price", func(t *testing.T) {
		e, ac := newMBOEngineW1()
		reachedReadyMBO(e, ch, instrID, 2 /*priceBound*/, 1)

		feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 10)
		e.Flush()
		clearFindings(ac)

		// SnapshotOrder with positive price — OK.
		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, instrID, 10, 1, snapID, 1), 1),
			snapEntry(buildSnapOrderFull(ch, snapID, 13001, 100 /*positive*/), 2),
			snapEntry(buildSnapEndFull(ch, instrID, 10, snapID), 3),
		})

		if hasViolation(ac, "SNAP.ORDER_PRICE_BOUND") {
			t.Error("positive price must not emit SNAP.ORDER_PRICE_BOUND")
		}
	})

	t.Run("no_violation_without_refdata", func(t *testing.T) {
		// Without refdata, priceBound is unknown — silent.
		e, ac := newMBOEngineW1()

		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, snapID, 1), 1),
			snapEntry(buildSnapOrderFull(ch, snapID, 14001, -999 /*negative*/), 2),
			snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 3),
		})

		if hasViolation(ac, "SNAP.ORDER_PRICE_BOUND") {
			t.Error("negative price without refdata must not emit SNAP.ORDER_PRICE_BOUND")
		}
	})
}

// --- TestSnapGroupInterleaveUnverifiable: interleave on a gapped snapshot port → Unverifiable ---

func TestSnapGroupInterleaveUnverifiable(t *testing.T) {
	const ch, instrID = uint8(1), uint32(214)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin group 1 at seq 1.
	// Skip seq 2 (gap — makes the group dirty).
	// Then Begin group 2 at seq 3 (interleave, but gapped → Unverifiable).
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, 10, 1), 1),
		// skip snap seq 2 — gap
		snapEntry(buildSnapBeginFull(ch, instrID, 2, 1, 11, 2), 3),
	})

	gotUnverifiable := false
	for _, fn := range findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		if fn.Status == core.Violation {
			t.Errorf("interleave with snapshot-port gap must be Unverifiable, got Violation: %s", fn.Detail)
		}
		if fn.Status == core.Unverifiable {
			gotUnverifiable = true
		}
	}
	if !gotUnverifiable {
		t.Error("expected at least one SNAP.BEGIN_ORDER_END_GROUPING Unverifiable finding for interleave on gapped port")
	}
}

// --- TestSnapGroupCoexistenceWithBeginCounterRules: existing Task-19 counter rules still fire ---

// Verify that the Task-19 SNAP.ANCHOR_IS_MKTDATA_SEQ rule still fires correctly
// alongside the new group accumulator.
func TestSnapGroupCoexistenceWithBeginCounterRules(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(215), uint32(90)
	e, ac := newMBOEngineW1()

	// Highest mktdata seq = 3.
	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 2), 2)
	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 3), 3)
	e.Flush()
	clearFindings(ac)

	// SnapshotBegin with anchor=100 (> 3) → SNAP.ANCHOR_IS_MKTDATA_SEQ must fire.
	// The group accumulator should also open properly (no group violation expected).
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 100, 1, snapID, 3), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 15001, 100), 2),
		snapEntry(buildSnapEndFull(ch, instrID, 100, snapID), 3),
	})

	if !hasViolation(ac, "SNAP.ANCHOR_IS_MKTDATA_SEQ") {
		t.Error("expected SNAP.ANCHOR_IS_MKTDATA_SEQ Violation for anchor > highest mktdata seq")
	}
	// Group rules must not fire spuriously.
	if hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		t.Error("SNAP.BEGIN_ORDER_END_GROUPING must not fire for a well-formed group")
	}
	if hasViolation(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH") {
		t.Error("SNAP.TOTAL_ORDERS_COUNT_MATCH must not fire for a count-matching group")
	}
}

// --- TestSnapGroupMultipleGroupsSequential: two sequential groups pass silently ---

func TestSnapGroupMultipleGroupsSequential(t *testing.T) {
	const ch, instrID = uint8(1), uint32(216)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Group 1: Begin(snapID=1) → Order → End.
	// Group 2: Begin(snapID=2) → Order → End.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 1, 1, 1), 1),
		snapEntry(buildSnapOrderFull(ch, 1, 20001, 100), 2),
		snapEntry(buildSnapEndFull(ch, instrID, 1, 1), 3),
		// Second group starts after the first is closed.
		snapEntry(buildSnapBeginFull(ch, instrID, 2, 1, 2, 2), 4),
		snapEntry(buildSnapOrderFull(ch, 2, 20002, 100), 5),
		snapEntry(buildSnapEndFull(ch, instrID, 2, 2), 6),
	})

	groupRules := []string{
		"SNAP.BEGIN_ORDER_END_GROUPING",
		"SNAP.TOTAL_ORDERS_COUNT_MATCH",
		"SNAP.END_FIELDS_MATCH_BEGIN",
		"SNAP.ORDER_SNAPSHOT_ID_MATCH",
		"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID",
	}
	for _, ruleID := range groupRules {
		if hasViolation(ac, ruleID) {
			t.Errorf("sequential groups must not emit %s Violation", ruleID)
		}
	}
}

// --- TestSnapGroupMissingEndAtEOF: Begin without End at end-of-run → SNAP.BEGIN_ORDER_END_GROUPING ---

func TestSnapGroupMissingEndAtEOF(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(217), uint32(99)
	e, ac := newMBOEngineW1()

	feedMktdataSeq(e, buildOrderAddFrame(ch, instrID, 1), 1)
	e.Flush()
	clearFindings(ac)

	// Begin a group but never close it — no End before EndRun.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 1), 1),
		snapEntry(buildSnapOrderFull(ch, snapID, 30001, 100), 2),
		// No SnapshotEnd — EOF.
	})
	// runStream calls e.Flush(); now call EndRun to trigger flushOpenSnaps.
	e.EndRun()

	if !hasViolation(ac, "SNAP.BEGIN_ORDER_END_GROUPING") {
		t.Error("expected SNAP.BEGIN_ORDER_END_GROUPING Violation for open group at end-of-run")
	}
}

// --- TestTransportCorruptionTaintsSnapPort: F2 — transport corruption on snap port
//     must set dirtyWindow so snapshot under-count is Unverifiable, not Violation ---

func TestTransportCorruptionTaintsSnapPort(t *testing.T) {
	const ch, instrID, snapID = uint8(1), uint32(218), uint32(100)
	e, ac := newMBOEngineW1()

	clearFindings(ac)

	// Build a complete snapshot group: Begin(total=2), one SnapshotOrder, End.
	// TotalOrders=2 but we only send 1 order → under-count.
	// We inject transport corruption on the snapshot port BEFORE the End arrives
	// by feeding a truncated datagram (too short to decode, Transport=true).
	// After corruption, dirtyWindow must be set on the snapshot port.

	// Snap seq 1: SnapshotBegin with TotalOrders=2.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, 1, 2, snapID, 0), 1),
	})

	// Snap seq 2: transport-corrupted frame (truncated, < FrameHeaderLen bytes).
	// This simulates a UDP datagram damaged in transit.
	truncated := []byte{0x01, 0x02} // too short — wire.Decode returns Transport=true
	{
		f, sf := wire.Decode(truncated, wire.MagicMBO)
		// sf must contain a Transport=true finding (FRAME.LENGTH_CONSISTENCY).
		hasTransport := false
		for _, s := range sf {
			if s.Transport {
				hasTransport = true
				break
			}
		}
		if !hasTransport {
			t.Fatal("test setup: truncated frame must produce a Transport=true StructFinding")
		}
		f.Header.Sequence = 2
		e.Process(f, core.PortSnapshot, sf)
	}
	e.Flush()

	clearFindings(ac)

	// Snap seq 3: SnapshotOrder (1 of the 2 declared).
	runStream(e, []streamEntry{
		snapEntry(buildSnapOrderFull(ch, snapID, 40001, 50), 3),
	})

	// Snap seq 4: SnapshotEnd — under-count (1 received, 2 declared).
	// Because the corruption at seq=2 set dirtyWindow=true, the under-count
	// must be Unverifiable, never Violation.
	runStream(e, []streamEntry{
		snapEntry(buildSnapEndFull(ch, instrID, 1, snapID), 4),
	})
	e.Flush()

	got := findingsFor(ac, "SNAP.TOTAL_ORDERS_COUNT_MATCH")
	if len(got) == 0 {
		t.Fatal("expected a SNAP.TOTAL_ORDERS_COUNT_MATCH under-count finding (Unverifiable), got none")
	}
	for _, fn := range got {
		if fn.Status == core.Violation {
			t.Errorf("SNAP.TOTAL_ORDERS_COUNT_MATCH under-count must be Unverifiable when transport corruption tainted the snapshot port, got Violation: %s", fn.Detail)
		}
		if fn.Status != core.Unverifiable {
			t.Errorf("expected Unverifiable, got status %v: %s", fn.Status, fn.Detail)
		}
	}
}
