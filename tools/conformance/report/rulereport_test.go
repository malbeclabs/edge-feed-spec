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
		SourceAddr: rrSrc, ResetCount: era, ResetCountKnown: true, EraIndex: uint64(era) + 1, Seq: seq}
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

	// A finding about the channel names no source IP address.
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
	if a.Instance == nil || *a.Instance != (ruleReportInstance{SourceAddr: "192.0.2.1", ChannelID: 3, DstPort: 40002}) {
		t.Errorf("A.RULE instance = %+v; want 192.0.2.1 channel 3 port 40002", a.Instance)
	}
	if ev := a.Evidence; ev == nil || ev.ResetCount != 2 || ev.EraIndex == nil || *ev.EraIndex != 3 ||
		ev.FirstSeq != 900 || ev.LastSeq != 900 {
		t.Errorf("A.RULE evidence = %+v; want Reset Count 2, era index 3, 900..900", a.Evidence)
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
// Past the bound two neighbouring ranges merge, so every finding stays inside a
// range and the counts still sum to the number of findings. The gaps here are all
// equal; TestRuleReportMergesTheSmallestGap pins which pair merges.
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
				SourceAddr *string `json:"source_addr"`
				ChannelID  *uint8  `json:"channel_id"`
				DstPort    *uint16 `json:"dst_port"`
			} `json:"instance"`
			Evidence *struct {
				ResetCount *uint8  `json:"reset_count"`
				EraIndex   *uint64 `json:"era_index"`
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
			if in.SourceAddr == nil || in.ChannelID == nil || in.DstPort == nil {
				t.Errorf("rule %d instance is missing a field", i)
			} else if a, err := netip.ParseAddr(*in.SourceAddr); err != nil || !a.Is4() {
				t.Errorf("rule %d instance source IP address %q is not an IPv4 address", i, *in.SourceAddr)
			}
		}
		if ev := e.Evidence; ev != nil {
			if e.Instance == nil {
				t.Errorf("rule %d has evidence and no instance", i)
			}
			if ev.EraIndex != nil && *ev.EraIndex == 0 {
				t.Errorf("rule %d era_index is 0; an index starts at 1 and an unknown one is absent", i)
			}
			if ev.ResetCount == nil || ev.FirstSeq == nil || ev.LastSeq == nil {
				t.Errorf("rule %d evidence is missing a field", i)
			} else if *ev.FirstSeq > *ev.LastSeq {
				t.Errorf("rule %d evidence range %d..%d is inverted", i, *ev.FirstSeq, *ev.LastSeq)
			}
		}
	}
}

// TestRuleReportKeepsWrappedErasApart checks that two eras with the same Reset
// Count stay apart. The Reset Count is a u8 and wraps after 256 resets. If the
// report keyed on it alone, a violation in one era would be compared with the
// loss of another era that has the same value.
func TestRuleReportKeepsWrappedErasApart(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	first := placed("A.RULE", core.Violation, core.PortMktData, 7, 10)
	first.EraIndex = 8
	wrapped := placed("A.RULE", core.Violation, core.PortMktData, 7, 11)
	wrapped.EraIndex = 8 + 256
	r.Record(first)
	r.Record(wrapped)
	entries := r.sortedEntries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: the two eras share Reset Count 7 and are not one era", len(entries))
	}
	if *entries[0].Evidence.EraIndex != 8 || *entries[1].Evidence.EraIndex != 264 {
		t.Errorf("era indexes %d and %d, want 8 and 264",
			*entries[0].Evidence.EraIndex, *entries[1].Evidence.EraIndex)
	}
}

// TestRuleReportMergesRangesThatTouch checks that ranges merge when a finding
// joins them. Odd sequence numbers 1 to 127 make 64 ranges. Even numbers 2 to 126
// fill every gap, so the 64 ranges become one. A finding at 1000 then starts a
// second range. If the report did not merge, it would reach the bound and stretch
// a range across 126 to 1000, and a hole at 500 would excuse a violation it has
// nothing to do with.
func TestRuleReportMergesRangesThatTouch(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	for seq := uint64(1); seq <= 127; seq += 2 {
		r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, seq))
	}
	for seq := uint64(2); seq <= 126; seq += 2 {
		r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, seq))
	}
	r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, 1000))

	type rng struct {
		first, last uint64
		count       int
	}
	var got []rng
	for _, e := range r.sortedEntries() {
		got = append(got, rng{e.Evidence.FirstSeq, e.Evidence.LastSeq, e.Count})
	}
	want := []rng{{1, 127, 127}, {1000, 1000, 1}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ranges %+v, want %+v", got, want)
	}
}

// TestRuleReportKeepsTheDetailOfEachRange checks that each range carries the
// detail of the earliest finding in that range. Two violations of one rule can
// concern different messages, and a range must not describe a finding outside it.
func TestRuleReportKeepsTheDetailOfEachRange(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	for _, f := range []struct {
		seq    uint64
		detail string
	}{{10, "type 1 length 9"}, {50, "type 2 length 3"}, {11, "type 1 length 8"}} {
		x := placed("A.RULE", core.Violation, core.PortMktData, 0, f.seq)
		x.Detail = f.detail
		r.Record(x)
	}
	entries := r.sortedEntries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Detail != "type 1 length 9" || entries[1].Detail != "type 2 length 3" {
		t.Errorf("details %q and %q, want the earliest finding of each range",
			entries[0].Detail, entries[1].Detail)
	}
}

// TestRuleReportBoundsPlacedKeysUnderAddressChurn checks the bound on placed
// keys. On a live subscription each new source IP address makes new keys. Past
// the bound the report keeps every count in an overflow entry with no instance,
// and it says how many findings it could not place.
func TestRuleReportBoundsPlacedKeysUnderAddressChurn(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	const extra = 100
	n := maxPlacedKeys + extra
	for i := range n {
		f := placed("A.RULE", core.Violation, core.PortMktData, 0, 1)
		f.SourceAddr = netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		r.Record(f)
	}
	if len(r.entries) != maxPlacedKeys+1 {
		t.Errorf("report holds %d keys, want the bound %d plus one overflow entry", len(r.entries), maxPlacedKeys)
	}
	if r.unplaced != extra {
		t.Errorf("unplaced_findings = %d, want %d", r.unplaced, extra)
	}
	total, overflow := 0, 0
	for _, e := range r.sortedEntries() {
		total += e.Count
		if e.Overflow {
			overflow += e.Count
			if e.Instance != nil || e.Evidence != nil {
				t.Errorf("overflow entry has an instance or evidence: %+v", e)
			}
		}
	}
	if total != n || overflow != extra {
		t.Errorf("counts sum to %d with %d in overflow, want %d with %d", total, overflow, n, extra)
	}
}

// TestRuleReportMergesTheSmallestGap pins which ranges merge past the bound. There
// are 64 single-finding ranges at 0, 10, ..., 630, except that the sixth is at 42,
// 2 after the fifth. A 65th finding at 1000 is over the bound. The smallest gap is
// 40 to 42, so those two merge and 1000 stays a range of its own. If the last
// range grew instead, it would span 630 to 1000, and loss anywhere in it would
// excuse both violations.
func TestRuleReportMergesTheSmallestGap(t *testing.T) {
	r := NewRuleReport(core.FeedMBP, rrPorts)
	for i := range uint64(maxRanges) {
		seq := 10 * i
		if i == 5 {
			seq = 42
		}
		r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, seq))
	}
	r.Record(placed("A.RULE", core.Violation, core.PortMktData, 0, 1000))

	type rng struct {
		first, last uint64
		count       int
	}
	got := map[rng]bool{}
	for _, e := range r.sortedEntries() {
		got[rng{e.Evidence.FirstSeq, e.Evidence.LastSeq, e.Count}] = true
	}
	if len(got) != maxRanges {
		t.Fatalf("got %d ranges, want the bound %d", len(got), maxRanges)
	}
	for _, want := range []rng{{40, 42, 2}, {1000, 1000, 1}, {630, 630, 1}} {
		if !got[want] {
			t.Errorf("no range %+v; the pair with the smallest gap must merge, and no other", want)
		}
	}
}
