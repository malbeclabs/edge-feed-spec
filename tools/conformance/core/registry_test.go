package core

import (
	"slices"
	"testing"
)

func TestRegistryComplete(t *testing.T) {
	if len(Rules) != 94 {
		t.Fatalf("registry has %d rules, want 94", len(Rules))
	}
	seen := map[string]bool{}
	for _, r := range Rules {
		if seen[r.ID] {
			t.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Tier == 1 && r.State != StateNone {
			t.Errorf("%s: T1 must have StateNone", r.ID)
		}
		if r.Tier == 2 && r.State == StateNone {
			t.Errorf("%s: T2 must not have StateNone", r.ID)
		}
		if len(r.Feeds) == 0 {
			t.Errorf("%s: no feeds", r.ID)
		}
	}
}

// conditionalExec is a second list beside Rules, so it can drift out of it. A
// rule renamed or dropped from the catalog would leave a dead entry here, and
// ConditionalExecRules would then quietly stop returning it — the denominator
// enforcement would pass by checking nothing.
func TestConditionalExecRulesExist(t *testing.T) {
	for id := range conditionalExec {
		if _, ok := Lookup(id); !ok {
			t.Errorf("conditionalExec lists %q, which is not in the rule catalog", id)
		}
	}
	// And the accessor must actually resolve them per feed: an entry no feed claims
	// is unreachable from the enforcement test.
	claimed := map[string]bool{}
	for _, feed := range allFeeds {
		for _, id := range ConditionalExecRules(feed) {
			claimed[id] = true
		}
	}
	for id := range conditionalExec {
		if !claimed[id] {
			t.Errorf("conditionalExec lists %q but no feed claims it", id)
		}
	}
}

func TestRuleLookup(t *testing.T) {
	if _, ok := Lookup("FRAME.MAGIC_MISMATCH"); !ok {
		t.Fatal("FRAME.MAGIC_MISMATCH not found")
	}
	if _, ok := Lookup("NOPE.NOPE"); ok {
		t.Fatal("unknown id resolved")
	}
}

// snapshotDriven is a third list beside Rules and can drift out of it the same way,
// with a worse consequence: a stale ID makes SnapshotDrivenRules silently drop it, so
// the CLI stops reporting a rule that an unbound snapshot port genuinely starves.
func TestSnapshotDrivenRulesExist(t *testing.T) {
	for id := range snapshotDriven {
		if _, ok := Lookup(id); !ok {
			t.Errorf("snapshotDriven lists %q, which is not in the rule catalog", id)
		}
	}
	claimed := map[string]bool{}
	for _, feed := range allFeeds {
		for _, id := range SnapshotDrivenRules(feed) {
			claimed[id] = true
		}
	}
	for id := range snapshotDriven {
		if !claimed[id] {
			t.Errorf("snapshotDriven lists %q but no feed claims it", id)
		}
	}
	// Only the two snapshot-bearing feeds have such rules; a TOB or Midpoint run has
	// no snapshot port and must not be warned about one.
	for _, feed := range []Feed{FeedTOB, FeedMidpoint} {
		if got := SnapshotDrivenRules(feed); len(got) != 0 {
			t.Errorf("feed %s has no snapshot port but claims snapshot-driven rules: %v", feed, got)
		}
	}
}

// TestFeedRuleCounts pins the per-feed split published in the README's rule
// catalog. TestRegistryComplete already guards the total, but the total is the
// number that does not matter to a user: a run reports one feed's subset, and a
// new feed-scoped rule moves that subset while leaving the total's guard happy.
//
// Documentation that no test reads goes stale silently, which is the same failure
// the rules themselves are written to avoid (engine/denominator.go). Update the
// README table in the same commit that changes a count here.
func TestFeedRuleCounts(t *testing.T) {
	want := map[Feed]int{
		FeedMBO:      69,
		FeedTOB:      39,
		FeedMBP:      38,
		FeedMidpoint: 32,
	}
	got := map[Feed]int{}
	for _, r := range Rules {
		for _, f := range r.Feeds {
			got[f]++
		}
	}
	for feed, n := range want {
		if got[feed] != n {
			t.Errorf("feed %s: registry has %d rules, README's rule catalog says %d — update tools/conformance/README.md", feed, got[feed], n)
		}
	}
	for feed := range got {
		if _, ok := want[feed]; !ok {
			t.Errorf("feed %s has rules but no expected count; add it here and to the README table", feed)
		}
	}
}

// TestSeedGatedRulesAreMarketByOrderRules: every entry of seedGated names a rule
// in the catalog that applies to the market-by-order feed. A misspelled or
// misplaced entry would drop out of the warning in reportStarvedRules without a
// sound.
func TestSeedGatedRulesAreMarketByOrderRules(t *testing.T) {
	for id := range seedGated {
		meta, ok := Lookup(id)
		if !ok {
			t.Errorf("%s is in seedGated but not in the rule catalog", id)
			continue
		}
		if !slices.Contains(meta.Feeds, FeedMBO) {
			t.Errorf("%s is in seedGated but does not apply to the mbo feed", id)
		}
	}
	if got := SeedGatedRules(FeedMBO); len(got) != len(seedGated) {
		t.Errorf("SeedGatedRules(mbo) returns %d rules, want %d", len(got), len(seedGated))
	}
}

// TestMBOInstanceRulesAreExactlyThese pins the state kind of each rule that reads
// the market-by-order state of one publisher (StateMBOInstance), and that no other
// rule has the kind. A rule that moves back to a channel kind stops naming its
// publisher, and no other test would see it.
func TestMBOInstanceRulesAreExactlyThese(t *testing.T) {
	want := map[string]bool{
		"BATCH.ATOMICITY_CONSISTENCY":                     true,
		"BATCH.ID_MONOTONIC":                              true,
		"DELTA.PERINSTR_DENSITY":                          true,
		"DELTA.PERINSTR_DUP_DIVERGENT":                    true,
		"DELTA.PERINSTR_FIRST_VALUE":                      true,
		"DELTA.PERINSTR_NO_SNAPSHOT_RESET":                true,
		"DELTA.PERINSTR_WRAP_BEFORE_RESET":                true,
		"FIELD.SOURCE_ID_CONSISTENCY":                     true,
		"REF.CANCEL_DANGLING_ORDER":                       true,
		"REF.DUPLICATE_LIVE_ORDERADD":                     true,
		"REF.EXEC_DANGLING_ORDER":                         true,
		"REF.EXEC_OVERFILL":                               true,
		"REF.FULLFILL_FLAG_DISAGREEMENT":                  true,
		"REF.OPERATION_AFTER_REMOVAL":                     true,
		"REF.SIDE_PRICE_CONSISTENCY":                      true,
		"RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR":     true,
		"RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET":    true,
		"RESET.SNAPSHOT_FOLLOWS":                          true,
		"SNAP.ANCHOR_IS_MKTDATA_SEQ":                      true,
		"SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING":      true,
		"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT":            true,
		"SNAP.BEGIN_ORDER_END_GROUPING":                   true,
		"SNAP.EMPTY_BOOK_WELL_FORMED":                     true,
		"SNAP.END_FIELDS_MATCH_BEGIN":                     true,
		"SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS": true,
		"SNAP.ORDER_SNAPSHOT_ID_MATCH":                    true,
		"SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT":        true,
		"SNAP.ROUND_ROBIN_COVERS_MANIFEST":                true,
		"SNAP.SNAPSHOT_ID_MONOTONIC":                      true,
		"SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID":             true,
		"SNAP.TOTAL_ORDERS_COUNT_MATCH":                   true,
		"TRADE.EXEC_GROUPING":                             true,
	}
	for _, r := range Rules {
		if got := r.State == StateMBOInstance; got != want[r.ID] {
			t.Errorf("%s: StateMBOInstance = %v, want %v", r.ID, got, want[r.ID])
		}
	}
	for id := range want {
		if _, ok := Lookup(id); !ok {
			t.Errorf("%s is listed but is not in the catalog", id)
		}
	}
}
