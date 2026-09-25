package report

import (
	"net/netip"
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

	// violations_total{feed=mbo, rule_id=FRAME.MAGIC_MISMATCH, severity=must,
	// source_addr=""} == 1. The finding carries no address, so the label is empty.
	if got := testutil.ToFloat64(p.violations.WithLabelValues("mbo", "FRAME.MAGIC_MISMATCH", "must", "")); got != 1 {
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
		"BATCH.ID_MONOTONIC", "should", doc.Summary, core.SpecURL("BATCH.ID_MONOTONIC", core.FeedMBO))); got != 1 {
		t.Fatalf("rule_info{BATCH.ID_MONOTONIC}: want 1, got %v", got)
	}

	// The spec link is the active feed's own document: a rule that fired against
	// market-by-price must not link an operator to a sibling's spec.
	pm := NewProm(prometheus.NewRegistry(), "v", "c", core.FeedMBP)
	fdoc, _ := core.Doc("MSG.SNAPSHOT_FLAG_MATCHES_PORT")
	if got := testutil.ToFloat64(pm.ruleInfo.WithLabelValues(
		"MSG.SNAPSHOT_FLAG_MATCHES_PORT", "must", fdoc.Summary,
		core.SpecURL("MSG.SNAPSHOT_FLAG_MATCHES_PORT", core.FeedMBP))); got != 1 {
		t.Fatalf("rule_info{MSG.SNAPSHOT_FLAG_MATCHES_PORT} for mbp: want 1, got %v", got)
	}
}

// TestPromSourceAddrLabel pins what the source_addr label carries: the publisher
// for a finding that names one, and the empty string for one that does not.
//
// The empty case is the load-bearing half. A channel-scoped verdict shares its
// series with every other channel-scoped verdict of that rule, which is what
// lets a consumer separate "this path did it" from "this channel did it" by
// reading the label alone.
func TestPromSourceAddrLabel(t *testing.T) {
	p := NewProm(prometheus.NewRegistry(), "", "", core.FeedMBP)

	src := netip.MustParseAddr("148.51.120.6")
	p.Record(core.Finding{
		Feed:       core.FeedMBP,
		RuleID:     "FRAME.MAGIC_MISMATCH",
		Severity:   core.Must,
		Status:     core.Violation,
		SourceAddr: src,
	})
	// Channel-scoped: emit left the address unset.
	p.Record(core.Finding{
		Feed:     core.FeedMBP,
		RuleID:   "MBP.SNAP.GROUP_STRUCTURE",
		Severity: core.Must,
		Status:   core.Violation,
	})

	if got := testutil.ToFloat64(p.violations.WithLabelValues(
		"mbp", "FRAME.MAGIC_MISMATCH", "must", src.String())); got != 1 {
		t.Fatalf("violations_total{source_addr=%s}: want 1, got %v", src, got)
	}
	if got := testutil.ToFloat64(p.violations.WithLabelValues(
		"mbp", "MBP.SNAP.GROUP_STRUCTURE", "must", "")); got != 1 {
		t.Fatalf("violations_total{source_addr=\"\"}: want 1, got %v", got)
	}

	// checks_total carries it too, so a rule's denominator can be read per path.
	if got := testutil.ToFloat64(p.checks.WithLabelValues(
		"mbp", "FRAME.MAGIC_MISMATCH", "violation", src.String())); got != 1 {
		t.Fatalf("checks_total{source_addr=%s}: want 1, got %v", src, got)
	}

	// An unverifiable finding carries the address on its own metric as well.
	p.Record(core.Finding{
		Feed:       core.FeedMBP,
		RuleID:     "FRAME.MAGIC_MISMATCH",
		Severity:   core.Must,
		Status:     core.Unverifiable,
		Reason:     core.ReasonColdStart,
		SourceAddr: src,
	})
	if got := testutil.ToFloat64(p.unverifiable.WithLabelValues(
		"mbp", "FRAME.MAGIC_MISMATCH", core.ReasonColdStart, src.String())); got != 1 {
		t.Fatalf("unverifiable_total{source_addr=%s}: want 1, got %v", src, got)
	}
}
