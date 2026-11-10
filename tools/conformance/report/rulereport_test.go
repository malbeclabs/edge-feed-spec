package report

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

var (
	rrSrc   = netip.MustParseAddr("192.0.2.1")
	rrPorts = map[core.Port]uint16{core.PortMktData: 40000, core.PortRefData: 40001, core.PortSnapshot: 40002}
)

func placed(rule string, st core.Status, port core.Port, era uint8, seq uint64) core.Finding {
	return core.Finding{RuleID: rule, Status: st, Severity: core.Must, Port: port, ChannelID: 3,
		SourceAddr: rrSrc, ResetCount: era, ResetCountKnown: true, Seq: seq}
}

// TestRuleReportPlacesAFindingOnItsInstance checks the three ways an entry is
// placed: on an instance with evidence, on an instance with no known era, and on
// no instance at all.
func TestRuleReportPlacesAFindingOnItsInstance(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	r.Record(placed("A.RULE", core.Violation, core.PortSnapshot, 2, 900))

	noEra := placed("B.RULE", core.Pass, core.PortMktData, 0, 7)
	noEra.ResetCountKnown = false
	r.Record(noEra)

	// A finding about the channel names no source address.
	channel := placed("C.RULE", core.Violation, core.PortMktData, 2, 7)
	channel.SourceAddr = netip.Addr{}
	r.Record(channel)

	// A port role with no configured port has no channel instance.
	unbound := NewRuleReport(core.FeedMBP, map[core.Port]uint16{core.PortMktData: 40000})
	unbound.Record(placed("D.RULE", core.Pass, core.PortRefData, 2, 7))

	got := map[string]ruleReportEntry{}
	for _, e := range append(r.sortedEntries(), unbound.sortedEntries()...) {
		got[e.RuleID] = e
	}

	a := got["A.RULE"]
	if a.Instance == nil || *a.Instance != (ruleReportInstance{Source: "192.0.2.1", ChannelID: 3, DstPort: 40002}) {
		t.Errorf("A.RULE instance = %+v; want 192.0.2.1 channel 3 port 40002", a.Instance)
	}
	if a.Evidence == nil || *a.Evidence != (ruleReportEvidence{ResetCount: 2, FirstSeq: 900, LastSeq: 900}) {
		t.Errorf("A.RULE evidence = %+v; want era 2, 900..900", a.Evidence)
	}
	if b := got["B.RULE"]; b.Instance == nil || b.Evidence != nil {
		t.Errorf("B.RULE: want an instance and no evidence when the era is unknown, got %+v / %+v",
			b.Instance, b.Evidence)
	}
	for _, id := range []string{"C.RULE", "D.RULE"} {
		e := got[id]
		if e.Instance != nil || e.Evidence != nil {
			t.Errorf("%s: want no instance and no evidence, got %+v / %+v", id, e.Instance, e.Evidence)
		}
		if e.Count != 1 || e.ChannelID != 3 || e.PortRole == "" {
			t.Errorf("%s: want count 1 on channel 3 with a port role, got %+v", id, e)
		}
	}
}

// TestRuleReportKeepsEachViolationRange checks that violations keep separate
// ranges. A consumer compares each range with the loss on the instance. If the
// report merged two distant violations into one range, a hole between them would
// excuse both.
func TestRuleReportKeepsEachViolationRange(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	for _, seq := range []uint64{10, 11, 12, 50, 13, 51} {
		r.Record(placed("A.RULE", core.Violation, core.PortMktData, 1, seq))
	}
	// Passes keep one range from the lowest to the highest sequence number.
	for _, seq := range []uint64{30, 5, 70} {
		r.Record(placed("A.RULE", core.Pass, core.PortMktData, 1, seq))
	}
	// Another era is another entry: the two sequence series do not compare.
	r.Record(placed("A.RULE", core.Violation, core.PortMktData, 2, 11))

	type rng struct {
		outcome     string
		era         uint8
		first, last uint64
		count       int
	}
	var got []rng
	for _, e := range r.sortedEntries() {
		got = append(got, rng{e.Outcome, e.Evidence.ResetCount, e.Evidence.FirstSeq, e.Evidence.LastSeq, e.Count})
	}
	want := []rng{
		{"pass", 1, 5, 70, 3},
		{"violation", 1, 10, 13, 4},
		{"violation", 1, 50, 51, 2},
		{"violation", 2, 11, 11, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %+v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestRuleReportBoundsTheRanges checks the bound on violation ranges. A rule that
// fails on every other datagram must not make the report grow with the capture.
// Past the bound the last range grows, so every finding stays inside a range and
// the counts still sum to the number of findings.
func TestRuleReportBoundsTheRanges(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	const n = 3 * maxRanges
	for i := range uint64(n) {
		r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, 2*i))
	}
	entries := r.sortedEntries()
	if len(entries) != maxRanges {
		t.Fatalf("got %d entries, want the bound %d", len(entries), maxRanges)
	}
	total := 0
	for _, e := range entries {
		total += e.Count
	}
	if total != n {
		t.Errorf("counts sum to %d, want %d", total, n)
	}
	if last := entries[len(entries)-1].Evidence; last.LastSeq != 2*(n-1) {
		t.Errorf("last range ends at %d, want %d: a finding past the bound is outside every range",
			last.LastSeq, 2*(n-1))
	}
}

// TestRuleReportSplitsUnverifiableByReason checks that an unverifiable entry
// keeps its cause. "loss" and "capture_loss" send an operator to different
// owners.
func TestRuleReportSplitsUnverifiableByReason(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	for _, rsn := range []string{core.ReasonLoss, core.ReasonCaptureLoss, ""} {
		f := placed("A.RULE", core.Unverifiable, core.PortMktData, 0, 1)
		f.Reason = rsn
		r.Record(f)
	}
	var reasons []string
	for _, e := range r.sortedEntries() {
		reasons = append(reasons, e.Reason)
	}
	want := []string{core.ReasonCaptureLoss, core.ReasonLoss, core.ReasonUnspecified}
	if len(reasons) != len(want) {
		t.Fatalf("reasons %v, want %v", reasons, want)
	}
	for i := range want {
		if reasons[i] != want[i] {
			t.Errorf("reasons %v, want %v", reasons, want)
		}
	}
}

// TestRuleReportOutcomeVocabulary pins the outcome strings. A reader in another
// repository matches them exactly, so each status must map to its own string.
func TestRuleReportOutcomeVocabulary(t *testing.T) {
	want := map[core.Status]string{
		core.Pass: "pass", core.Violation: "violation", core.Suspected: "suspected",
		core.Unverifiable: "unverifiable", core.NA: "na",
	}
	seen := map[string]bool{}
	for st := core.Pass; st <= core.NA; st++ {
		got := outcomeString(st)
		if got != want[st] {
			t.Errorf("outcomeString(%d) = %q, want %q", st, got, want[st])
		}
		if seen[got] {
			t.Errorf("two statuses map to %q", got)
		}
		seen[got] = true
	}
}

// TestWriteRuleReportShape checks the file against the declared format: the
// field names and types a reader parses. It decodes into a strict mirror of the
// reader's types, so a renamed or retyped field fails here.
func TestWriteRuleReportShape(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	r.Record(placed("A.RULE", core.Violation, core.PortMktData, 1, 10))
	channel := placed("C.RULE", core.NA, core.PortMktData, 0, 0)
	channel.SourceAddr = netip.Addr{}
	r.Record(channel)

	path := filepath.Join(t.TempDir(), "rule-report.json")
	if err := WriteRuleReport(r, path, Meta{Version: "v1.2.3", Commit: "abc"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkRuleReportShape(t, data, "mbp")
}

// checkRuleReportShape decodes data into the format-1 types that a reader keeps:
// report_format and feed at the top, and rule_id, outcome, instance, evidence and
// detail in each entry. Other fields are allowed, because a reader ignores a field
// it does not know.
func checkRuleReportShape(t *testing.T, data []byte, feed string) {
	t.Helper()
	var doc struct {
		ReportFormat *int    `json:"report_format"`
		Feed         *string `json:"feed"`
		Rules        []struct {
			RuleID   *string `json:"rule_id"`
			Outcome  *string `json:"outcome"`
			Instance *struct {
				Source    *string `json:"source"`
				ChannelID *uint8  `json:"channel_id"`
				DstPort   *uint16 `json:"dst_port"`
			} `json:"instance"`
			Evidence *struct {
				ResetCount *uint8  `json:"reset_count"`
				FirstSeq   *uint64 `json:"first_seq"`
				LastSeq    *uint64 `json:"last_seq"`
			} `json:"evidence"`
			Detail *string `json:"detail"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("report does not decode: %v", err)
	}
	if doc.ReportFormat == nil || *doc.ReportFormat != 1 {
		t.Fatalf("report_format = %v, want 1", doc.ReportFormat)
	}
	if doc.Feed == nil || *doc.Feed != feed {
		t.Fatalf("feed = %v, want %q", doc.Feed, feed)
	}
	if len(doc.Rules) == 0 {
		t.Fatal("report has no rules")
	}
	outcomes := map[string]bool{"pass": true, "violation": true, "suspected": true, "unverifiable": true, "na": true}
	for i, e := range doc.Rules {
		if e.RuleID == nil || *e.RuleID == "" {
			t.Errorf("rule %d has no rule_id", i)
		}
		if e.Outcome == nil || !outcomes[*e.Outcome] {
			t.Errorf("rule %d outcome %v is not in the declared vocabulary", i, e.Outcome)
		}
		if e.Detail == nil {
			t.Errorf("rule %d has no detail", i)
		}
		if in := e.Instance; in != nil {
			if in.Source == nil || in.ChannelID == nil || in.DstPort == nil {
				t.Errorf("rule %d instance is missing a field", i)
			} else if a, err := netip.ParseAddr(*in.Source); err != nil || !a.Is4() {
				t.Errorf("rule %d instance source %q is not an IPv4 address", i, *in.Source)
			}
		}
		if ev := e.Evidence; ev != nil {
			if e.Instance == nil {
				t.Errorf("rule %d has evidence and no instance", i)
			}
			if ev.ResetCount == nil || ev.FirstSeq == nil || ev.LastSeq == nil {
				t.Errorf("rule %d evidence is missing a field", i)
			} else if *ev.FirstSeq > *ev.LastSeq {
				t.Errorf("rule %d evidence range %d..%d is inverted", i, *ev.FirstSeq, *ev.LastSeq)
			}
		}
	}
}
