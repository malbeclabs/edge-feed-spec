package report

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

func TestExitAggregator(t *testing.T) {
	agg := &Aggregator{}
	agg.Record(core.Finding{RuleID: "X", Severity: core.Should, Status: core.Violation})
	if agg.ExitCode(false) != 0 {
		t.Fatal("should-violation must not fail CI by default")
	}
	if agg.ExitCode(true) != 1 {
		t.Fatal("should-violation must fail under --strict")
	}
	agg.Record(core.Finding{RuleID: "Y", Severity: core.Must, Status: core.Violation})
	if agg.ExitCode(false) != 1 {
		t.Fatal("must-violation must fail CI")
	}
}

func TestAggregatorIgnoresNonViolations(t *testing.T) {
	agg := &Aggregator{}
	agg.Record(core.Finding{RuleID: "Z", Severity: core.Must, Status: core.Suspected})
	agg.Record(core.Finding{RuleID: "Z", Severity: core.Must, Status: core.Unverifiable})
	agg.Record(core.Finding{RuleID: "Z", Severity: core.Must, Status: core.Pass})
	if agg.ExitCode(false) != 0 {
		t.Fatal("suspected/unverifiable/pass must not fail CI")
	}
}

// A conditional rule's denominator is only useful if the shortfall says why, and in
// --pcap mode the JSON report is the only place it can say so.
func TestAggregatorBreaksDownUnverifiableByReason(t *testing.T) {
	agg := &Aggregator{}
	agg.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Pass})
	agg.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Unverifiable, Reason: core.ReasonColdStart})
	agg.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Unverifiable, Reason: core.ReasonColdStart})
	agg.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Unverifiable, Reason: core.ReasonPending})
	// A reasonless Unverifiable still has to land somewhere, or the breakdown would
	// not sum to the status count and could not be read as a denominator.
	agg.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Unverifiable})

	got := agg.UnverifiableReasons()["R"]
	for reason, want := range map[string]int{
		core.ReasonColdStart:   2,
		core.ReasonPending:     1,
		core.ReasonUnspecified: 1,
	} {
		if got[reason] != want {
			t.Errorf("reason %q: %d, want %d", reason, got[reason], want)
		}
	}

	// The breakdown must account for every Unverifiable and nothing else.
	sum := 0
	for _, n := range got {
		sum += n
	}
	if want := agg.Counts()["R"][core.Unverifiable]; sum != want {
		t.Errorf("reasons sum to %d, but %d unverifiable findings were recorded", sum, want)
	}
}
