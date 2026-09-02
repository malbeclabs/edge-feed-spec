package core

import "testing"

func TestRegistryComplete(t *testing.T) {
	if len(Rules) != 88 {
		t.Fatalf("registry has %d rules, want 88", len(Rules))
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
		FeedMBO:      68,
		FeedTOB:      33,
		FeedMBP:      32,
		FeedMidpoint: 31,
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
