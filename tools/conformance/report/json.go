package report

import (
	"cmp"
	"encoding/json"
	"os"
	"slices"
)

// ruleStatusCounts is the JSON-serialisable form of per-rule status counts.
// Status values are rendered as their string names for readability.
type ruleStatusCounts struct {
	RuleID string         `json:"rule_id"`
	Counts map[string]int `json:"counts"`
}

// JSONReport marshals the aggregator's per-rule status counts to the given file path.
// The file is created or truncated and written in a single os.WriteFile call; a crash
// mid-write can leave a partial file (acceptable for CI report output, not durable state).
func JSONReport(agg *Aggregator, path string) error {
	counts := agg.Counts()
	rows := make([]ruleStatusCounts, 0, len(counts))
	for ruleID, statusMap := range counts {
		named := make(map[string]int, len(statusMap))
		for st, n := range statusMap {
			named[statusString(st)] = n
		}
		rows = append(rows, ruleStatusCounts{RuleID: ruleID, Counts: named})
	}

	// stable output: sort by rule_id so the file is deterministic
	sortRuleRows(rows)

	report := struct {
		Rules []ruleStatusCounts `json:"rules"`
	}{Rules: rows}

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
