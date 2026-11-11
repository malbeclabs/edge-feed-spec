package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
)

var updateRuleReport = flag.Bool("update-rule-report", false, "rewrite testdata/rule_report_mbp.golden.json")

// ruleReportGolden is the rule report of the committed market-by-price capture.
// Another repository reads this format, and this file is the example it can test
// against.
const ruleReportGolden = "testdata/rule_report_mbp.golden.json"

// TestRuleReportGolden replays the committed market-by-price capture with
// -rule-report and compares the file with the golden copy. The capture breaks
// rules, so the report must place violations on channel instances with evidence
// ranges.
func TestRuleReportGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rule-report.json")
	code := Run(RunOpts{
		Cfg: engine.Config{Feed: core.FeedMBP, OracleConfirmCycles: 2, ReorderWindow: 1},
		// Ports of the committed capture (see engine/mbp_fixture_test.go).
		PcapPath:     filepath.Join("testdata", "nonconformant_mbp.pcap"),
		MktDataPort:  31000,
		RefDataPort:  41000,
		SnapshotPort: 51000,
		RuleReport:   path,
		Version:      "v0.0.0-test",
		Commit:       "abc123def456",
	})
	if code != 1 {
		t.Fatalf("Run() = %d, want 1: the capture breaks must rules", code)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no rule report written: %v", err)
	}
	if *updateRuleReport {
		if err := os.WriteFile(ruleReportGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(ruleReportGolden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("rule report differs from %s; rerun with -update-rule-report and review the diff", ruleReportGolden)
	}

	var doc struct {
		Rules []struct {
			Outcome  string           `json:"outcome"`
			Instance *json.RawMessage `json:"instance"`
			Evidence *json.RawMessage `json:"evidence"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	placed := 0
	for _, r := range doc.Rules {
		if r.Outcome == "violation" && r.Instance != nil && r.Evidence != nil {
			placed++
		}
	}
	if placed == 0 {
		t.Error("no violation is placed on an instance with evidence; a reader cannot test any of them against loss")
	}
}

// TestRunWritesNoRuleReportUnlessAsked checks that the rule report is off by
// default, and that the counts report keeps its own shape when both are asked.
func TestRunWritesNoRuleReportUnlessAsked(t *testing.T) {
	dir := t.TempDir()
	counts := filepath.Join(dir, "counts.json")
	Run(RunOpts{
		Cfg:          engine.Config{Feed: core.FeedMBP, OracleConfirmCycles: 2, ReorderWindow: 1},
		PcapPath:     filepath.Join("testdata", "nonconformant_mbp.pcap"),
		MktDataPort:  31000,
		RefDataPort:  41000,
		SnapshotPort: 51000,
		JSONReport:   counts,
	})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("run wrote %d files, want only the counts report", len(entries))
	}
	rep := readJSONReport(t, counts)
	if len(rep.Rules) == 0 {
		t.Error("counts report has no rules")
	}
}

// TestRuleReportCountsMatchTheJSONReport checks that the two reports count the
// same findings. Every finding goes into exactly one rule report entry, so for
// each rule and outcome the sum of the entry counts is the JSON report's count.
func TestRuleReportCountsMatchTheJSONReport(t *testing.T) {
	dir := t.TempDir()
	counts, placedPath := filepath.Join(dir, "counts.json"), filepath.Join(dir, "rules.json")
	Run(RunOpts{
		Cfg:          engine.Config{Feed: core.FeedMBP, OracleConfirmCycles: 2, ReorderWindow: 1},
		PcapPath:     filepath.Join("testdata", "nonconformant_mbp.pcap"),
		MktDataPort:  31000,
		RefDataPort:  41000,
		SnapshotPort: 51000,
		JSONReport:   counts,
		RuleReport:   placedPath,
	})
	want := map[[2]string]int{}
	for _, r := range readJSONReport(t, counts).Rules {
		for outcome, n := range r.Counts {
			want[[2]string{r.RuleID, outcome}] += n
		}
	}
	data, err := os.ReadFile(placedPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rules []struct {
			RuleID  string `json:"rule_id"`
			Outcome string `json:"outcome"`
			Count   int    `json:"count"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[[2]string]int{}
	for _, r := range doc.Rules {
		got[[2]string{r.RuleID, r.Outcome}] += r.Count
	}
	if len(want) == 0 {
		t.Fatal("the JSON report has no counts")
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s %s: rule report counts %d, JSON report counts %d", k[0], k[1], got[k], n)
		}
	}
	for k, n := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s %s: rule report counts %d, the JSON report has none", k[0], k[1], n)
		}
	}
}
