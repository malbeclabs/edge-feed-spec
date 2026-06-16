package engine

// mbo.go — MBO feed validator rules inventory.
//
// The MBO classifier entry point is Engine.checkMBO, defined in gate.go along
// with the per-instrument seq tracker and verifiability gate.  It is wired into
// Engine.classify via the per-feed dispatch switch:
//
//	case core.FeedMBO:
//	    e.checkMBO(f, f.Header.ChannelID)
//
// Snapshot-port frames are routed via:
//
//	case port == core.PortSnapshot && cfg.Feed == FeedMBO:
//	    e.checkMBOSnapshot(f, ch, snapPortSeq)
//
// Rules implemented in gate.go (Task 18):
//   - DELTA.PERINSTR_DENSITY     (Must, counters)
//   - DELTA.PERINSTR_FIRST_VALUE (Should, counters)
//
// Rules implemented in gate.go (Task 19):
//   - DELTA.PERINSTR_NO_SNAPSHOT_RESET                   (Must, counters)
//   - DELTA.PERINSTR_DUP_DIVERGENT                       (Must, counters)
//   - DELTA.PERINSTR_WRAP_BEFORE_RESET                   (Info, counters)
//   - FRAME.MKTDATA_SEQ_START                            (Should, counters)
//   - BATCH.ID_MONOTONIC                                 (Should, counters)
//   - SNAP.ANCHOR_IS_MKTDATA_SEQ                         (Must, counters)
//   - SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT               (Should, counters)
//   - SNAP.SNAPSHOT_ID_MONOTONIC                         (Should, counters)
//   - SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS    (Must, counters)
//   - RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR        (Must, counters)
//
// Rules implemented in gate.go (Task 21 — snapshot-group accumulator):
//   - SNAP.BEGIN_ORDER_END_GROUPING          (Must, snapshot_group)
//   - SNAP.TOTAL_ORDERS_COUNT_MATCH          (Must, snapshot_group)
//   - SNAP.END_FIELDS_MATCH_BEGIN            (Must, snapshot_group)
//   - SNAP.ORDER_SNAPSHOT_ID_MATCH           (Must, snapshot_group)
//   - SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID   (Must, snapshot_group)
//   - SNAP.EMPTY_BOOK_WELL_FORMED            (Info, snapshot_group)
//   - SNAP.ORDER_PRICE_BOUND                 (Should, refdata)
//
// Rules implemented in gate.go (Task 22 — cross-port rules):
//   - RESET.SNAPSHOT_FOLLOWS                          (Must, snapshot_group)
//   - RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET    (Must, snapshot_group)
//   - SNAP.ROUND_ROBIN_COVERS_MANIFEST                (Should, refdata)
//
// Rules implemented in gate.go (Task 25 — quantity-conservation):
//   - REF.EXEC_OVERFILL                  (Must, full_book, MBO)
//   - REF.FULLFILL_FLAG_DISAGREEMENT     (Must, full_book, MBO)
//   - BATCH.ATOMICITY_CONSISTENCY        (Should, full_book, MBO)
//
// Rules implemented in oracle.go (Task 26 — reconstruction oracle):
//   - SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT  (Must, full_book, MBO)
//
// Rules implemented in gate.go (Task 27 — observability):
//   - SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING  (Info, counters)
