package report

import (
	"cmp"
	"encoding/json"
	"os"
	"slices"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// ruleStatusCounts is the JSON-serialisable form of per-rule status counts.
// Status values are rendered as their string names for readability.
type ruleStatusCounts struct {
	RuleID string `json:"rule_id"`
	// Severity is the rule's registry severity. Only must and should move the exit
	// code, so a reader needs it (with the report's `strict`) to reconstruct that code
	// from the counts.
	Severity string         `json:"severity"`
	Counts   map[string]int `json:"counts"`
	// Unverifiable is the `unverifiable` count broken down by cause, omitted when
	// the rule had none. For a rule whose execution is conditional this is what makes
	// its denominator readable: `{"pass": 5, "unverifiable": 33}` says the check ran 5
	// times out of 38, and this says whether the other 33 were healthy cold starts or
	// a feed dropping frames.
	Unverifiable map[string]int `json:"unverifiable_by_reason,omitempty"`
}

// Meta labels a report with the build that produced it and the exit-code policy it ran
// under. Both come from the caller: version and commit are package-main ldflags vars,
// and strict lives in the engine config, so nothing here can derive them.
type Meta struct {
	Version string
	Commit  string
	Strict  bool
}

// JSONReport marshals the aggregator's per-rule status counts to the given file path.
// The file is created or truncated and written in a single os.WriteFile call; a crash
// mid-write can leave a partial file (acceptable for CI report output, not durable state).
func JSONReport(agg *Aggregator, path string, meta Meta) error {
	counts := agg.Counts()
	reasons := agg.UnverifiableReasons()
	rows := make([]ruleStatusCounts, 0, len(counts))
	for ruleID, statusMap := range counts {
		named := make(map[string]int, len(statusMap))
		for st, n := range statusMap {
			named[statusString(st)] = n
		}
		var sev string
		if rule, ok := core.Lookup(ruleID); ok {
			sev = rule.Severity.String()
		}
		rows = append(rows, ruleStatusCounts{RuleID: ruleID, Severity: sev, Counts: named, Unverifiable: reasons[ruleID]})
	}

	// stable output: sort by rule_id so the file is deterministic
	sortRuleRows(rows)

	report := struct {
		Version string             `json:"version"`
		Commit  string             `json:"commit"`
		Strict  bool               `json:"strict"`
		Rules   []ruleStatusCounts `json:"rules"`
	}{Version: meta.Version, Commit: meta.Commit, Strict: meta.Strict, Rules: rows}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// sortRuleRows sorts rows by RuleID for deterministic output.
func sortRuleRows(rows []ruleStatusCounts) {
	slices.SortFunc(rows, func(a, b ruleStatusCounts) int {
		return cmp.Compare(a.RuleID, b.RuleID)
	})
}
