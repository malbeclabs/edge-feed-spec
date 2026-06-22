package report

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPromCounts(t *testing.T) {
	p := NewProm(prometheus.NewRegistry(), "", "", core.FeedMBO)

	// Record a must-violation for the mbo feed with rule FRAME.MAGIC_MISMATCH
	p.Record(core.Finding{
		Feed:     core.FeedMBO,
		RuleID:   "FRAME.MAGIC_MISMATCH",
		Severity: core.Must,
		Status:   core.Violation,
	})

	// violations_total{feed=mbo, rule_id=FRAME.MAGIC_MISMATCH, severity=must} == 1
	if got := testutil.ToFloat64(p.violations.WithLabelValues("mbo", "FRAME.MAGIC_MISMATCH", "must")); got != 1 {
		t.Fatalf("violations_total: want 1, got %v", got)
	}

	// TransportLoss bumps transport_loss_total{port=mktdata}
	p.TransportLoss(core.PortMktData)
	if got := testutil.ToFloat64(p.transportLoss.WithLabelValues("mktdata")); got != 1 {
		t.Fatalf("transport_loss_total: want 1, got %v", got)
	}
}

// TestRuleInfo verifies rule_info is published for exactly the rules applicable
// to the active feed, each carrying its summary and spec link.
func TestRuleInfo(t *testing.T) {
	p := NewProm(prometheus.NewRegistry(), "v", "c", core.FeedMBO)

	// One series per MBO-applicable rule — TOB/Midpoint-only rules are excluded.
	want := 0
	for _, r := range core.Rules {
		if feedApplies(core.FeedMBO, r.Feeds) {
			want++
		}
	}
	if got := testutil.CollectAndCount(p.ruleInfo); got != want {
		t.Fatalf("rule_info series: want %d (mbo-applicable rules), got %d", want, got)
	}

	// A known MBO rule is published with its summary + spec link.
	doc, _ := core.Doc("BATCH.ID_MONOTONIC")
	if got := testutil.ToFloat64(p.ruleInfo.WithLabelValues(
		"BATCH.ID_MONOTONIC", "should", doc.Summary, core.SpecURL("BATCH.ID_MONOTONIC"))); got != 1 {
		t.Fatalf("rule_info{BATCH.ID_MONOTONIC}: want 1, got %v", got)
	}
}
