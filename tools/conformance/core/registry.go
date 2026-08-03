package core

import (
	"slices"
	"sort"
)

type RuleMeta struct {
	ID          string
	Severity    Severity
	Tier        int // 1 or 2
	State       StateKind
	Feeds       []Feed
	Conditional bool // must only when its --expect-* config is set; else info/skip
}

var (
	allFeeds = []Feed{FeedTOB, FeedMidpoint, FeedMBO, FeedMBP}
	mboOnly  = []Feed{FeedMBO}
	tobOnly  = []Feed{FeedTOB}
	midOnly  = []Feed{FeedMidpoint}
	mbpOnly  = []Feed{FeedMBP}
	// mboMBP is for rules the two snapshot/delta feeds state identically. A rule
	// listed mboOnly while its emit path fires for another feed produces findings
	// with no matching rule_info row, and the Grafana join drops them silently.
	mboMBP = []Feed{FeedMBO, FeedMBP}
)

// Rules is the source of truth for the rule registry, transcribed from the
// design spec's Appendix: Conformance Rule Catalog. One entry per catalog row.
var Rules = []RuleMeta{
	// --- Frame & message structure (shared header rules apply to all feeds by magic) ---
	{"FRAME.MAGIC_MISMATCH", Must, 1, StateNone, allFeeds, false},
	{"FRAME.SCHEMA_VERSION", Info, 1, StateNone, allFeeds, false},
	{"FRAME.MSG_COUNT_RANGE", Must, 1, StateNone, allFeeds, false},
	{"FRAME.LENGTH_CONSISTENCY", Must, 1, StateNone, allFeeds, false},
	{"MSG.LENGTH_PER_TYPE", Must, 1, StateNone, allFeeds, false},
	{"MSG.WRONG_PORT_PLACEMENT", Must, 1, StateNone, allFeeds, false},
	{"MSG.UNKNOWN_TYPE_SKIPPED", Info, 1, StateNone, allFeeds, false},
	{"MSG.SNAPSHOT_FLAG_MATCHES_PORT", Must, 1, StateNone, mbpOnly, false},
	{"MSG.RESERVED_TYPE_0X03_0X05", Should, 1, StateNone, mboOnly, false},
	{"RESERVED.FIELD_BITS_ZERO", Should, 1, StateNone, mboOnly, false},
	{"HEARTBEAT.CHANNEL_ID_MATCH", Should, 1, StateNone, allFeeds, false},
	{"FIELD.SIDE_ENUM", Should, 1, StateNone, mboOnly, false},
	{"FIELD.AGGRESSOR_SIDE_ENUM", Info, 1, StateNone, mboOnly, false},
	// InstrumentReset is byte-identical across the two feeds and both specs state
	// this requirement in the same words (market-by-order §recovery step 1,
	// market-by-price the same), and tier1's InstrumentReset case is not feed-gated —
	// so the registry has to say mbp too.
	{"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME", Must, 1, StateNone, mboMBP, false},
	{"FIELD.QTY_POSITIVE", Should, 1, StateNone, mboOnly, false},
	{"SNAP.ORDER_STRUCT_VALID", Should, 1, StateNone, mboOnly, false},
	// --- MBO delta sequencing (detector, counters) ---
	{"FRAME.SEQ_DUP_DIVERGENT", Must, 2, StateCounters, allFeeds, false},
	{"DELTA.PERINSTR_DENSITY", Must, 2, StateCounters, mboOnly, false},
	{"DELTA.PERINSTR_FIRST_VALUE", Should, 2, StateCounters, mboOnly, false},
	{"DELTA.PERINSTR_NO_SNAPSHOT_RESET", Must, 2, StateCounters, mboOnly, false},
	{"DELTA.PERINSTR_DUP_DIVERGENT", Must, 2, StateCounters, mboOnly, false},
	{"DELTA.PERINSTR_WRAP_BEFORE_RESET", Info, 2, StateCounters, mboOnly, false},
	{"FRAME.MKTDATA_SEQ_START", Should, 2, StateCounters, mboOnly, false},
	{"FRAME.SEND_TS_MONOTONIC", Info, 2, StateCounters, allFeeds, false},
	{"HEARTBEAT.CADENCE", Info, 2, StateCounters, allFeeds, true},
	{"BATCH.ID_MONOTONIC", Should, 2, StateCounters, mboOnly, false},
	{"FRAME.SEQ_RESET_GAP", Must, 2, StateCounters, allFeeds, false},
	// --- Market-by-price: per-instrument sequencing and the reconstruction oracle ---
	{"MBP.DELTA.PERINSTR_DENSITY", Must, 2, StateCounters, mbpOnly, false},
	{"MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET", Must, 2, StateCounters, mbpOnly, false},
	{"MBP.DELTA.ABSOLUTE_APPLY", Must, 2, StateCounters, mbpOnly, false},
	{"MBP.SNAP.GROUP_STRUCTURE", Must, 2, StateSnapshotGroup, mbpOnly, false},
	{"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", Must, 2, StateFullBook, mbpOnly, false},
	// --- MBO referential integrity (consumer, order_id_set) ---
	{"REF.EXEC_DANGLING_ORDER", Must, 2, StateOrderIDSet, mboOnly, false},
	{"REF.CANCEL_DANGLING_ORDER", Must, 2, StateOrderIDSet, mboOnly, false},
	{"REF.DUPLICATE_LIVE_ORDERADD", Must, 2, StateOrderIDSet, mboOnly, false},
	{"REF.OPERATION_AFTER_REMOVAL", Must, 2, StateOrderIDSet, mboOnly, false},
	{"REF.SIDE_PRICE_CONSISTENCY", Should, 2, StateOrderIDSet, mboOnly, false},
	{"FIELD.SOURCE_ID_CONSISTENCY", Info, 2, StateOrderIDSet, mboOnly, false},
	{"TRADE.EXEC_GROUPING", Info, 2, StateOrderIDSet, mboOnly, false},
	// --- MBO quantity conservation (consumer, full_book) ---
	{"REF.EXEC_OVERFILL", Must, 2, StateFullBook, mboOnly, false},
	{"REF.FULLFILL_FLAG_DISAGREEMENT", Must, 2, StateFullBook, mboOnly, false},
	{"BATCH.ATOMICITY_CONSISTENCY", Should, 2, StateFullBook, mboOnly, false},
	// --- MBO price-bound (consumer, refdata) ---
	{"FIELD.ORDERADD_PRICE_BOUND", Should, 2, StateRefdata, mboOnly, false},
	{"REF.EXEC_PRICE_BOUND", Should, 2, StateRefdata, mboOnly, false},
	{"SNAP.ORDER_PRICE_BOUND", Should, 2, StateRefdata, mboOnly, false},
	// --- MBO snapshot & recovery ---
	{"SNAP.BEGIN_ORDER_END_GROUPING", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.TOTAL_ORDERS_COUNT_MATCH", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.END_FIELDS_MATCH_BEGIN", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.ORDER_SNAPSHOT_ID_MATCH", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.EMPTY_BOOK_WELL_FORMED", Info, 2, StateSnapshotGroup, mboOnly, false},
	{"SNAP.ANCHOR_IS_MKTDATA_SEQ", Must, 2, StateCounters, mboOnly, false},
	{"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT", Should, 2, StateCounters, mboOnly, false},
	{"SNAP.SNAPSHOT_ID_MONOTONIC", Should, 2, StateCounters, mboOnly, false},
	{"SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS", Must, 2, StateCounters, mboOnly, false},
	{"SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING", Info, 2, StateCounters, mboOnly, false},
	{"SNAP.ROUND_ROBIN_COVERS_MANIFEST", Should, 2, StateRefdata, mboOnly, false},
	{"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", Must, 2, StateFullBook, mboOnly, false},
	{"RESET.SNAPSHOT_FOLLOWS", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET", Must, 2, StateSnapshotGroup, mboOnly, false},
	{"RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR", Must, 2, StateCounters, mboOnly, false},
	// --- Reference-data supplement (all feeds) ---
	{"REFDATA.MANIFEST_CADENCE", Must, 2, StateCounters, allFeeds, true},
	{"REFDATA.DEFINITION_CYCLE_COVERAGE", Must, 2, StateRefdata, allFeeds, true},
	{"REFDATA.COUNT_VS_DISTINCT_DEFS", Must, 2, StateRefdata, allFeeds, false},
	{"REFDATA.SET_CHANGE_NO_SEQ_BUMP", Must, 2, StateRefdata, allFeeds, false},
	{"REFDATA.COUNT_CHANGE_NO_SEQ_BUMP", Must, 2, StateCounters, allFeeds, false},
	{"REFDATA.STALE_SEQ_TAG_AFTER_BUMP", Must, 2, StateRefdata, allFeeds, false},
	{"REFDATA.VALID_FLAG_WHILE_SERVING", Must, 2, StateRefdata, allFeeds, false},
	{"REFDATA.NEVER_REACHES_READY", Must, 2, StateRefdata, allFeeds, true},
	{"REFDATA.NO_BURST_DEFINITIONS", Should, 2, StateRefdata, allFeeds, true},
	{"REFDATA.SEQ_MONOTONIC_NO_REGRESS", Should, 2, StateCounters, allFeeds, false},
	{"REFDATA.SEQ_BUMP_NOT_BY_ONE", Should, 2, StateCounters, allFeeds, false},
	{"REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID", Should, 2, StateRefdata, allFeeds, false},
	{"MANIFEST.STATE_MACHINE", Should, 2, StateRefdata, allFeeds, false},
	// --- Top-of-Book ---
	{"TOB.QUOTE.STRUCT_LEN_TYPE", Must, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.GONE_VS_ZERO_PRICE", Must, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.CROSSED_LOCKED", Info, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.UPDATE_FLAGS_COHERENCE", Should, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.SOURCE_ID_REGISTRY", Must, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.SOURCE_COUNT", Info, 1, StateNone, tobOnly, false},
	{"TOB.QUOTE.REFDATA_KNOWN", Should, 2, StateRefdata, tobOnly, false},
	{"TOB.TRADE.FIELDS", Must, 1, StateNone, tobOnly, false},
	// --- Midpoint ---
	{"MID.STRUCT_LEN_TYPE", Must, 1, StateNone, midOnly, false},
	{"MID.METHOD_RANGE", Info, 1, StateNone, midOnly, false},
	{"MID.QUALITY_FLAGS", Should, 1, StateNone, midOnly, false},
	{"MID.TIMESTAMP_ORDERING", Should, 1, StateNone, midOnly, false},
	{"MID.METHOD0_REQUIRES_DEFAULT", Must, 2, StateRefdata, midOnly, false},
	{"MID.PRICE_BOUND", Must, 2, StateRefdata, midOnly, false},
}

// conditionalExec lists the rules whose *execution* is conditional: they run only
// when their preconditions line up, so silence from one of them is ambiguous —
// full coverage and never having run once produce the same empty output.
//
// Each of these MUST account for every opportunity it gets, with a `pass` when the
// property held and an `unverifiable`/`na` naming what stopped it otherwise. That
// makes `checks_total{rule_id}` the rule's denominator. See
// engine/denominator.go for the invariant and `TestConditionalRulesReportADenominator`
// for its enforcement.
//
// **Not the same as `RuleMeta.Conditional`**, which means the rule is off until its
// `--expect-*` flag is set. That is a static fact, visible in `rule_info` before a
// frame arrives; this is about stream state.
var conditionalExec = map[string]struct{}{
	// The two reconstruction oracles: comparable only once a baseline ladder/book
	// exists and the delta side has been applied through the snapshot's K.
	"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT": {},
	"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":     {},
	// Snapshot-group structure: decided only when a group completes.
	"MBP.SNAP.GROUP_STRUCTURE": {},
	// Refdata-gated: need the instrument's definition before they can judge.
	"FIELD.ORDERADD_PRICE_BOUND":   {},
	"REF.EXEC_PRICE_BOUND":         {},
	"SNAP.ORDER_PRICE_BOUND":       {},
	"TOB.QUOTE.REFDATA_KNOWN":      {},
	"MID.METHOD0_REQUIRES_DEFAULT": {},
	"MID.PRICE_BOUND":              {},
	// Reset recovery: nothing to judge until an InstrumentReset has happened, so on a
	// stream with no reset these are silent — and used to be silent in the same way
	// when every recovery was correct.
	"RESET.SNAPSHOT_FOLLOWS":                       {},
	"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET": {},
	"RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR":  {},
	// Monotonicity across successive snapshots: needs a predecessor, so a capture
	// with one snapshot per instrument never runs them.
	"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT": {},
	"SNAP.SNAPSHOT_ID_MONOTONIC":           {},
	// End-of-window rules: need enough observed cycles to conclude anything.
	"SNAP.ROUND_ROBIN_COVERS_MANIFEST":  {},
	"REFDATA.DEFINITION_CYCLE_COVERAGE": {},
	"REFDATA.NO_BURST_DEFINITIONS":      {},
	"REFDATA.NEVER_REACHES_READY":       {},
}

// ConditionalExec reports whether the rule's execution is conditional, and so
// whether it owes a per-opportunity denominator.
func ConditionalExec(id string) bool { _, ok := conditionalExec[id]; return ok }

// snapshotDriven lists the rules whose every emit path runs off a snapshot-port
// frame, so with `--snapshot-port` unset they cannot report *anything* — not even
// the unverifiable that the denominator invariant would otherwise require.
//
// **That is a hole the invariant cannot close by itself**, and it is worth being
// precise about why. A refdata-gated rule with no `--refdata-port` still reports:
// it is driven by mktdata messages and merely *gated* on reference data, so every
// message it cannot judge becomes an `unverifiable`/`cold_start`. These rules have
// no such driver. No snapshot frames means no code path executes, so zero
// opportunities, so silence — and silence reads as a clean pass. Concretely: run a
// market-by-price feed without `--snapshot-port`, fix whatever unrelated violation
// the run reported, re-run the same command, and the tool exits 0 with an empty
// report while every MBP.SNAP.* rule was starved.
//
// So the CLI reports them as NA at startup instead, from configuration rather than
// from traffic. See reportStarvedRules in run.go, and
// TestStarvedRulesAreExactlyTheSnapshotDrivenOnes, which proves each entry
// disappears without the port and appears with it.
//
// `RESET.SNAPSHOT_FOLLOWS` is deliberately absent: it also emits from the mktdata
// path and from EndRun, where it already downgrades to NA on an unbound snapshot
// port, so listing it would double-report.
var snapshotDriven = map[string]struct{}{
	"MBP.SNAP.GROUP_STRUCTURE":                        {},
	"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":    {},
	"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET":    {},
	"SNAP.ANCHOR_IS_MKTDATA_SEQ":                      {},
	"SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING":      {},
	"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT":            {},
	"SNAP.BEGIN_ORDER_END_GROUPING":                   {},
	"SNAP.EMPTY_BOOK_WELL_FORMED":                     {},
	"SNAP.END_FIELDS_MATCH_BEGIN":                     {},
	"SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS": {},
	"SNAP.ORDER_PRICE_BOUND":                          {},
	"SNAP.ORDER_SNAPSHOT_ID_MATCH":                    {},
	"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":        {},
	"SNAP.SNAPSHOT_ID_MONOTONIC":                      {},
	"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID":             {},
	"SNAP.TOTAL_ORDERS_COUNT_MATCH":                   {},
}

// SnapshotDrivenRules returns the rule IDs applicable to feed that can only ever
// fire from a snapshot-port frame — the rules an operator silently loses by leaving
// --snapshot-port unset. Sorted, so callers report them deterministically.
func SnapshotDrivenRules(feed Feed) []string {
	var out []string
	for _, r := range Rules {
		if _, ok := snapshotDriven[r.ID]; !ok {
			continue
		}
		if slices.Contains(r.Feeds, feed) {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out
}

// ConditionalExecRules returns the conditional-execution rule IDs applicable to
// feed, so a test can assert each one reported a denominator.
func ConditionalExecRules(feed Feed) []string {
	var out []string
	for _, r := range Rules {
		if !ConditionalExec(r.ID) {
			continue
		}
		for _, f := range r.Feeds {
			if f == feed {
				out = append(out, r.ID)
				break
			}
		}
	}
	return out
}

var byID = func() map[string]RuleMeta {
	m := make(map[string]RuleMeta, len(Rules))
	for _, r := range Rules {
		m[r.ID] = r
	}
	return m
}()

func Lookup(id string) (RuleMeta, bool) { r, ok := byID[id]; return r, ok }
