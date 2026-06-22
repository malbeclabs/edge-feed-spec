package engine

// coverage_test.go — Rule-coverage guard (Task 28).
//
// Strategy: deterministic curated map (ruleID → test function name).
// This is order-independent and does not rely on test execution sequencing.
// The guard fails loudly listing any rule missing from the map, and fails
// again if the map contains a rule ID not present in core.Rules (stale entry).
//
// Maintenance contract: whenever a new rule is added to core.Rules, add a
// corresponding entry here and a real test that fires on a violating input and
// stays silent on a conformant input.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// testedRules maps every rule ID in core.Rules to the test function (or helper)
// that exercises it with at least one conformant and one violating case.
// Key: rule ID (must match core.Rules exactly).
// Value: test function or descriptive reference (for human reference only).
var testedRules = map[string]string{
	// --- Frame & message structure ---
	"FRAME.MAGIC_MISMATCH":              "TestTier1Rules/FRAME.MAGIC_MISMATCH (tier1_test.go)",
	"FRAME.SCHEMA_VERSION":              "TestTier1Rules/FRAME.SCHEMA_VERSION (tier1_test.go)",
	"FRAME.MSG_COUNT_RANGE":             "TestTier1Rules/FRAME.MSG_COUNT_RANGE (tier1_test.go)",
	"FRAME.LENGTH_CONSISTENCY":          "TestTier1Rules/FRAME.LENGTH_CONSISTENCY (tier1_test.go)",
	"MSG.LENGTH_PER_TYPE":               "TestTier1Rules/MSG.LENGTH_PER_TYPE (tier1_test.go)",
	"MSG.WRONG_PORT_PLACEMENT":          "TestTier1Rules/MSG.WRONG_PORT_PLACEMENT (tier1_test.go)",
	"MSG.UNKNOWN_TYPE_SKIPPED":          "TestTier1Rules/MSG.UNKNOWN_TYPE_SKIPPED (tier1_test.go)",
	"MSG.RESERVED_TYPE_0X03_0X05":       "TestTier1Rules/MSG.RESERVED_TYPE_0X03_0X05 (tier1_test.go)",
	"RESERVED.FIELD_BITS_ZERO":          "TestTier1Rules/RESERVED.FIELD_BITS_ZERO (tier1_test.go)",
	"HEARTBEAT.CHANNEL_ID_MATCH":        "TestTier1Rules/HEARTBEAT.CHANNEL_ID_MATCH (tier1_test.go)",
	"FIELD.SIDE_ENUM":                   "TestTier1Rules/FIELD.SIDE_ENUM (tier1_test.go)",
	"FIELD.AGGRESSOR_SIDE_ENUM":         "TestTier1Rules/FIELD.AGGRESSOR_SIDE_ENUM (tier1_test.go)",
	"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME": "TestTier1Rules/RESET.ANCHOR_SEQ_IS_CURRENT_FRAME (tier1_test.go)",
	"FIELD.QTY_POSITIVE":                "TestTier1Rules/FIELD.QTY_POSITIVE (tier1_test.go)",
	"SNAP.ORDER_STRUCT_VALID":           "TestTier1Rules/SNAP.ORDER_STRUCT_VALID (tier1_test.go)",

	// --- MBO delta sequencing (detector, counters) ---
	"FRAME.SEQ_DUP_DIVERGENT":          "TestDupDivergent (state_test.go)",
	"DELTA.PERINSTR_DENSITY":           "TestPerInstrDensityJumpGaplessChannel (gate_test.go)",
	"DELTA.PERINSTR_FIRST_VALUE":       "TestPerInstrFirstValueColdStart / TestInstrumentResetFirstValueViolation (gate_test.go)",
	"DELTA.PERINSTR_NO_SNAPSHOT_RESET": "TestDeltaPerInstrNoSnapshotReset (mbo_detector_test.go)",
	"DELTA.PERINSTR_DUP_DIVERGENT":     "TestDeltaPerInstrDupDivergent (mbo_detector_test.go)",
	"DELTA.PERINSTR_WRAP_BEFORE_RESET": "TestDeltaPerInstrWrapBeforeReset (mbo_detector_test.go)",
	"FRAME.MKTDATA_SEQ_START":          "TestFrameMktdataSeqStart (mbo_detector_test.go)",
	"FRAME.SEND_TS_MONOTONIC":          "TestSendTSMonotonic (state_test.go)",
	"HEARTBEAT.CADENCE":                "TestHeartbeatCadenceViolation (cadence_test.go)",
	"BATCH.ID_MONOTONIC":               "TestBatchIDMonotonic (mbo_detector_test.go)",
	"FRAME.SEQ_RESET_GAP":              "TestSeqResetGapViolation (state_test.go)",

	// --- MBO referential integrity (consumer, order_id_set) ---
	"REF.EXEC_DANGLING_ORDER":     "TestExecDanglingOrder (mbo_ref_test.go)",
	"REF.CANCEL_DANGLING_ORDER":   "TestCancelDanglingOrder (mbo_ref_test.go)",
	"REF.DUPLICATE_LIVE_ORDERADD": "TestDuplicateLiveOrderAdd (mbo_ref_test.go)",
	"REF.OPERATION_AFTER_REMOVAL": "TestOperationAfterRemoval (mbo_ref_test.go)",
	"REF.SIDE_PRICE_CONSISTENCY":  "TestSidePriceConsistency (mbo_ref_test.go)",
	"FIELD.SOURCE_ID_CONSISTENCY": "TestSourceIDConsistency (mbo_ref_test.go)",
	"TRADE.EXEC_GROUPING":         "TestTradeExecGrouping (mbo_ref_test.go)",

	// --- MBO quantity conservation (consumer, full_book) ---
	"REF.EXEC_OVERFILL":              "TestExecOverfill (mbo_qty_test.go)",
	"REF.FULLFILL_FLAG_DISAGREEMENT": "TestFullFillFlagDisagreement (mbo_qty_test.go)",
	"BATCH.ATOMICITY_CONSISTENCY":    "TestBatchAtomicityConsistency (mbo_qty_test.go)",

	// --- MBO price-bound (consumer, refdata) ---
	"FIELD.ORDERADD_PRICE_BOUND": "TestOrderAddPriceBound (mbo_ref_test.go)",
	"REF.EXEC_PRICE_BOUND":       "TestExecPriceBound (mbo_ref_test.go)",
	"SNAP.ORDER_PRICE_BOUND":     "TestSnapGroupOrderPriceBound (mbo_snapgroup_test.go)",

	// --- MBO snapshot & recovery ---
	"SNAP.BEGIN_ORDER_END_GROUPING":                   "TestSnapGroupInterleave / TestSnapGroupOrderWithoutBegin / TestSnapGroupEndWithoutBegin (mbo_snapgroup_test.go)",
	"SNAP.TOTAL_ORDERS_COUNT_MATCH":                   "TestSnapGroupOverCount / TestSnapGroupUnderCount (mbo_snapgroup_test.go)",
	"SNAP.END_FIELDS_MATCH_BEGIN":                     "TestSnapGroupEndFieldsMismatch (mbo_snapgroup_test.go)",
	"SNAP.ORDER_SNAPSHOT_ID_MATCH":                    "TestSnapGroupOrderSnapIDMismatch (mbo_snapgroup_test.go)",
	"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID":             "TestSnapGroupDupOrderID (mbo_snapgroup_test.go)",
	"SNAP.EMPTY_BOOK_WELL_FORMED":                     "TestSnapGroupEmptyBookViolation (mbo_snapgroup_test.go)",
	"SNAP.ANCHOR_IS_MKTDATA_SEQ":                      "TestSnapAnchorIsMktdataSeq (mbo_detector_test.go)",
	"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT":            "TestSnapAnchorMonotonicPerInstrument (mbo_detector_test.go)",
	"SNAP.SNAPSHOT_ID_MONOTONIC":                      "TestSnapSnapshotIDMonotonic (mbo_detector_test.go)",
	"SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS": "TestSnapLastInstrumentSeqConsistentWithDeltas (mbo_detector_test.go)",
	"SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING":      "TestAnchorLeOrGtLastAppliedHandling (mbo_integration_test.go)",
	"SNAP.ROUND_ROBIN_COVERS_MANIFEST":                "TestRoundRobinViolationWhenInstrumentMissing (mbo_roundrobin_test.go)",
	"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":        "TestOracleMismatchSuspectedThenConfirmedFixedK (oracle_test.go)",
	"RESET.SNAPSHOT_FOLLOWS":                          "TestResetNoRecoverySnapshot (mbo_reset_test.go)",
	"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET":    "TestResetRecoveryWrongAnchor (mbo_reset_test.go)",
	"RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR":     "TestResetNoDanglingDeltasAtOrBelowAnchor (mbo_detector_test.go)",

	// --- Reference-data supplement (all feeds) ---
	"REFDATA.MANIFEST_CADENCE":                "TestManifestCadenceViolation (cadence_test.go)",
	"REFDATA.DEFINITION_CYCLE_COVERAGE":       "TestDefinitionCycleCoverage (cadence_test.go)",
	"REFDATA.COUNT_VS_DISTINCT_DEFS":          "TestRefdataCountVsDistinctDefs (refdata_test.go)",
	"REFDATA.SET_CHANGE_NO_SEQ_BUMP":          "TestRefdataSetChangeNoSeqBump (refdata_test.go)",
	"REFDATA.COUNT_CHANGE_NO_SEQ_BUMP":        "TestRefdataCountChangeNoSeqBump (refdata_test.go)",
	"REFDATA.STALE_SEQ_TAG_AFTER_BUMP":        "TestRefdataStaleSeqTagAfterBump (refdata_test.go)",
	"REFDATA.VALID_FLAG_WHILE_SERVING":        "TestRefdataValidFlagWhileServing (refdata_test.go)",
	"REFDATA.NEVER_REACHES_READY":             "TestNeverReachesReady (cadence_test.go)",
	"REFDATA.NO_BURST_DEFINITIONS":            "TestNoBurstDefinitions (cadence_test.go)",
	"REFDATA.SEQ_MONOTONIC_NO_REGRESS":        "TestRefdataSeqMonotonicNoRegress (refdata_test.go)",
	"REFDATA.SEQ_BUMP_NOT_BY_ONE":             "TestRefdataSeqBumpNotByOne (refdata_test.go)",
	"REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID": "TestRefdataManifestSeqNonzeroWhenValid (refdata_test.go)",
	"MANIFEST.STATE_MACHINE":                  "TestRefdataManifestStateMachine (refdata_test.go)",

	// --- Top-of-Book ---
	"TOB.QUOTE.STRUCT_LEN_TYPE":        "TestTier1Rules/TOB.QUOTE.STRUCT_LEN_TYPE (tier1_test.go)",
	"TOB.QUOTE.GONE_VS_ZERO_PRICE":     "TestTier1Rules/TOB.QUOTE.GONE_VS_ZERO_PRICE (tier1_test.go)",
	"TOB.QUOTE.CROSSED_LOCKED":         "TestTier1Rules/TOB.QUOTE.CROSSED_LOCKED (tier1_test.go)",
	"TOB.QUOTE.UPDATE_FLAGS_COHERENCE": "TestTier1Rules/TOB.QUOTE.UPDATE_FLAGS_COHERENCE (tier1_test.go)",
	"TOB.QUOTE.SOURCE_ID_REGISTRY":     "TestTier1Rules/TOB.QUOTE.SOURCE_ID_REGISTRY (tier1_test.go)",
	"TOB.QUOTE.SOURCE_COUNT":           "TestTier1Rules/TOB.QUOTE.SOURCE_COUNT (tier1_test.go)",
	"TOB.QUOTE.REFDATA_KNOWN":          "TestTOBRefdataKnownAfterReadyUnknownInstrument (tob_test.go)",
	"TOB.TRADE.FIELDS":                 "TestTier1Rules/TOB.TRADE.FIELDS (tier1_test.go)",

	// --- Midpoint ---
	"MID.STRUCT_LEN_TYPE":          "TestTier1Rules/MID.STRUCT_LEN_TYPE (tier1_test.go)",
	"MID.METHOD_RANGE":             "TestTier1Rules/MID.METHOD_RANGE (tier1_test.go)",
	"MID.QUALITY_FLAGS":            "TestTier1Rules/MID.QUALITY_FLAGS (tier1_test.go)",
	"MID.TIMESTAMP_ORDERING":       "TestTier1Rules/MID.TIMESTAMP_ORDERING (tier1_test.go)",
	"MID.METHOD0_REQUIRES_DEFAULT": "TestMidMethod0RequiresDefaultFires (midpoint_test.go)",
	"MID.PRICE_BOUND":              "TestMidPriceBound1NegativePrice (midpoint_test.go)",
}

// TestRuleCoverage asserts that every rule in core.Rules has an entry in
// testedRules, and that every entry in testedRules refers to a valid rule in
// core.Rules (no stale entries). Both failures are reported loudly.
func TestRuleCoverage(t *testing.T) {
	// Build set of rule IDs from core.Rules.
	registryIDs := make(map[string]bool, len(core.Rules))
	for _, r := range core.Rules {
		registryIDs[r.ID] = true
	}

	var missing []string // rules in registry but not in testedRules
	var stale []string   // entries in testedRules but not in registry

	// Check every registry rule is in the map.
	for _, r := range core.Rules {
		if _, ok := testedRules[r.ID]; !ok {
			missing = append(missing, r.ID)
		}
	}

	// Check for stale entries in the map.
	for id := range testedRules {
		if !registryIDs[id] {
			stale = append(stale, id)
		}
	}

	sort.Strings(missing)
	sort.Strings(stale)

	var msgs []string
	if len(missing) > 0 {
		msgs = append(msgs, fmt.Sprintf(
			"MISSING: %d rule(s) in core.Rules have no entry in testedRules:\n  %s",
			len(missing),
			strings.Join(missing, "\n  "),
		))
	}
	if len(stale) > 0 {
		msgs = append(msgs, fmt.Sprintf(
			"STALE: %d entry(ies) in testedRules do not exist in core.Rules (remove them):\n  %s",
			len(stale),
			strings.Join(stale, "\n  "),
		))
	}
	if len(msgs) > 0 {
		t.Errorf("rule coverage guard failed:\n%s", strings.Join(msgs, "\n"))
	}

	// Summary.
	t.Logf("rule coverage: %d/%d rules covered", len(core.Rules)-len(missing), len(core.Rules))
}
