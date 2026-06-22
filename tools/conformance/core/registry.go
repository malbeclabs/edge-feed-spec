package core

type RuleMeta struct {
	ID          string
	Severity    Severity
	Tier        int // 1 or 2
	State       StateKind
	Feeds       []Feed
	Conditional bool // must only when its --expect-* config is set; else info/skip
}

var (
	allFeeds = []Feed{FeedTOB, FeedMidpoint, FeedMBO}
	mboOnly  = []Feed{FeedMBO}
	tobOnly  = []Feed{FeedTOB}
	midOnly  = []Feed{FeedMidpoint}
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
	{"MSG.RESERVED_TYPE_0X03_0X05", Should, 1, StateNone, mboOnly, false},
	{"RESERVED.FIELD_BITS_ZERO", Should, 1, StateNone, mboOnly, false},
	{"HEARTBEAT.CHANNEL_ID_MATCH", Should, 1, StateNone, allFeeds, false},
	{"FIELD.SIDE_ENUM", Should, 1, StateNone, mboOnly, false},
	{"FIELD.AGGRESSOR_SIDE_ENUM", Info, 1, StateNone, mboOnly, false},
	{"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME", Must, 1, StateNone, mboOnly, false},
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
	{"TOB.QUOTE.CROSSED_LOCKED", Should, 1, StateNone, tobOnly, false},
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

var byID = func() map[string]RuleMeta {
	m := make(map[string]RuleMeta, len(Rules))
	for _, r := range Rules {
		m[r.ID] = r
	}
	return m
}()

func Lookup(id string) (RuleMeta, bool) { r, ok := byID[id]; return r, ok }
