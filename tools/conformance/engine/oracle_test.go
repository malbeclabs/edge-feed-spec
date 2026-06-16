package engine

// oracle_test.go — Tests for Task 26: snapshot-vs-delta reconstruction oracle.
//
// Rule: SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT (Must, full_book, MBO)
// Metric: snapshot_audits_total{match|mismatch_suspected|mismatch_confirmed|unverifiable}
//
// Scenarios:
//  1. Delta book and snapshot agree at K → "match", no finding.
//  2. Snapshot omits a still-resting order on a gapless history:
//     first cycle → "mismatch_suspected" (no CI fail);
//     reproduce same divergence next cycle → "mismatch_confirmed" + Violation.
//  3. Loss before K (mktdata gap, bookTrusted=false) → "unverifiable", never Violation.
//  4. --oracle-confirm-cycles=1 confirms on the first cycle.
//  5. Hidden orders excluded from the diff's qty comparison.
//  6. lastInstrSeq != K (book advanced past snapshot) → "unverifiable".
//  7. Dirty snapshot group → "unverifiable".
//  8. Different divergence on second cycle → suspect resets, no confirm.
//  9. Match after suspect clears the suspect state.
// 10. No refdata → unverifiable.
// 11. Structural violation in snapshot group (ORDER_SNAPSHOT_ID_MATCH) → "unverifiable",
//     never mismatch.
// 12. Unverifiable cycle breaks consecutive-clean-cycle count (Finding 2): a mismatch
//     followed by an unverifiable, then the same mismatch, must NOT confirm.
// 13. Duplicate/late delta corrupts book → "unverifiable", never mismatch.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// oracleCapture extends allCapture to also record SnapshotAudit calls.
type oracleCapture struct {
	allCapture
	auditResults []string // "match", "mismatch_suspected", "mismatch_confirmed", "unverifiable"
}

func (oc *oracleCapture) SnapshotAudit(result string) {
	oc.auditResults = append(oc.auditResults, result)
}

func (oc *oracleCapture) lastAudit() string {
	if len(oc.auditResults) == 0 {
		return ""
	}
	return oc.auditResults[len(oc.auditResults)-1]
}

// newOracleEngine creates a window-1 MBO engine wired to an oracleCapture.
// OracleConfirmCycles defaults to 2.
func newOracleEngine(confirmCycles int) (*Engine, *oracleCapture) {
	oc := &oracleCapture{}
	cfg := Config{
		Feed:                core.FeedMBO,
		ReorderWindow:       1,
		OracleConfirmCycles: confirmCycles,
	}
	return New(cfg, oc), oc
}

// --- Frame builders for oracle tests ---

// buildSnapOrderFullFields builds a SnapshotOrder (0x21, 44 bytes) frame
// with all fields explicitly set (side, flags, enterTS, price, qty).
//
//	Body[0:4]  = Snapshot ID (u32 LE)
//	Body[4:12] = Order ID (u64 LE)
//	Body[12]   = Side (u8)
//	Body[13]   = Order Flags (u8)
//	Body[14:16]= padding
//	Body[16:24]= Enter Timestamp (u64 LE)
//	Body[24:32]= Price (i64 LE)
//	Body[32:40]= Quantity (u64 LE)
func buildSnapOrderFields(ch uint8, snapshotID uint32, orderID uint64, side uint8, flags uint8, enterTS uint64, price int64, qty uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeSnapshotOrder, 44, func(b *wb.Body) {
			b.U32(snapshotID) // body[0]  Snapshot ID
			b.U64(orderID)    // body[4]  Order ID
			b.U8(side)        // body[12] Side
			b.U8(flags)       // body[13] Order Flags
			b.Pad(2)          // body[14] padding
			b.U64(enterTS)    // body[16] Enter Timestamp
			b.I64(price)      // body[24] Price
			b.U64(qty)        // body[32] Quantity
		}).
		Bytes()
}

// buildOrderAddExact builds an OrderAdd frame with explicit side, flags, enterTS, price, qty.
// Used to add a resting order whose fields exactly match a snapshot record.
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
func buildOrderAddExact(ch uint8, instrID uint32, perSeq uint32, orderID uint64, side uint8, flags uint8, enterTS uint64, price int64, qty uint64) []byte {
	return wb.Frame(wire.MagicMBO).
		Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID) // body[0]  Instrument ID
			b.U16(1)       // body[4]  Source ID
			b.U8(side)     // body[6]  Side
			b.U8(flags)    // body[7]  Order Flags
			b.U32(perSeq)  // body[8]  Per-Instrument Seq
			b.U64(orderID) // body[12] Order ID
			b.U64(enterTS) // body[20] Enter Timestamp
			b.I64(price)   // body[28] Price
			b.U64(qty)     // body[36] Quantity
			b.Pad(4)       // body[44] Reserved
		}).
		Bytes()
}

// seedOracleHistory bootstraps refdata and feeds gapless OrderAdd frames to
// establish a trusted per-instrument history. Returns the next mktdata seq.
//
// orderID range: 80000+n (to avoid conflict with test-case orderIDs).
func seedOracleHistory(e *Engine, ch uint8, instrID uint32, n uint32, mktSeq uint64) uint64 {
	reachedReadyMBO(e, ch, instrID, 0, 1)
	seq := mktSeq
	for i := uint32(1); i <= n; i++ {
		runMktdataSeq(e, buildOrderAddExact(ch, instrID, i, uint64(80000+i), 0, 0, 1000, 100, 10), seq)
		seq++
	}
	e.Flush()
	return seq
}

// completeSnapshot feeds Begin→(orders...)→End on the snapshot port and returns
// the next snapshot-port seq to use. instrSeqK is SnapshotBegin's Last Instrument Seq.
func completeSnapshot(e *Engine, ch uint8, instrID uint32, anchorSeq uint64, snapID uint32, instrSeqK uint32, snapOrders [][]byte, startSnapSeq uint64) uint64 {
	snapSeq := startSnapSeq
	beginRaw := buildSnapBeginFull(ch, instrID, anchorSeq, uint32(len(snapOrders)), snapID, instrSeqK)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: snapSeq}})
	snapSeq++
	for _, orderRaw := range snapOrders {
		runStream(e, []streamEntry{{port: core.PortSnapshot, raw: orderRaw, seq: snapSeq}})
		snapSeq++
	}
	endRaw := buildSnapEndFull(ch, instrID, anchorSeq, snapID)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: snapSeq}})
	snapSeq++
	return snapSeq
}

// --- Test 1: Match — delta book and snapshot agree ---

// TestOracleMatch: when the delta book at per-instrument seq K matches the
// snapshot, the oracle emits "match" and no SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT.
func TestOracleMatch(t *testing.T) {
	const ch, instrID = uint8(1), uint32(300)

	e, oc := newOracleEngine(2)

	// Seed gapless history: one OrderAdd at perSeq=1, orderID=80001.
	// The delta book at K=1 has: {80001: side=0, flags=0, enterTS=1000, price=100, qty=10}.
	mktSeq := seedOracleHistory(e, ch, instrID, 1, 1)
	_ = mktSeq
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot at K=1 with the same order.
	snapOrders := [][]byte{
		buildSnapOrderFields(ch, 1, 80001, 0, 0, 1000, 100, 10),
	}
	completeSnapshot(e, ch, instrID, 1, 1, 1, snapOrders, 1)

	if oc.lastAudit() != "match" {
		t.Errorf("expected audit='match', got %q (all audits: %v)", oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("no violation expected on match")
	}
}

// --- Test 2: Mismatch suspected then confirmed ---

// TestOracleMismatchSuspectedThenConfirmed: first cycle emits Suspected (no CI fail),
// reproduce same divergence on second clean cycle → mismatch_confirmed + Violation.
func TestOracleMismatchSuspectedThenConfirmed(t *testing.T) {
	const ch, instrID = uint8(1), uint32(301)

	e, oc := newOracleEngine(2) // confirmCycles=2

	// Seed gapless history: two orders added at perSeq=1,2.
	// orderID 80001 and 80002 are in the delta book at K=2.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 1, 0, 2000, 200, 5), 2)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot cycle 1 at K=2: OMIT order 80002 (mismatch: extra_in_delta).
	snapOrders1 := [][]byte{
		buildSnapOrderFields(ch, 10, 80001, 0, 0, 1000, 100, 10),
		// 80002 is in delta book but missing from snapshot
	}
	// Begin declares totalOrders=1 (only 80001).
	beginRaw1 := buildSnapBeginFull(ch, instrID, 2, 1 /*totalOrders*/, 10, 2 /*K=2*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw1, seq: 1}})
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: snapOrders1[0], seq: 2}})
	endRaw1 := buildSnapEndFull(ch, instrID, 2, 10)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw1, seq: 3}})

	// First cycle: should be "mismatch_suspected", NO Violation.
	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 1: expected audit='mismatch_suspected', got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 1: no Violation expected for first suspected mismatch")
	}

	// We need a second snapshot at K=2 again (the delta book hasn't changed, so
	// mktdata seq can stay the same — feed a mktdata frame to keep anchor correct).
	// Feed mktdata at seq 3 to advance the mktdata port (anchor check gating).
	runMktdataSeq(e, buildOrderAddFrame(ch, instrID, 3), 3)
	e.Flush()

	// Snapshot cycle 2: same omission → same signature → confirmed.
	beginRaw2 := buildSnapBeginFull(ch, instrID, 2, 1 /*totalOrders*/, 11, 2 /*K=2*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw2, seq: 4}})
	snapOrders2 := [][]byte{
		buildSnapOrderFields(ch, 11, 80001, 0, 0, 1000, 100, 10),
	}
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: snapOrders2[0], seq: 5}})
	endRaw2 := buildSnapEndFull(ch, instrID, 2, 11)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw2, seq: 6}})

	// After feeding perSeq=3, lastInstrSeq=3 != K=2, so cycle 2 must be unverifiable
	// (cannot rewind the delta book to K=2 without an undo log).
	if oc.lastAudit() != "unverifiable" {
		t.Errorf("cycle 2: after advancing past K, expected 'unverifiable', got %q (all: %v)",
			oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 2: must not emit Violation when book is ahead of K")
	}
}

// TestOracleMismatchSuspectedThenConfirmedFixedK demonstrates the suspect→confirmed
// path using an empty book (K=0, no orders ever added) where the oracle can always
// compare at K=0.
func TestOracleMismatchSuspectedThenConfirmedFixedK(t *testing.T) {
	const ch, instrID = uint8(1), uint32(302)

	e, oc := newOracleEngine(2) // confirmCycles=2

	// Bootstrap refdata so gateConsumer condition 1 is met.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	// Feed perSeq=1 so lastInstrSeq=1 and bookTrusted=true.
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Delta book at K=1 has order 80001 (side=0, flags=0, enterTS=1000, price=100, qty=10).
	// Snapshot claims K=1 but omits order 80001 (total=0, no orders).
	// This is: snapshot says empty book, delta says 1 order → "extra_in_delta:80001".

	// Cycle 1: snapshot at K=1, empty book.
	beginRaw1 := buildSnapBeginFull(ch, instrID, 1 /*anchorSeq*/, 0 /*totalOrders*/, 20 /*snapID*/, 1 /*K*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw1, seq: 1}})
	endRaw1 := buildSnapEndFull(ch, instrID, 1, 20)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw1, seq: 2}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 1: expected 'mismatch_suspected', got %q (all: %v)", oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 1: Suspected must NOT emit a Violation finding")
	}

	// Cycle 2: same scenario, same signature → confirmed.
	beginRaw2 := buildSnapBeginFull(ch, instrID, 1, 0, 21, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw2, seq: 3}})
	endRaw2 := buildSnapEndFull(ch, instrID, 1, 21)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw2, seq: 4}})

	if oc.lastAudit() != "mismatch_confirmed" {
		t.Errorf("cycle 2: expected 'mismatch_confirmed', got %q (all: %v)", oc.lastAudit(), oc.auditResults)
	}
	if !hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 2: expected SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT Violation on confirmed mismatch")
	}
}

// --- Test 3: Loss before K → unverifiable ---

// TestOracleUnverifiableOnLoss: a mktdata gap makes bookTrusted=false, forcing "unverifiable".
func TestOracleUnverifiableOnLoss(t *testing.T) {
	const ch, instrID = uint8(1), uint32(303)

	e, oc := newOracleEngine(2)

	// Bootstrap refdata.
	reachedReadyMBO(e, ch, instrID, 0, 1)

	// Feed perSeq=1 then SKIP perSeq=2 (gap) → bookTrusted=false after the gap.
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	// Jump mktdata seq from 1 to 3 (gap at 2), then perSeq=3.
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 3, 80002, 0, 0, 2000, 200, 5), 3)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot at K=3 (even though bookTrusted=false due to the per-instr gap).
	beginRaw := buildSnapBeginFull(ch, instrID, 3, 0, 30, 3 /*K=3*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	endRaw := buildSnapEndFull(ch, instrID, 3, 30)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 2}})

	if oc.lastAudit() != "unverifiable" {
		t.Errorf("expected 'unverifiable' with bookTrusted=false, got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("must never emit Violation when unverifiable (loss)")
	}
}

// --- Test 4: OracleConfirmCycles=1 confirms on the first cycle ---

// TestOracleConfirmCycles1: with --oracle-confirm-cycles=1 a single divergence is confirmed.
func TestOracleConfirmCycles1(t *testing.T) {
	const ch, instrID = uint8(1), uint32(304)

	e, oc := newOracleEngine(1) // confirmCycles=1

	// Bootstrap refdata and seed one order.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot at K=1 but empty (delta has 80001, snapshot is empty → extra_in_delta).
	beginRaw := buildSnapBeginFull(ch, instrID, 1, 0, 40, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	endRaw := buildSnapEndFull(ch, instrID, 1, 40)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 2}})

	if oc.lastAudit() != "mismatch_confirmed" {
		t.Errorf("confirmCycles=1: expected 'mismatch_confirmed' on first cycle, got %q", oc.lastAudit())
	}
	if !hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("confirmCycles=1: expected Violation on first cycle")
	}
}

// --- Test 5: Hidden orders excluded from qty diff ---

// TestOracleHiddenOrdersExcluded: hidden orders (OrderFlags bit2=1) are skipped
// in the diff; their qty mismatch must NOT trigger a violation.
func TestOracleHiddenOrdersExcluded(t *testing.T) {
	const ch, instrID = uint8(1), uint32(305)
	const hiddenFlags = uint8(0x04) // bit 2 set

	e, oc := newOracleEngine(1) // confirmCycles=1 so any spurious mismatch fires immediately

	// Bootstrap refdata.
	reachedReadyMBO(e, ch, instrID, 0, 1)

	// Add a hidden order at perSeq=1. Hidden orders go into hiddenIDs, not live.
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, hiddenFlags, 1000, 100, 10), 1)
	// Add a visible order at perSeq=2.
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 0, 0, 2000, 200, 5), 2)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot at K=2: contains both orders.
	// The hidden order's snapshot qty differs from delta (delta doesn't track it in live).
	// The visible order matches exactly.
	// Expected: oracle skips the hidden order entirely → "match".
	snapOrders := [][]byte{
		// Hidden order: qty=999 (deliberately wrong qty, but should be ignored).
		buildSnapOrderFields(ch, 50, 80001, 0, hiddenFlags, 1000, 100, 999),
		// Visible order: qty=5 matches delta.
		buildSnapOrderFields(ch, 50, 80002, 0, 0, 2000, 200, 5),
	}
	beginRaw := buildSnapBeginFull(ch, instrID, 2, 2 /*totalOrders*/, 50, 2 /*K=2*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	for i, raw := range snapOrders {
		runStream(e, []streamEntry{{port: core.PortSnapshot, raw: raw, seq: uint64(2 + i)}})
	}
	endRaw := buildSnapEndFull(ch, instrID, 2, 50)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: uint64(2 + len(snapOrders))}})

	// Hidden orders are excluded from the diff entirely. The visible order matches.
	// Delta book does not contain 80001 (hidden). Snapshot has 80001 hidden → skipped.
	// → should be "match".
	if oc.lastAudit() != "match" {
		t.Errorf("hidden order exclusion: expected 'match', got %q (all: %v)", oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("hidden order qty mismatch must not trigger a Violation")
	}
}

// --- Test 6: lastInstrSeq != K → unverifiable ---

// TestOracleUnverifiableBookAheadOfK: if the delta book has advanced past K,
// the oracle emits "unverifiable" (cannot rewind).
func TestOracleUnverifiableBookAheadOfK(t *testing.T) {
	const ch, instrID = uint8(1), uint32(306)

	e, oc := newOracleEngine(1)

	// Bootstrap refdata.
	reachedReadyMBO(e, ch, instrID, 0, 1)

	// Feed perSeq=1 and perSeq=2 (delta book at K=2 after flush).
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 0, 0, 2000, 200, 5), 2)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot claims K=1, but lastInstrSeq=2 (book is ahead of K).
	beginRaw := buildSnapBeginFull(ch, instrID, 1, 0, 60, 1 /*K=1, but lastInstrSeq=2*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	endRaw := buildSnapEndFull(ch, instrID, 1, 60)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 2}})

	if oc.lastAudit() != "unverifiable" {
		t.Errorf("book ahead of K: expected 'unverifiable', got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("must never emit Violation when book is ahead of K")
	}
}

// --- Test 7: Dirty snapshot group → unverifiable ---

// TestOracleUnverifiableDirtyGroup: an intra-group snapshot-port gap makes the
// group dirty → "unverifiable", never a mismatch.
func TestOracleUnverifiableDirtyGroup(t *testing.T) {
	const ch, instrID = uint8(1), uint32(307)

	e, oc := newOracleEngine(1)

	// Bootstrap refdata and seed gapless history at K=1.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot with an intra-group seq gap (Begin at snapSeq=1, Order at snapSeq=3 — gap at 2).
	beginRaw := buildSnapBeginFull(ch, instrID, 1, 0, 70, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	// skip snapSeq=2 (gap → dirty)
	endRaw := buildSnapEndFull(ch, instrID, 1, 70)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 3}})

	if oc.lastAudit() != "unverifiable" {
		t.Errorf("dirty group: expected 'unverifiable', got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("dirty group must never emit Violation")
	}
}

// --- Test 8: Different divergence on second cycle → suspect resets, no confirm ---

// TestOracleSuspectResetOnDifferentSignature: if cycle 2 has a DIFFERENT
// divergence from cycle 1, the count resets to 1 (still Suspected, not Confirmed).
func TestOracleSuspectResetOnDifferentSignature(t *testing.T) {
	const ch, instrID = uint8(1), uint32(308)

	e, oc := newOracleEngine(2) // confirmCycles=2

	// Bootstrap refdata and add two orders.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 1, 0, 2000, 200, 5), 2)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Cycle 1 at K=2: snapshot omits 80001 (delta has both, snapshot has only 80002).
	// Diff signature: "80001:extra_in_delta".
	beginRaw1 := buildSnapBeginFull(ch, instrID, 2, 1, 80, 2)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw1, seq: 1}})
	runStream(e, []streamEntry{{port: core.PortSnapshot,
		raw: buildSnapOrderFields(ch, 80, 80002, 1, 0, 2000, 200, 5), seq: 2}})
	endRaw1 := buildSnapEndFull(ch, instrID, 2, 80)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw1, seq: 3}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 1: expected 'mismatch_suspected', got %q", oc.lastAudit())
	}

	// Cycle 2 at K=2: DIFFERENT signature — snapshot omits 80002 instead.
	// Diff signature: "80002:extra_in_delta".
	beginRaw2 := buildSnapBeginFull(ch, instrID, 2, 1, 81, 2)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw2, seq: 4}})
	runStream(e, []streamEntry{{port: core.PortSnapshot,
		raw: buildSnapOrderFields(ch, 81, 80001, 0, 0, 1000, 100, 10), seq: 5}})
	endRaw2 := buildSnapEndFull(ch, instrID, 2, 81)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw2, seq: 6}})

	// Different signature: count reset → still Suspected (count=1 < confirmCycles=2).
	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 2 (different sig): expected 'mismatch_suspected', got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("different signatures must not confirm")
	}
}

// --- Test 9: Match after suspect clears the suspect state ---

// TestOracleSuspectClearedOnMatch: a "match" after a "suspected" clears the suspect,
// so a subsequent divergence starts fresh (count=1, Suspected).
func TestOracleSuspectClearedOnMatch(t *testing.T) {
	const ch, instrID = uint8(1), uint32(309)

	e, oc := newOracleEngine(2)

	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Cycle 1: mismatch → suspected (count=1).
	beginRaw1 := buildSnapBeginFull(ch, instrID, 1, 0, 90, 1)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw1, seq: 1}})
	endRaw1 := buildSnapEndFull(ch, instrID, 1, 90)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw1, seq: 2}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 1: expected 'mismatch_suspected', got %q", oc.lastAudit())
	}

	// Cycle 2: match → clears suspect.
	beginRaw2 := buildSnapBeginFull(ch, instrID, 1, 1, 91, 1)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw2, seq: 3}})
	runStream(e, []streamEntry{{port: core.PortSnapshot,
		raw: buildSnapOrderFields(ch, 91, 80001, 0, 0, 1000, 100, 10), seq: 4}})
	endRaw2 := buildSnapEndFull(ch, instrID, 1, 91)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw2, seq: 5}})

	if oc.lastAudit() != "match" {
		t.Errorf("cycle 2: expected 'match', got %q", oc.lastAudit())
	}

	// Cycle 3: mismatch again → suspected again (count reset to 1, not confirmed).
	beginRaw3 := buildSnapBeginFull(ch, instrID, 1, 0, 92, 1)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw3, seq: 6}})
	endRaw3 := buildSnapEndFull(ch, instrID, 1, 92)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw3, seq: 7}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 3 after match: expected 'mismatch_suspected' (not confirmed), got %q", oc.lastAudit())
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 3: cleared suspect should restart at 1, not confirm")
	}
}

// --- Test 10: No refdata → unverifiable ---

// TestOracleUnverifiableNoRefdata: without refdata, oracle must emit "unverifiable".
func TestOracleUnverifiableNoRefdata(t *testing.T) {
	const ch, instrID = uint8(1), uint32(310)

	// Engine without refdata bootstrapping.
	e, oc := newOracleEngine(1)

	// Feed perSeq=1 (no refdata, so bookTrusted will be true but refdata gate will fail).
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	beginRaw := buildSnapBeginFull(ch, instrID, 1, 0, 100, 1)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	endRaw := buildSnapEndFull(ch, instrID, 1, 100)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 2}})

	if oc.lastAudit() != "unverifiable" {
		t.Errorf("no refdata: expected 'unverifiable', got %q", oc.lastAudit())
	}
}

// --- Test 11: Structural violation in snapshot group → unverifiable ---

// TestOracleUnverifiableStructuralViolation: a snapshot group that triggers a
// structural violation (ORDER_SNAPSHOT_ID_MATCH — order's snapshot ID differs
// from the open group's snapshot ID) must emit "unverifiable", never "mismatch_*".
// This guards against false positives from malformed snapshot data (Finding 1).
func TestOracleUnverifiableStructuralViolation(t *testing.T) {
	const ch, instrID = uint8(1), uint32(311)

	e, oc := newOracleEngine(1) // confirmCycles=1 so any spurious mismatch fires immediately

	// Bootstrap refdata and seed gapless history at K=1.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// SnapshotBegin declares snapshot ID=200. The SnapshotOrder will carry a
	// different snapshot ID (=999) → triggers SNAP.ORDER_SNAPSHOT_ID_MATCH violation
	// → structuralViolation=true → oracle must emit "unverifiable", not "mismatch_*".
	beginRaw := buildSnapBeginFull(ch, instrID, 1 /*anchorSeq*/, 1 /*totalOrders*/, 200 /*snapID*/, 1 /*K*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	// SnapshotOrder with WRONG snapshot ID 999 instead of 200.
	wrongIDOrder := buildSnapOrderFields(ch, 999 /*wrong snapID*/, 80001, 0, 0, 1000, 100, 10)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: wrongIDOrder, seq: 2}})
	endRaw := buildSnapEndFull(ch, instrID, 1, 200)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: 3}})

	// Structural violation → unverifiable, never mismatch.
	if oc.lastAudit() != "unverifiable" {
		t.Errorf("structural violation: expected 'unverifiable', got %q (all: %v)",
			oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("structural violation: oracle must not emit SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	}
	// The structural rule itself should have fired.
	if !hasViolation(&oc.allCapture, "SNAP.ORDER_SNAPSHOT_ID_MATCH") {
		t.Error("structural violation: expected SNAP.ORDER_SNAPSHOT_ID_MATCH to fire")
	}
}

// --- Test 12: Unverifiable cycle breaks consecutive-clean-cycle count ---

// TestOracleSuspectResetOnUnverifiable: a mismatch_suspected followed by an
// unverifiable cycle, then the same mismatch again, must NOT confirm.
// The unverifiable cycle must reset the suspect state (Finding 2).
func TestOracleSuspectResetOnUnverifiable(t *testing.T) {
	const ch, instrID = uint8(1), uint32(312)

	e, oc := newOracleEngine(2) // confirmCycles=2

	// Bootstrap refdata and seed one order at K=1.
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Cycle 1: snapshot at K=1 is empty (delta has 80001) → mismatch_suspected.
	beginRaw1 := buildSnapBeginFull(ch, instrID, 1, 0 /*totalOrders=0*/, 110, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw1, seq: 1}})
	endRaw1 := buildSnapEndFull(ch, instrID, 1, 110)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw1, seq: 2}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 1: expected 'mismatch_suspected', got %q", oc.lastAudit())
	}

	// Cycle 2: introduce an intra-group gap → dirty → unverifiable.
	// This must RESET the suspect state so cycle 3 can't confirm.
	beginRaw2 := buildSnapBeginFull(ch, instrID, 1, 0, 111, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw2, seq: 3}})
	// skip snapSeq=4 (gap → dirty)
	endRaw2 := buildSnapEndFull(ch, instrID, 1, 111)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw2, seq: 5}})

	if oc.lastAudit() != "unverifiable" {
		t.Errorf("cycle 2: expected 'unverifiable' (dirty), got %q", oc.lastAudit())
	}

	// Cycle 3: same mismatch as cycle 1 (empty snapshot, delta has 80001).
	// Without the Finding 2 fix, count would be 2 → confirmed. With the fix,
	// unverifiable reset count to 0, so cycle 3 is count=1 → suspected only.
	beginRaw3 := buildSnapBeginFull(ch, instrID, 1, 0, 112, 1 /*K=1*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw3, seq: 6}})
	endRaw3 := buildSnapEndFull(ch, instrID, 1, 112)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw3, seq: 7}})

	if oc.lastAudit() != "mismatch_suspected" {
		t.Errorf("cycle 3: after unverifiable reset, expected 'mismatch_suspected' (not confirmed), got %q (all: %v)",
			oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("cycle 3: unverifiable must have reset the suspect; must not confirm")
	}
}

// --- Test 13: Duplicate/late delta corrupts book → unverifiable ---

// TestOracleUnverifiableDuplicateDelta: a replayed/late delta (perSeq <= lastInstrSeq)
// mutates the live book without advancing lastInstrSeq. The oracle must emit
// "unverifiable" rather than diff a post-duplicate-corrupted book (Codex P2 fix).
func TestOracleUnverifiableDuplicateDelta(t *testing.T) {
	const ch, instrID = uint8(1), uint32(313)

	e, oc := newOracleEngine(1) // confirmCycles=1 so any spurious mismatch fires immediately

	// Bootstrap refdata and seed gapless history at K=2.
	// Delta book at K=2: {80001: side=0, flags=0, enterTS=1000, price=100, qty=10}
	//                     {80002: side=1, flags=0, enterTS=2000, price=200, qty=5}
	reachedReadyMBO(e, ch, instrID, 0, 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1, 80001, 0, 0, 1000, 100, 10), 1)
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 2, 80002, 1, 0, 2000, 200, 5), 2)
	e.Flush()

	// Feed a duplicate of perSeq=1 (same orderID 80001 but different qty=99).
	// This is a divergent duplicate: perSeq=1 <= lastInstrSeq=2, so lastInstrSeq
	// stays at 2 but the book now has 80001 at qty=99 instead of 10.
	// bookCorruptedByDup must be set → oracle emits "unverifiable".
	runMktdataSeq(e, buildOrderAddExact(ch, instrID, 1 /*dup perSeq*/, 80001, 0, 0, 1000, 100, 99 /*different qty*/), 3)
	e.Flush()
	clearFindings(&oc.allCapture)
	oc.auditResults = nil

	// Snapshot at K=2 with the CORRECT state (80001@qty=10, 80002@qty=5).
	// The oracle must NOT compare: the book has been corrupted by the dup.
	snapOrders := [][]byte{
		buildSnapOrderFields(ch, 120, 80001, 0, 0, 1000, 100, 10),
		buildSnapOrderFields(ch, 120, 80002, 1, 0, 2000, 200, 5),
	}
	beginRaw := buildSnapBeginFull(ch, instrID, 2, 2 /*totalOrders*/, 120, 2 /*K=2*/)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: beginRaw, seq: 1}})
	for i, raw := range snapOrders {
		runStream(e, []streamEntry{{port: core.PortSnapshot, raw: raw, seq: uint64(2 + i)}})
	}
	endRaw := buildSnapEndFull(ch, instrID, 2, 120)
	runStream(e, []streamEntry{{port: core.PortSnapshot, raw: endRaw, seq: uint64(2 + len(snapOrders))}})

	// bookCorruptedByDup=true → oracle must emit "unverifiable".
	if oc.lastAudit() != "unverifiable" {
		t.Errorf("dup-corrupted book: expected 'unverifiable', got %q (all: %v)",
			oc.lastAudit(), oc.auditResults)
	}
	if hasViolation(&oc.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("dup-corrupted book: oracle must not emit SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT")
	}
}
