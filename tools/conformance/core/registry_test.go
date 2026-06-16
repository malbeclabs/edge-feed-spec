package core

import "testing"

func TestRegistryComplete(t *testing.T) {
	if len(Rules) != 82 {
		t.Fatalf("registry has %d rules, want 82", len(Rules))
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

func TestRuleLookup(t *testing.T) {
	if _, ok := Lookup("FRAME.MAGIC_MISMATCH"); !ok {
		t.Fatal("FRAME.MAGIC_MISMATCH not found")
	}
	if _, ok := Lookup("NOPE.NOPE"); ok {
		t.Fatal("unknown id resolved")
	}
}
