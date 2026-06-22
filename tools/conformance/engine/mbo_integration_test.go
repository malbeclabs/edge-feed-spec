package engine

// mbo_integration_test.go — End-to-end MBO validator integration test (Task 27).
//
// Two scenarios exercise the full pipeline through Engine.Process + Flush + EndRun:
//
//  1. conformantMBOSession: a fully conformant session
//     refdata → initial snapshot bootstrap → dense deltas → matching periodic
//     snapshot.  Asserts: ZERO must-severity Violations across the whole session,
//     and the oracle emits at least one "match" audit result.
//
//  2. divergentMBOSession: identical setup but the periodic snapshot omits a
//     still-resting order, repeated across OracleConfirmCycles=2 clean cycles at
//     the same per-instrument seq K (book does not advance between cycles).
//     Asserts: SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT is confirmed (exactly one
//     Violation) and no OTHER must-Violation fires.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// integrationCapture is a full Reporter that collects findings and audit results.
type integrationCapture struct {
	allCapture
	auditResults []string
}

func (ic *integrationCapture) SnapshotAudit(result string) {
	ic.auditResults = append(ic.auditResults, result)
}

func hasAudit(ic *integrationCapture, result string) bool {
	for _, r := range ic.auditResults {
		if r == result {
			return true
		}
	}
	return false
}

// mustViolations returns all findings that are Must-severity Violations.
func mustViolations(ic *integrationCapture) []core.Finding {
	var out []core.Finding
	for _, f := range ic.findings {
		if f.Severity == core.Must && f.Status == core.Violation {
			out = append(out, f)
		}
	}
	return out
}

// newIntegrationEngine creates an MBO engine with OracleConfirmCycles=2.
func newIntegrationEngine(confirmCycles int) (*Engine, *integrationCapture) {
	ic := &integrationCapture{}
	cfg := Config{
		Feed:                core.FeedMBO,
		ReorderWindow:       1,
		OracleConfirmCycles: confirmCycles,
	}
	return New(cfg, ic), ic
}

// feedRefDataOneInstr bootstraps MBO refdata for channel ch with one instrument.
// Sends ManifestSummary(valid=1,seq=1,count=1) + InstrumentDef + ManifestSummary.
// Returns the next refdata seq.
func feedRefDataOneInstr(e *Engine, ch uint8, instrID uint32, startSeq uint64) uint64 {
	seq := startSeq

	feed := func(raw []byte) {
		f, sf := wire.Decode(raw, wire.MagicMBO)
		f.Header.Sequence = seq
		e.Process(f, core.PortRefData, sf)
		seq++
	}

	feed(wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes())

	feed(wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID  body[0..3]
			b.Pad(69)      // opaque fields  body[4..72]
			b.U8(0)        // priceBound=0   body[73]
			b.U16(1)       // Manifest Seq=1 body[74..75]
			// 76 body bytes total → 80-byte message (4 header + 76 body)
		}).
		Bytes())

	feed(wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes())

	return seq
}

// feedMktdataOrderAdd feeds a single OrderAdd delta on the mktdata port.
// Returns the next mktdata seq.
func feedMktdataOrderAdd(e *Engine, ch uint8, instrID uint32, perSeq uint32, orderID uint64, price int64, qty uint64, mktSeq uint64) uint64 {
	raw := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID
			b.U16(1)       // Source ID
			b.U8(0)        // Side = bid
			b.U8(0)        // OrderFlags = 0
			b.U32(perSeq)  // Per-Instrument Seq
			b.U64(orderID) // Order ID
			b.U64(1000)    // Enter Timestamp
			b.I64(price)   // Price
			b.U64(qty)     // Quantity
			b.Pad(4)       // Reserved
		}).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicMBO)
	f.Header.Sequence = mktSeq
	e.Process(f, core.PortMktData, sf)
	return mktSeq + 1
}

// feedMktdataOrderCancel feeds a single OrderCancel delta on the mktdata port.
// Returns the next mktdata seq.
func feedMktdataOrderCancel(e *Engine, ch uint8, instrID uint32, perSeq uint32, orderID uint64, mktSeq uint64) uint64 {
	raw := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeOrderCancel, 32, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID
			b.U16(1)       // Source ID
			b.Pad(2)       // Reserved
			b.U32(perSeq)  // Per-Instrument Seq
			b.U64(orderID) // Order ID
			b.U64(0)       // Reserved
		}).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicMBO)
	f.Header.Sequence = mktSeq
	e.Process(f, core.PortMktData, sf)
	return mktSeq + 1
}

// feedInitialSnapshot feeds the cold-start (bootstrap) snapshot: an empty book
// at K=0 (no orders yet). This establishes the snapshot port baseline.
// Returns the next snapshot seq.
func feedInitialSnapshot(e *Engine, ch uint8, instrID uint32, anchorSeq uint64, snapID uint32, startSnapSeq uint64) uint64 {
	seq := startSnapSeq
	// Empty book: TotalOrders=0, LastInstrumentSeq=0.
	beginRaw := buildSnapBeginFull(ch, instrID, anchorSeq, 0 /*totalOrders*/, snapID, 0 /*K=0*/)
	f, sf := wire.Decode(beginRaw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortSnapshot, sf)
	seq++

	endRaw := buildSnapEndFull(ch, instrID, anchorSeq, snapID)
	f, sf = wire.Decode(endRaw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortSnapshot, sf)
	seq++
	return seq
}

// feedPeriodicSnapshot feeds a periodic snapshot with a given set of orders.
// orders is a slice of (orderID, price, qty) tuples encoded as SnapshotOrder frames.
// snapID should be unique across invocations. anchorSeq is the mktdata anchor.
// lastInstrSeqK is the LastInstrumentSeq field of SnapshotBegin.
// Returns the next snapshot seq.
func feedPeriodicSnapshot(e *Engine, ch uint8, instrID uint32, anchorSeq uint64, snapID uint32, lastInstrSeqK uint32, snapOrders [][]byte, startSnapSeq uint64) uint64 {
	seq := startSnapSeq
	beginRaw := buildSnapBeginFull(ch, instrID, anchorSeq, uint32(len(snapOrders)), snapID, lastInstrSeqK)
	f, sf := wire.Decode(beginRaw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortSnapshot, sf)
	seq++

	for _, orderRaw := range snapOrders {
		f, sf = wire.Decode(orderRaw, wire.MagicMBO)
		f.Header.Sequence = seq
		e.Process(f, core.PortSnapshot, sf)
		seq++
	}

	endRaw := buildSnapEndFull(ch, instrID, anchorSeq, snapID)
	f, sf = wire.Decode(endRaw, wire.MagicMBO)
	f.Header.Sequence = seq
	e.Process(f, core.PortSnapshot, sf)
	seq++
	return seq
}

// buildSnapOrderForInteg builds a SnapshotOrder frame for the integration test.
func buildSnapOrderForInteg(ch uint8, snapID uint32, orderID uint64, price int64, qty uint64) []byte {
	return wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotOrder, 44, func(b *wb.Body) {
			b.U32(snapID)  // Snapshot ID
			b.U64(orderID) // Order ID
			b.U8(0)        // Side = bid
			b.U8(0)        // OrderFlags = 0
			b.Pad(2)       // padding
			b.U64(1000)    // Enter Timestamp
			b.I64(price)   // Price
			b.U64(qty)     // Quantity
		}).
		Bytes()
}

// --- Test 1: Conformant MBO session → zero must-Violations + oracle match ---

// TestMBOIntegrationConformant exercises the full MBO pipeline with a conformant
// synthetic session:
//
//   - refdata: ManifestSummary(valid=1, seq=1, count=1) + InstrumentDefinition +
//     ManifestSummary (closes bootstrap cycle)
//   - initial snapshot: empty-book bootstrap (anchor=0, K=0) on PortSnapshot
//   - deltas: OrderAdd(perSeq=1, orderID=1001), OrderAdd(perSeq=2, orderID=1002),
//     OrderCancel(perSeq=3, orderID=1002) — delta book at K=3: {1001: price=100, qty=10}
//   - periodic snapshot at anchor=mktSeq3, K=3: exactly {orderID=1001, price=100, qty=10}
//
// Expected: zero must-Violations, oracle emits "match".
func TestMBOIntegrationConformant(t *testing.T) {
	const ch = uint8(1)
	const instrID = uint32(500)

	e, ic := newIntegrationEngine(2)

	// Step 1: Bootstrap refdata.
	feedRefDataOneInstr(e, ch, instrID, 1)
	e.Flush()

	// Step 2: Initial snapshot bootstrap (empty book at anchor=0, K=0).
	// This satisfies SNAP.ROUND_ROBIN_COVERS_MANIFEST before we enter the main stream
	// and sets up snapCount=1 for the instrument.
	snapSeq := feedInitialSnapshot(e, ch, instrID, 0 /*anchor*/, 1 /*snapID*/, 1)
	e.Flush()

	// Step 3: Feed conformant deltas on the mktdata port.
	// K=3: book = {1001: side=bid, price=100, qty=10}
	// orderID 1002 is added then cancelled → not in final book.
	mktSeq := uint64(1)
	mktSeq = feedMktdataOrderAdd(e, ch, instrID, 1 /*perSeq*/, 1001 /*orderID*/, 100 /*price*/, 10 /*qty*/, mktSeq)
	mktSeq = feedMktdataOrderAdd(e, ch, instrID, 2 /*perSeq*/, 1002 /*orderID*/, 200 /*price*/, 5 /*qty*/, mktSeq)
	mktSeq = feedMktdataOrderCancel(e, ch, instrID, 3 /*perSeq*/, 1002 /*orderID*/, mktSeq)
	e.Flush()

	// anchorMktSeq: the mktdata frame seq at the last delta (perSeq=3 was at mktSeq=3).
	// mktSeq is now 4 (next to use), so the anchor for the K=3 snapshot is seq 3.
	anchorMktSeq := mktSeq - 1

	// Step 4: Periodic snapshot at anchor=anchorMktSeq, K=3.
	// Snapshot book matches delta book exactly: {1001: price=100, qty=10}.
	snapOrders := [][]byte{
		buildSnapOrderForInteg(ch, 2 /*snapID*/, 1001 /*orderID*/, 100 /*price*/, 10 /*qty*/),
	}
	snapSeq = feedPeriodicSnapshot(e, ch, instrID, anchorMktSeq, 2 /*snapID*/, 3 /*K=3*/, snapOrders, snapSeq)
	e.Flush()
	e.EndRun()
	_ = snapSeq

	// Assert: zero must-Violations.
	if mvs := mustViolations(ic); len(mvs) != 0 {
		t.Errorf("conformant session: expected zero must-Violations, got %d:", len(mvs))
		for _, f := range mvs {
			t.Errorf("  rule=%s status=%v detail=%s", f.RuleID, f.Status, f.Detail)
		}
	}

	// Assert: oracle emitted at least one "match".
	if !hasAudit(ic, "match") {
		t.Errorf("conformant session: expected oracle to emit 'match', got audits: %v", ic.auditResults)
	}
}

// --- Test 2: Divergent periodic snapshot → SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT confirmed ---

// TestMBOIntegrationDivergentSnapshot verifies that a periodic snapshot which
// omits a still-resting order (on a gapless history, across OracleConfirmCycles=2
// clean cycles at the same per-instrument seq K) produces exactly one confirmed
// Violation of SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT and no other must-Violation.
//
// Setup: same refdata + initial snapshot + 3 deltas as the conformant test.
// At K=3 the delta book has {1001: price=100, qty=10}.
// Each periodic snapshot claims K=3 but presents an empty book (omits 1001).
// First cycle → mismatch_suspected; second cycle → mismatch_confirmed + Violation.
func TestMBOIntegrationDivergentSnapshot(t *testing.T) {
	const ch = uint8(1)
	const instrID = uint32(501)

	e, ic := newIntegrationEngine(2) // OracleConfirmCycles=2

	// Step 1: Bootstrap refdata.
	feedRefDataOneInstr(e, ch, instrID, 1)
	e.Flush()

	// Step 2: Initial snapshot bootstrap (empty book at anchor=0, K=0).
	snapSeq := feedInitialSnapshot(e, ch, instrID, 0, 1, 1)
	e.Flush()

	// Step 3: Feed conformant deltas on the mktdata port.
	// K=3: book = {1001: side=bid, price=100, qty=10}
	mktSeq := uint64(1)
	mktSeq = feedMktdataOrderAdd(e, ch, instrID, 1, 1001, 100, 10, mktSeq)
	mktSeq = feedMktdataOrderAdd(e, ch, instrID, 2, 1002, 200, 5, mktSeq)
	mktSeq = feedMktdataOrderCancel(e, ch, instrID, 3, 1002, mktSeq)
	e.Flush()

	// anchorMktSeq = last mktdata frame seq used (mktSeq-1 = 3).
	anchorMktSeq := mktSeq - 1

	// NOTE: do NOT feed any more deltas after this point so that lastInstrSeq
	// stays at K=3 for both periodic snapshot cycles.

	// Step 4: Divergent periodic snapshot — cycle 1.
	// Snapshot claims K=3 but presents empty book (omits still-resting order 1001).
	// Expected oracle outcome: mismatch_suspected (count=1, no Violation yet).
	snapSeq = feedPeriodicSnapshot(e, ch, instrID, anchorMktSeq, 2, 3, nil /*empty book*/, snapSeq)
	e.Flush()

	if !hasAudit(ic, "mismatch_suspected") {
		t.Errorf("divergent cycle 1: expected oracle 'mismatch_suspected', got audits: %v", ic.auditResults)
	}
	if hasViolation(&ic.allCapture, "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT") {
		t.Error("divergent cycle 1: must NOT emit Violation on first suspected mismatch")
	}

	// Step 5: Divergent periodic snapshot — cycle 2.
	// Same omission, same signature (empty book, order 1001 extra_in_delta).
	// Expected oracle outcome: mismatch_confirmed + Violation.
	snapSeq = feedPeriodicSnapshot(e, ch, instrID, anchorMktSeq, 3, 3, nil /*empty book*/, snapSeq)
	e.Flush()
	e.EndRun()
	_ = snapSeq

	if !hasAudit(ic, "mismatch_confirmed") {
		t.Errorf("divergent cycle 2: expected oracle 'mismatch_confirmed', got audits: %v", ic.auditResults)
	}

	// Assert: SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT fires exactly once.
	oracleViolationCount := 0
	for _, f := range mustViolations(ic) {
		if f.RuleID == "SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT" {
			oracleViolationCount++
		} else {
			t.Errorf("unexpected must-Violation: rule=%s status=%v detail=%s", f.RuleID, f.Status, f.Detail)
		}
	}
	if oracleViolationCount == 0 {
		t.Error("divergent cycle 2: expected SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT Violation, got none")
	}
	if oracleViolationCount > 1 {
		t.Errorf("divergent cycle 2: SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT fired %d times, want exactly 1", oracleViolationCount)
	}
}

// --- Test 3: SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING info rule ---

// TestAnchorLeOrGtLastAppliedHandling verifies the info rule fires for a periodic
// snapshot of a ready instrument (both anchor<=last_applied and anchor>last_applied
// cases) and never fires for a cold-start (no deltas seen before the snapshot).
func TestAnchorLeOrGtLastAppliedHandling(t *testing.T) {
	const ch = uint8(1)
	const instrID = uint32(502)
	const ruleID = "SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING"

	t.Run("fires_when_anchor_le_last_applied", func(t *testing.T) {
		// The rule requires st.snapCount > 0 (at least one prior snapshot completed)
		// so we feed an initial cold-start snapshot (snapCount → 1) before the deltas.
		//
		// Snapshot: anchor=1 (≤ lastInstrSeq=2) → "consistency" path.
		// K=2 == lastInstrSeq=2 (avoids SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS).
		// Both resting orders included so the oracle emits "match" (no must-violation).
		e, ic := newIntegrationEngine(2)
		feedRefDataOneInstr(e, ch, instrID, 1)
		e.Flush()

		// Initial cold-start snapshot (empty book, K=0, anchor=0) → snapCount=1.
		snapSeq := feedInitialSnapshot(e, ch, instrID, 0 /*anchor*/, 8 /*snapID*/, 1)
		e.Flush()

		// Two deltas: perSeq=1 (orderID=2001), perSeq=2 (orderID=2002).
		// Delta book at K=2: {2001: price=100 qty=5, 2002: price=200 qty=3}.
		mktSeq := uint64(1)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 1, 2001, 100, 5, mktSeq)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 2, 2002, 200, 3, mktSeq)
		e.Flush()

		// anchor=1 ≤ mktHigh=2 (no SNAP.ANCHOR_IS_MKTDATA_SEQ).
		// K=2 == lastInstrSeq=2 (no SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS).
		// anchor=1 < lastApplied_per_instr=2 → "consistency: anchor<=last_applied" branch.
		_ = mktSeq
		snapOrders := [][]byte{
			buildSnapOrderForInteg(ch, 10, 2001, 100, 5),
			buildSnapOrderForInteg(ch, 10, 2002, 200, 3),
		}
		feedPeriodicSnapshot(e, ch, instrID, 1 /*anchor*/, 10 /*snapID*/, 2 /*K=2*/, snapOrders, snapSeq)
		e.Flush()

		// Assert conformant — no must-Violations.
		if mvs := mustViolations(ic); len(mvs) != 0 {
			t.Errorf("anchor_le subtest: unexpected must-Violations:")
			for _, f := range mvs {
				t.Errorf("  rule=%s detail=%s", f.RuleID, f.Detail)
			}
		}

		found := false
		for _, f := range ic.findings {
			if f.RuleID == ruleID {
				found = true
				if f.Status == core.Violation {
					t.Errorf("%s must never be a Violation, got status=%v detail=%s", ruleID, f.Status, f.Detail)
				}
			}
		}
		if !found {
			t.Errorf("%s: expected info finding for periodic snapshot with anchor<=last_applied, got none", ruleID)
		}
	})

	t.Run("fires_when_anchor_gt_last_applied", func(t *testing.T) {
		// The rule requires st.snapCount > 0, so we feed an initial snapshot first.
		//
		// Start mktdata frames at seq=10 so that highestMktdataSeq=10 after the
		// delta. lastInstrSeq (per-instrument) will be 1. Using anchor=5 then gives
		// anchor(5) <= mktHigh(10) (no SNAP.ANCHOR_IS_MKTDATA_SEQ) and
		// anchor(5) > lastApplied_per_instr(1) → re-bootstrap branch.
		e, ic := newIntegrationEngine(2)
		feedRefDataOneInstr(e, ch, instrID, 1)
		e.Flush()

		// Initial cold-start snapshot (empty book, K=0, anchor=0) → snapCount=1.
		// Use snapshot seq starting at 1, but mktdata frames start at seq=10 so
		// the two ports do not interfere.
		snapSeq := feedInitialSnapshot(e, ch, instrID, 0 /*anchor*/, 9 /*snapID*/, 1)
		e.Flush()

		mktSeq := uint64(10)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 1, 3001, 100, 5, mktSeq)
		e.Flush()

		_ = mktSeq
		snapOrders := [][]byte{
			buildSnapOrderForInteg(ch, 20, 3001, 100, 5),
		}
		// anchor=5 > lastApplied_per_instr(perInstrSeq=1); anchor=5 <= mktHigh=10.
		feedPeriodicSnapshot(e, ch, instrID, 5 /*anchor*/, 20, 1 /*K=1*/, snapOrders, snapSeq)
		e.Flush()

		// Assert no must-Violations in this conformant subtest.
		if mvs := mustViolations(ic); len(mvs) != 0 {
			t.Errorf("anchor_gt subtest: unexpected must-Violations:")
			for _, f := range mvs {
				t.Errorf("  rule=%s detail=%s", f.RuleID, f.Detail)
			}
		}

		found := false
		for _, f := range ic.findings {
			if f.RuleID == ruleID {
				found = true
				if f.Status == core.Violation {
					t.Errorf("%s must never be a Violation, got status=%v detail=%s", ruleID, f.Status, f.Detail)
				}
			}
		}
		if !found {
			t.Errorf("%s: expected info finding for periodic snapshot with anchor>last_applied, got none", ruleID)
		}
	})

	t.Run("silent_for_cold_start_no_deltas", func(t *testing.T) {
		// No deltas before the snapshot → lastInstrSeq == nil AND snapCount == 0 →
		// rule must NOT fire for either guard reason.
		e, ic := newIntegrationEngine(2)
		feedRefDataOneInstr(e, ch, instrID, 1)
		e.Flush()

		// Snapshot with no prior deltas (lastInstrSeq=nil, snapCount=0).
		feedPeriodicSnapshot(e, ch, instrID, 1 /*anchor*/, 30, 0 /*K=0*/, nil, 1)
		e.Flush()

		for _, f := range ic.findings {
			if f.RuleID == ruleID {
				t.Errorf("%s: must not fire when no deltas seen (cold start), got detail=%s", ruleID, f.Detail)
			}
		}
	})

	t.Run("silent_for_recovery_snapshot", func(t *testing.T) {
		// A recovery snapshot following InstrumentReset must NOT trigger this info
		// rule. onInstrumentReset creates a fresh instrTracker (lastInstrSeq=nil) and
		// the snapTracker.snapCount remains 0 if no prior snapshot completed in this
		// era. Both guards in onSnapGroupComplete (lastInstrSeq != nil AND
		// st.snapCount > 0) therefore suppress the emission for recovery snapshots.
		e, ic := newIntegrationEngine(2)
		feedRefDataOneInstr(e, ch, instrID, 1)
		e.Flush()

		// Feed 3 deltas on gapless mktdata seqs 1,2,3 (lastInstrSeq=3).
		// Use feedMktdataOrderAdd to get distinct orderIDs (buildOrderAddFrame always
		// uses orderID=0 which would trigger REF.DUPLICATE_LIVE_ORDERADD here).
		mktSeq := uint64(1)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 1, 5001, 100, 1, mktSeq)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 2, 5002, 200, 1, mktSeq)
		mktSeq = feedMktdataOrderAdd(e, ch, instrID, 3, 5003, 300, 1, mktSeq)
		_ = mktSeq
		e.Flush()

		// InstrumentReset at mktdata seq=4 (New Anchor Seq=4).
		const resetAnchor = uint64(4)
		runStream(e, []streamEntry{
			mktdataEntrySeq(buildInstrumentResetFrame(ch, instrID, resetAnchor), resetAnchor),
		})

		// Recovery snapshot (anchor=4 matches reset anchor, K=3).
		// This satisfies awaitingRecovery — it is a recovery snapshot, not periodic.
		// The info rule must NOT fire.
		runStream(e, []streamEntry{
			snapEntrySeq(buildSnapBeginFull(ch, instrID, resetAnchor, 0 /*noOrders*/, 40, 3 /*K=3*/), 1),
			snapEntrySeq(buildSnapEndFull(ch, instrID, resetAnchor, 40), 2),
		})

		for _, f := range ic.findings {
			if f.RuleID == ruleID {
				t.Errorf("%s: must not fire for recovery snapshot (awaitingRecovery was true), got detail=%s", ruleID, f.Detail)
			}
		}
		// Also verify no must-Violations from recovery handling itself.
		if mvs := mustViolations(ic); len(mvs) != 0 {
			t.Errorf("recovery subtest: unexpected must-Violations:")
			for _, f := range mvs {
				t.Errorf("  rule=%s detail=%s", f.RuleID, f.Detail)
			}
		}
	})
}
