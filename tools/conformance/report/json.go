package report

import (
	"encoding/json"
	"os"
)

// ruleStatusCounts is the JSON-serialisable form of per-rule status counts.
// Status values are rendered as their string names for readability.
type ruleStatusCounts struct {
	RuleID string         `json:"rule_id"`
	Counts map[string]int `json:"counts"`
}

// JSONReport marshals the aggregator's per-rule status counts to the given file path.
// The file is created (or truncated) and written atomically enough for CI use.
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
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].RuleID < rows[j-1].RuleID; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
