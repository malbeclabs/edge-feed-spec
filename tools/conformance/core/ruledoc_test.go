package core

import (
	"strings"
	"testing"
)

// TestEveryRuleDocumented enforces that every rule in the registry has a
// non-empty Summary and a derivable spec URL, and that ruleDocs has no orphan
// entries. Mirrors the engine coverage guard so rule docs cannot rot.
func TestEveryRuleDocumented(t *testing.T) {
	for _, r := range Rules {
		d, ok := Doc(r.ID)
		if !ok {
			t.Errorf("%s: no ruleDocs entry", r.ID)
			continue
		}
		if strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%s: empty Summary", r.ID)
		}
		if SpecURL(r.ID) == specBaseURL {
			t.Errorf("%s: SpecURL fell through to the repo root (unmapped category)", r.ID)
		}
	}
	if len(ruleDocs) != len(Rules) {
		t.Errorf("ruleDocs has %d entries, registry has %d (orphan or missing doc)", len(ruleDocs), len(Rules))
	}
}

func TestSpecURLByCategory(t *testing.T) {
	cases := map[string]string{
		"FRAME.LENGTH_CONSISTENCY":  "top-of-book/spec.md",
		"BATCH.ID_MONOTONIC":        "market-by-order/spec.md",
		"REF.CANCEL_DANGLING_ORDER": "market-by-order/spec.md",
		"REFDATA.MANIFEST_CADENCE":  "reference-data/spec.md",
		"TOB.QUOTE.STRUCT_LEN_TYPE": "top-of-book/spec.md",
		"MID.PRICE_BOUND":           "midpoint/spec.md",
	}
	for id, want := range cases {
		if got := SpecURL(id); !strings.HasSuffix(got, want) {
			t.Errorf("SpecURL(%s) = %s, want suffix %s", id, got, want)
		}
	}
}
