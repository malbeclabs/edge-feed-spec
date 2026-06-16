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
	if agg.ExitCode(false) != 0 {
		t.Fatal("suspected/unverifiable must not fail CI")
	}
}
