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
		// Every feed the rule applies to must resolve to a document, not the repo root.
		for _, f := range r.Feeds {
			if SpecURL(r.ID, f) == specBaseURL {
				t.Errorf("%s (feed %s): SpecURL fell through to the repo root", r.ID, f)
			}
		}
	}
	if len(ruleDocs) != len(Rules) {
		t.Errorf("ruleDocs has %d entries, registry has %d (orphan or missing doc)", len(ruleDocs), len(Rules))
	}
}

// A rule links to the spec of the feed it fired against, because that is the
// document stating the requirement for that feed. The prefix does not decide it:
// `MSG.SNAPSHOT_FLAG_MATCHES_PORT` is market-by-price's own MUST and the
// top-of-book spec only describes the field, while a shared rule like
// `FRAME.LENGTH_CONSISTENCY` has to resolve differently per feed.
func TestSpecURLByFeed(t *testing.T) {
	cases := []struct {
		id   string
		feed Feed
		want string
	}{
		{"FRAME.LENGTH_CONSISTENCY", FeedTOB, "top-of-book/spec.md"},
		{"FRAME.LENGTH_CONSISTENCY", FeedMBP, "market-by-price/spec.md"},
		{"BATCH.ID_MONOTONIC", FeedMBO, "market-by-order/spec.md"},
		{"REF.CANCEL_DANGLING_ORDER", FeedMBO, "market-by-order/spec.md"},
		{"TOB.QUOTE.STRUCT_LEN_TYPE", FeedTOB, "top-of-book/spec.md"},
		{"MID.PRICE_BOUND", FeedMidpoint, "midpoint/spec.md"},
		{"MBP.SNAP.GROUP_STRUCTURE", FeedMBP, "market-by-price/spec.md"},
		// The rule the prefix map got wrong: market-by-price is the only spec in the
		// family that gives Flags bit 0 a normative setting.
		{"MSG.SNAPSHOT_FLAG_MATCHES_PORT", FeedMBP, "market-by-price/spec.md"},
		// Shared across both snapshot/delta feeds; each links to its own spec.
		{"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME", FeedMBO, "market-by-order/spec.md"},
		{"RESET.ANCHOR_SEQ_IS_CURRENT_FRAME", FeedMBP, "market-by-price/spec.md"},
		// The reference-data supplement is one document for every feed.
		{"REFDATA.MANIFEST_CADENCE", FeedMBP, "reference-data/spec.md"},
		{"REFDATA.MANIFEST_CADENCE", FeedTOB, "reference-data/spec.md"},
	}
	for _, c := range cases {
		if got := SpecURL(c.id, c.feed); !strings.HasSuffix(got, c.want) {
			t.Errorf("SpecURL(%s, %s) = %s, want suffix %s", c.id, c.feed, got, c.want)
		}
	}
}
