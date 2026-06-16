package core

import "strings"

// RuleDoc is the human-facing documentation for a rule. It is kept separate
// from RuleMeta so the machine-readable registry stays compact. Every rule in
// Rules must have a ruleDocs entry (enforced by TestEveryRuleDocumented).
//
// Today it carries only a one-line Summary — enough to label a rule in Grafana
// (via the dz_conformance_rule_info metric) and in reports. A longer
// Description for the standalone conformance report can be added later without
// touching call sites.
type RuleDoc struct {
	// Summary is a single scannable line stating what the publisher must do to
	// conform. Sourced from the design-spec rule catalog.
	Summary string
}

// Doc returns the documentation for a rule id.
func Doc(id string) (RuleDoc, bool) {
	d, ok := ruleDocs[id]
	return d, ok
}

const specBaseURL = "https://github.com/malbeclabs/edge-feed-spec/blob/main/"

// SpecURL returns a link to the authoritative edge-feed-spec document for a
// rule, derived from its category prefix. Surfaced as a Grafana data link so a
// non-conforming rule points at the requirement it violates.
func SpecURL(id string) string {
	prefix, _, _ := strings.Cut(id, ".")
	switch prefix {
	case "REFDATA", "MANIFEST":
		return specBaseURL + "reference-data/spec.md"
	case "TOB":
		return specBaseURL + "top-of-book/spec.md"
	case "MID":
		return specBaseURL + "midpoint/spec.md"
	case "FRAME", "MSG", "HEARTBEAT":
		// Shared frame header / message framing — documented canonically in the
		// top-of-book spec's transport-framing section.
		return specBaseURL + "top-of-book/spec.md"
	case "RESERVED", "FIELD", "RESET", "SNAP", "DELTA", "BATCH", "REF", "TRADE":
		return specBaseURL + "market-by-order/spec.md"
	default:
		return specBaseURL
	}
}

// ruleDocs holds the one-line summary for every rule in Rules. Transcribed from
// the design-spec Appendix: Conformance Rule Catalog. Keep entries to a single
// scannable sentence describing the conformant behaviour.
var ruleDocs = map[string]RuleDoc{
	// --- Frame & message structure ---
	"FRAME.MAGIC_MISMATCH":              {"Frame magic matches the feed (MBO 0x4444, TOB 0x445A, Midpoint 0x4D44)."},
	"FRAME.SCHEMA_VERSION":              {"Schema Version is 1; 0 or an unknown-higher value is flagged."},
	"FRAME.MSG_COUNT_RANGE":             {"Message Count is 1–255 and equals the messages actually present in the frame."},
	"FRAME.LENGTH_CONSISTENCY":          {"Declared Frame Length is 24–1232 bytes and equals 24 + the sum of contained message lengths."},
	"FRAME.SEQ_DUP_DIVERGENT":           {"A frame re-using a port sequence number carries identical payload; a divergent duplicate is non-conformant."},
	"FRAME.MKTDATA_SEQ_START":           {"On a Reset Count change, the mktdata sequence number restarts at 0."},
	"FRAME.SEND_TS_MONOTONIC":           {"Send Timestamp is non-decreasing as the per-port sequence increases."},
	"FRAME.SEQ_RESET_GAP":               {"Per-channel sequence is monotonic and resets to 0 only when Reset Count changes."},
	"MSG.LENGTH_PER_TYPE":               {"Each message type carries its mandated byte length (OrderAdd 52, Cancel 32, Execute 56, …)."},
	"MSG.WRONG_PORT_PLACEMENT":          {"A known message type only appears on the port it belongs to."},
	"MSG.UNKNOWN_TYPE_SKIPPED":          {"Unknown/reserved message types are skipped via Message Length and reported, never silently dropped."},
	"MSG.RESERVED_TYPE_0X03_0X05":       {"Reserved MBO type IDs 0x03 and 0x05 are never emitted."},
	"RESERVED.FIELD_BITS_ZERO":          {"Reserved flag bits and padding are zero (OrderAdd flags 5–7, Execute flags 2–7, named padding)."},
	"HEARTBEAT.CHANNEL_ID_MATCH":        {"A Heartbeat's embedded Channel ID matches the frame header's Channel ID."},
	"FIELD.SIDE_ENUM":                   {"OrderAdd.Side is 0 or 1."},
	"FIELD.AGGRESSOR_SIDE_ENUM":         {"OrderExecute/Trade Aggressor Side is 0, 1, or 2."},
	"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME": {"InstrumentReset.NewAnchorSeq equals the carrying mktdata frame's own sequence number."},
	"FIELD.QTY_POSITIVE":                {"OrderAdd.Quantity is greater than zero."},
	"SNAP.ORDER_STRUCT_VALID":           {"SnapshotOrder fields are valid: Side 0/1, reserved Order Flags bits 5–7 zero, Quantity > 0."},

	// --- MBO delta sequencing (counters) ---
	"DELTA.PERINSTR_DENSITY":           {"Per-Instrument Seq increments by exactly 1 per delta within an era; no skips."},
	"DELTA.PERINSTR_FIRST_VALUE":       {"The first delta for an instrument in an era carries Per-Instrument Seq = 1."},
	"DELTA.PERINSTR_NO_SNAPSHOT_RESET": {"Per-Instrument Seq does not restart at a snapshot — only on a Reset Count change."},
	"DELTA.PERINSTR_DUP_DIVERGENT":     {"A delta re-using a per-instrument seq carries identical payload; a divergent duplicate is non-conformant."},
	"DELTA.PERINSTR_WRAP_BEFORE_RESET": {"The u32 Per-Instrument Seq does not wrap within an era without a reset."},
	"HEARTBEAT.CADENCE":                {"Heartbeats arrive within the configured idle interval (enabled by --expect-heartbeat)."},
	"BATCH.ID_MONOTONIC":               {"BatchBoundary Batch ID is monotonic within a Reset Count era (forward skips allowed)."},

	// --- MBO referential integrity (live order-id set) ---
	"REF.EXEC_DANGLING_ORDER":     {"OrderExecute references an order currently live on the book."},
	"REF.CANCEL_DANGLING_ORDER":   {"OrderCancel references an order currently live on the book."},
	"REF.DUPLICATE_LIVE_ORDERADD": {"OrderAdd does not reuse a currently-live Order ID."},
	"REF.OPERATION_AFTER_REMOVAL": {"No execute or cancel arrives after an order was removed (full-fill or cancel)."},
	"REF.SIDE_PRICE_CONSISTENCY":  {"An execute's Aggressor Side is consistent with the resting order's Side."},
	"FIELD.SOURCE_ID_CONSISTENCY": {"A live order's Source ID stays stable across its lifecycle."},
	"TRADE.EXEC_GROUPING":         {"A Trade with a non-zero Trade ID has at least one matching OrderExecute."},

	// --- MBO quantity conservation (full book) ---
	"REF.EXEC_OVERFILL":              {"Execute Quantity ≤ resting remaining; cumulative fills ≤ entry quantity (hidden orders exempt)."},
	"REF.FULLFILL_FLAG_DISAGREEMENT": {"The full-fill flag is set exactly when an execute drives remaining quantity to zero (hidden exempt)."},
	"FIELD.ORDERADD_PRICE_BOUND":     {"OrderAdd.Price respects the instrument's Price Bound (e.g. [0,1] for binary outcomes)."},
	"REF.EXEC_PRICE_BOUND":           {"OrderExecute.Exec Price respects the instrument's Price Bound."},
	"BATCH.ATOMICITY_CONSISTENCY":    {"At each BatchBoundary the reconstructed book has no negative-remaining or orphaned orders."},

	// --- MBO snapshot & recovery ---
	"SNAP.BEGIN_ORDER_END_GROUPING":                   {"A snapshot is a well-formed Begin → exactly Total Orders × Order → End, with no inter-instrument interleave."},
	"SNAP.TOTAL_ORDERS_COUNT_MATCH":                   {"The number of SnapshotOrder messages equals the group's declared Total Orders."},
	"SNAP.END_FIELDS_MATCH_BEGIN":                     {"SnapshotEnd's Instrument ID, Anchor Seq, and Snapshot ID match SnapshotBegin."},
	"SNAP.ORDER_SNAPSHOT_ID_MATCH":                    {"Every SnapshotOrder's Snapshot ID matches its containing SnapshotBegin."},
	"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID":             {"No Order ID appears twice within one snapshot group."},
	"SNAP.EMPTY_BOOK_WELL_FORMED":                     {"An empty book is SnapshotBegin(total=0) → SnapshotEnd with no orders."},
	"SNAP.ORDER_PRICE_BOUND":                          {"SnapshotOrder.Price respects the instrument's Price Bound."},
	"SNAP.ANCHOR_IS_MKTDATA_SEQ":                      {"A snapshot's Anchor Seq is drawn from the mktdata sequence series, not snapshot/refdata."},
	"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT":            {"Successive snapshots for an instrument have non-decreasing Anchor Seq."},
	"SNAP.SNAPSHOT_ID_MONOTONIC":                      {"Snapshot ID is monotonic per (channel, instrument) within an era."},
	"SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS": {"Last Instrument Seq equals the last per-instrument seq applied at/below the anchor; the next delta is K+1."},
	"SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING":      {"Both anchor>last-applied (re-bootstrap) and anchor≤last-applied (consistency) are legitimate."},
	"SNAP.ROUND_ROBIN_COVERS_MANIFEST":                {"Every manifest instrument is snapshotted over a round-robin cycle."},
	"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":        {"Oracle: the delta-derived book equals the snapshot at the Anchor Seq."},
	"RESET.SNAPSHOT_FOLLOWS":                          {"An InstrumentReset is followed by a recovery snapshot for that instrument before deltas resume."},
	"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET":    {"The recovery snapshot's Anchor Seq equals the reset's new anchor; no premature delta resumption."},
	"RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR":     {"The first post-reset delta carries per-instrument seq = recovery anchor + 1."},

	// --- Reference-data supplement ---
	"REFDATA.MANIFEST_CADENCE":                {"ManifestSummary is sent at least once per manifest cadence (enabled by --expect-manifest-cadence)."},
	"REFDATA.DEFINITION_CYCLE_COVERAGE":       {"Every active definition is retransmitted at least once per cycle (enabled by --expect-definition-cycle)."},
	"REFDATA.COUNT_VS_DISTINCT_DEFS":          {"Under a stable seq, the distinct definitions retransmitted per cycle equals Instrument Count."},
	"REFDATA.SET_CHANGE_NO_SEQ_BUMP":          {"Any active-set membership change bumps the Manifest Seq."},
	"REFDATA.COUNT_CHANGE_NO_SEQ_BUMP":        {"Any Instrument Count change between summaries comes with a Manifest Seq change."},
	"REFDATA.STALE_SEQ_TAG_AFTER_BUMP":        {"After a seq bump, every new InstrumentDefinition is tagged with the new seq."},
	"REFDATA.VALID_FLAG_WHILE_SERVING":        {"With an established set, ManifestSummary.Valid is 1."},
	"REFDATA.NEVER_REACHES_READY":             {"A fresh both-port subscriber reaches ready() within manifest cadence + cycle."},
	"REFDATA.NO_BURST_DEFINITIONS":            {"Definitions are paced across the cycle, not sent in a single burst."},
	"REFDATA.SEQ_MONOTONIC_NO_REGRESS":        {"Manifest Seq is modular-monotonic non-decreasing within an era."},
	"REFDATA.SEQ_BUMP_NOT_BY_ONE":             {"On a set change, Manifest Seq increments by exactly 1 (modular)."},
	"REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID": {"A Valid=1 summary's seq is consistent with definition tags and a non-zero count."},
	"MANIFEST.STATE_MACHINE":                  {"ManifestSummary is internally coherent (Valid 0/1, seq +1 on change, count matches)."},

	// --- Top-of-Book ---
	"TOB.QUOTE.STRUCT_LEN_TYPE":        {"A Quote (0x03) is exactly 60 bytes, mktdata only, and parses within the frame."},
	"TOB.QUOTE.GONE_VS_ZERO_PRICE":     {"Bid-gone implies Bid Price 0; ask-gone implies Ask Price 0."},
	"TOB.QUOTE.CROSSED_LOCKED":         {"When both sides are present, Bid Price < Ask Price (not crossed or locked)."},
	"TOB.QUOTE.UPDATE_FLAGS_COHERENCE": {"Quote Update Flags are internally coherent (updated vs gone not contradictory)."},
	"TOB.QUOTE.SOURCE_ID_REGISTRY":     {"Quote Source ID is non-zero and within the registry's allowed range."},
	"TOB.QUOTE.SOURCE_COUNT":           {"Bid/Ask Source Count is advisory (0 = unavailable); observability only."},
	"TOB.QUOTE.REFDATA_KNOWN":          {"A Quote/Trade Instrument ID resolves to a current-manifest definition."},
	"TOB.TRADE.FIELDS":                 {"Trade (0x04) fields are valid: Aggressor Side 0/1/2, flag bits 0–2, registered source id."},

	// --- Midpoint ---
	"MID.STRUCT_LEN_TYPE":          {"A Midpoint (0x03) is exactly 40 bytes, mktdata only, magic 0x4D44."},
	"MID.METHOD_RANGE":             {"Method is a u8 (unknown non-zero = Custom); observability only."},
	"MID.QUALITY_FLAGS":            {"Quality Flags use bits 0–3; bits 4–7 are reserved and zero."},
	"MID.TIMESTAMP_ORDERING":       {"Book ≤ Compute ≤ frame Send timestamp."},
	"MID.METHOD0_REQUIRES_DEFAULT": {"Method=0 only when a definition with a non-zero Default Method was published."},
	"MID.PRICE_BOUND":              {"Mid Price respects the instrument's Price Bound (e.g. [0,1] for binary outcomes)."},
}
