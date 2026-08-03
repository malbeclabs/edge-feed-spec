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
