package report

import (
	"cmp"
	"encoding/json"
	"net/netip"
	"os"
	"slices"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// RuleReportFormat is the format number of the rule report. A reader refuses a
// number it does not know. Increment it when a field changes its meaning. Adding
// a field does not change the number: a reader ignores a field it does not know.
const RuleReportFormat = 1

// maxRanges is the maximum number of evidence ranges that one entry key keeps for
// a violation or a suspected finding. A publisher that breaks a rule on every
// datagram makes one range per datagram only when the datagrams are not
// contiguous. Past the bound, a new finding extends the last range. The report
// stays bounded, and the range still covers every finding.
const maxRanges = 64

// RuleReport is the placed report: one entry for each (rule, outcome, channel
// instance, era) that the run produced, with the sequence range of its evidence.
//
// The counts report (JSONReport) answers "how often did each rule hold". This
// report answers "where did each outcome occur". A consumer that keeps one row
// for each rule and channel instance needs the second answer. It must compare a
// violation against the loss on that instance, and it can do so only when it
// knows the instance and the sequence range.
//
// RuleReport is a Reporter. It records findings and ignores the other telemetry.
type RuleReport struct {
	noopMetrics
	feed    core.Feed
	ports   map[core.Port]uint16
	entries map[entryKey]*entry
}

// NewRuleReport returns a RuleReport for one feed. ports gives the destination
// port of each port role. A role with no port, or port 0, has no channel
// instance, so its findings carry no instance.
func NewRuleReport(feed core.Feed, ports map[core.Port]uint16) *RuleReport {
	return &RuleReport{feed: feed, ports: ports, entries: map[entryKey]*entry{}}
}

// entryKey groups findings into one entry. The instance fields are zero when the
// finding names no instance, and hasInstance is then false.
type entryKey struct {
	ruleID      string
	status      core.Status
	severity    core.Severity
	reason      string
	hasInstance bool
	source      netip.Addr
	channelID   uint8
	port        core.Port
	hasEra      bool
	resetCount  uint8
}

// seqRange is one evidence range and the number of findings in it.
type seqRange struct {
	first, last uint64
	count       int
}

type entry struct {
	count  int
	detail string
	// ranges holds the evidence. A violation or a suspected finding keeps up to
	// maxRanges contiguous ranges. Any other outcome keeps one range from the
	// lowest to the highest sequence number.
	ranges []seqRange
}

// Record adds one finding. Like the other reporters it is not safe for
// concurrent use; the engine calls it from one goroutine.
func (r *RuleReport) Record(f core.Finding) {
	k := entryKey{ruleID: f.RuleID, status: f.Status, severity: f.Severity,
		channelID: f.ChannelID, port: f.Port}
	if f.Status == core.Unverifiable {
		k.reason = f.Reason
		if k.reason == "" {
			k.reason = core.ReasonUnspecified
		}
	}
	src := f.SourceAddr.Unmap()
	if dst := r.ports[f.Port]; src.Is4() && dst != 0 {
		k.hasInstance = true
		k.source = src
		if f.ResetCountKnown {
			k.hasEra = true
			k.resetCount = f.ResetCount
		}
	}
	e := r.entries[k]
	if e == nil {
		e = &entry{detail: f.Detail}
		r.entries[k] = e
	}
	e.count++
	if !k.hasEra {
		return
	}
	if f.Status == core.Violation || f.Status == core.Suspected {
		e.addRange(f.Seq)
		return
	}
	if len(e.ranges) == 0 {
		e.ranges = append(e.ranges, seqRange{first: f.Seq, last: f.Seq})
	}
	e.ranges[0].extend(f.Seq)
}

// addRange puts seq into a range that contains it or touches it. If no range
// does, it starts a new range, up to maxRanges. Past the bound it extends the
// last range to cover seq.
func (e *entry) addRange(seq uint64) {
	for i := len(e.ranges) - 1; i >= 0; i-- {
		rg := &e.ranges[i]
		if seq+1 >= rg.first && seq <= rg.last+1 {
			rg.extend(seq)
			return
		}
	}
	if len(e.ranges) < maxRanges {
		e.ranges = append(e.ranges, seqRange{first: seq, last: seq, count: 1})
		return
	}
	e.ranges[len(e.ranges)-1].extend(seq)
}

// extend widens the range to cover seq and counts one more finding in it.
func (rg *seqRange) extend(seq uint64) {
	rg.first = min(rg.first, seq)
	rg.last = max(rg.last, seq)
	rg.count++
}

// ruleReportInstance is the channel instance: (source IP address, Channel ID,
// destination port).
type ruleReportInstance struct {
	Source    string `json:"source"`
	ChannelID uint8  `json:"channel_id"`
	DstPort   uint16 `json:"dst_port"`
}

// ruleReportEvidence is the sequence range of the evidence, in one era.
type ruleReportEvidence struct {
	ResetCount uint8  `json:"reset_count"`
	FirstSeq   uint64 `json:"first_seq"`
	LastSeq    uint64 `json:"last_seq"`
}

// ruleReportEntry is one entry. instance is absent when the finding judges the
// channel and not one path of it (see core.StateKind.InstanceScoped), or when the
// port role has no configured port. evidence is absent when instance is absent or
// the era is not known. channel_id and port_role are always present, so that a
// reader can place an entry with no instance on its channel.
type ruleReportEntry struct {
	RuleID    string              `json:"rule_id"`
	Outcome   string              `json:"outcome"`
	Severity  string              `json:"severity"`
	Reason    string              `json:"reason,omitempty"`
	Instance  *ruleReportInstance `json:"instance,omitempty"`
	Evidence  *ruleReportEvidence `json:"evidence,omitempty"`
	ChannelID uint8               `json:"channel_id"`
	PortRole  string              `json:"port_role"`
	Count     int                 `json:"count"`
	Detail    string              `json:"detail"`
}

// sortedEntries returns the report entries in a deterministic order.
func (r *RuleReport) sortedEntries() []ruleReportEntry {
	out := make([]ruleReportEntry, 0, len(r.entries))
	for k, e := range r.entries {
		base := ruleReportEntry{RuleID: k.ruleID, Outcome: outcomeString(k.status),
			Severity: k.severity.String(), Reason: k.reason, ChannelID: k.channelID,
			PortRole: k.port.String(), Count: e.count, Detail: e.detail}
		if k.hasInstance {
			base.Instance = &ruleReportInstance{Source: k.source.String(), ChannelID: k.channelID,
				DstPort: r.ports[k.port]}
		}
		if len(e.ranges) == 0 {
			out = append(out, base)
			continue
		}
		// One entry for each range, so that each range is tested against the loss
		// on its own. Each entry counts the findings in its own range, so the sum
		// of the counts is the number of findings.
		for _, rg := range e.ranges {
			x := base
			x.Count = rg.count
			x.Evidence = &ruleReportEvidence{ResetCount: k.resetCount, FirstSeq: rg.first, LastSeq: rg.last}
			out = append(out, x)
		}
	}
	slices.SortFunc(out, compareEntries)
	return out
}

func compareEntries(a, b ruleReportEntry) int {
	if c := cmp.Compare(a.RuleID, b.RuleID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.ChannelID, b.ChannelID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.PortRole, b.PortRole); c != 0 {
		return c
	}
	if c := compareInstance(a.Instance, b.Instance); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Outcome, b.Outcome); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Severity, b.Severity); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Reason, b.Reason); c != 0 {
		return c
	}
	return compareEvidence(a.Evidence, b.Evidence)
}

func compareInstance(a, b *ruleReportInstance) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	if c := netip.MustParseAddr(a.Source).Compare(netip.MustParseAddr(b.Source)); c != 0 {
		return c
	}
	return cmp.Compare(a.DstPort, b.DstPort)
}

func compareEvidence(a, b *ruleReportEvidence) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	if c := cmp.Compare(a.ResetCount, b.ResetCount); c != 0 {
		return c
	}
	return cmp.Compare(a.FirstSeq, b.FirstSeq)
}

// outcomeString is the outcome vocabulary of the rule report. It is a declared
// interface: a reader matches these strings exactly.
func outcomeString(s core.Status) string {
	switch s {
	case core.Pass:
		return "pass"
	case core.Violation:
		return "violation"
	case core.Suspected:
		return "suspected"
	case core.Unverifiable:
		return "unverifiable"
	default:
		return "na"
	}
}

// WriteRuleReport writes the rule report to path in one os.WriteFile call. meta
// gives the build, the read error and the capture drops, the same values the
// counts report carries.
func WriteRuleReport(r *RuleReport, path string, meta Meta) error {
	var readErr string
	if meta.ReadErr != nil {
		readErr = meta.ReadErr.Error()
	}
	doc := struct {
		ReportFormat int               `json:"report_format"`
		Feed         string            `json:"feed"`
		Version      string            `json:"version"`
		Commit       string            `json:"commit"`
		ReadError    string            `json:"read_error"`
		CaptureDrops uint64            `json:"capture_drops"`
		Rules        []ruleReportEntry `json:"rules"`
	}{ReportFormat: RuleReportFormat, Feed: string(r.feed), Version: meta.Version,
		Commit: meta.Commit, ReadError: readErr, CaptureDrops: meta.CaptureDrops,
		Rules: r.sortedEntries()}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
