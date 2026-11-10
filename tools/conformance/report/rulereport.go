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
// a violation or a suspected finding. Ranges that touch or overlap always merge
// into one. Past the bound, the two neighbouring ranges with the smallest gap
// between them merge. The report stays bounded, every finding stays inside a
// range, and a merge spans the smallest gap that it can.
const maxRanges = 64

// maxPlacedKeys bounds the entry keys that name a channel instance. Each new
// source IP address, Channel ID, destination port and era makes new keys, and a
// live subscription can see an unbounded number of them. The engine evicts its
// own trackers at a bound, but a report must keep what it recorded. Past this
// bound a finding goes into an overflow entry with no instance, and the report
// counts it in unplaced_findings. The counts stay exact. Only the placement is
// lost, and the report says how much of it was lost.
const maxPlacedKeys = 16384

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
	// placed counts the keys in entries that name a channel instance.
	placed int
	// unplaced counts the findings that went into an overflow entry because the
	// report held maxPlacedKeys placed keys.
	unplaced uint64
	// order numbers the findings, so that a merge keeps the detail of the
	// earliest finding.
	order uint64
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
	sourceAddr  netip.Addr
	channelID   uint8
	port        core.Port
	hasEra      bool
	resetCount  uint8
	// eraIndex is the run-local ordinal of the era. The Reset Count is a u8 and
	// wraps, so two eras 256 resets apart have the same value. The index tells
	// them apart.
	eraIndex uint64
	// overflow marks the entry that holds findings past maxPlacedKeys.
	overflow bool
}

// seqRange is one evidence range, the number of findings in it, and the detail
// of the earliest finding in it.
type seqRange struct {
	first, last uint64
	count       int
	detail      string
	order       uint64
}

type entry struct {
	count  int
	detail string
	// ranges holds the evidence, in order of first. A violation or a suspected
	// finding keeps up to maxRanges ranges, none of which touch. Any other
	// outcome keeps one range from the lowest to the highest sequence number.
	ranges []seqRange
}

// Record adds one finding. Like the other reporters it is not safe for
// concurrent use; the engine calls it from one goroutine.
func (r *RuleReport) Record(f core.Finding) {
	r.order++
	k := entryKey{ruleID: f.RuleID, status: f.Status, severity: f.Severity,
		channelID: f.ChannelID, port: f.Port}
	if f.Status == core.Unverifiable {
		k.reason = f.Reason
		if k.reason == "" {
			k.reason = core.ReasonUnspecified
		}
	}
	addr := f.SourceAddr.Unmap()
	if dst := r.ports[f.Port]; addr.Is4() && dst != 0 {
		k.hasInstance = true
		k.sourceAddr = addr
		if f.ResetCountKnown {
			k.hasEra = true
			k.resetCount = f.ResetCount
			k.eraIndex = f.EraIndex
		}
	}
	e := r.entries[k]
	if e == nil && k.hasInstance && r.placed >= maxPlacedKeys {
		// Past the bound: keep the count, drop the placement.
		k.hasInstance, k.sourceAddr = false, netip.Addr{}
		k.hasEra, k.resetCount, k.eraIndex = false, 0, 0
		k.overflow = true
		r.unplaced++
		e = r.entries[k]
	}
	if e == nil {
		e = &entry{detail: f.Detail}
		r.entries[k] = e
		if k.hasInstance {
			r.placed++
		}
	}
	e.count++
	if !k.hasEra {
		return
	}
	rg := seqRange{first: f.Seq, last: f.Seq, count: 1, detail: f.Detail, order: r.order}
	if f.Status == core.Violation || f.Status == core.Suspected {
		e.addRange(rg)
		return
	}
	if len(e.ranges) == 0 {
		e.ranges = append(e.ranges, rg)
		return
	}
	e.ranges[0] = mergeRanges(e.ranges[0], rg)
}

// touch reports whether two ranges, a before b in order of first, overlap or
// are adjacent.
func touch(a, b seqRange) bool { return b.first <= a.last || b.first-a.last == 1 }

// mergeRanges returns one range that covers a and b. It sums their counts and
// keeps the detail of the earlier finding.
func mergeRanges(a, b seqRange) seqRange {
	out := seqRange{first: min(a.first, b.first), last: max(a.last, b.last), count: a.count + b.count,
		detail: a.detail, order: a.order}
	if b.order < a.order {
		out.detail, out.order = b.detail, b.order
	}
	return out
}

// addRange adds one finding's range. It merges every range that the result
// touches, and then applies maxRanges.
func (e *entry) addRange(rg seqRange) {
	// i is the first range that does not end before rg starts.
	i, _ := slices.BinarySearchFunc(e.ranges, rg.first, func(x seqRange, seq uint64) int {
		return cmp.Compare(x.last, seq)
	})
	e.ranges = slices.Insert(e.ranges, i, rg)
	// Merge back while the range before touches this one.
	for i > 0 && touch(e.ranges[i-1], e.ranges[i]) {
		e.ranges[i-1] = mergeRanges(e.ranges[i-1], e.ranges[i])
		e.ranges = slices.Delete(e.ranges, i, i+1)
		i--
	}
	// Merge forward while this range touches the one after it.
	for i+1 < len(e.ranges) && touch(e.ranges[i], e.ranges[i+1]) {
		e.ranges[i] = mergeRanges(e.ranges[i], e.ranges[i+1])
		e.ranges = slices.Delete(e.ranges, i+1, i+2)
	}
	if len(e.ranges) <= maxRanges {
		return
	}
	// Merge the two neighbours with the smallest gap, the first such pair on a tie.
	best := 0
	for j := 1; j+1 < len(e.ranges); j++ {
		if e.ranges[j+1].first-e.ranges[j].last < e.ranges[best+1].first-e.ranges[best].last {
			best = j
		}
	}
	e.ranges[best] = mergeRanges(e.ranges[best], e.ranges[best+1])
	e.ranges = slices.Delete(e.ranges, best+1, best+2)
}

// ruleReportInstance is the channel instance: (source IP address, Channel ID,
// destination port).
type ruleReportInstance struct {
	SourceAddr string `json:"source_addr"`
	ChannelID  uint8  `json:"channel_id"`
	DstPort    uint16 `json:"dst_port"`
}

// ruleReportEvidence is the sequence range of the evidence, in one era.
// era_index is the run-local ordinal of the era: reset_count wraps after 256
// resets, and era_index does not.
type ruleReportEvidence struct {
	ResetCount uint8  `json:"reset_count"`
	EraIndex   uint64 `json:"era_index"`
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
	// Overflow is true on an entry that holds findings past maxPlacedKeys. Such an
	// entry has no instance and no evidence.
	Overflow bool `json:"overflow,omitempty"`
}

// sortedEntries returns the report entries in a deterministic order.
func (r *RuleReport) sortedEntries() []ruleReportEntry {
	out := make([]ruleReportEntry, 0, len(r.entries))
	for k, e := range r.entries {
		base := ruleReportEntry{RuleID: k.ruleID, Outcome: outcomeString(k.status),
			Severity: k.severity.String(), Reason: k.reason, ChannelID: k.channelID,
			PortRole: k.port.String(), Count: e.count, Detail: e.detail, Overflow: k.overflow}
		if k.hasInstance {
			base.Instance = &ruleReportInstance{SourceAddr: k.sourceAddr.String(), ChannelID: k.channelID,
				DstPort: r.ports[k.port]}
		}
		if len(e.ranges) == 0 {
			out = append(out, base)
			continue
		}
		// One entry for each range, so that each range is tested against the loss
		// on its own. Each entry counts the findings in its own range, so the sum
		// of the counts is the number of findings. Each entry carries the detail of
		// the earliest finding in its own range.
		for _, rg := range e.ranges {
			x := base
			x.Count = rg.count
			x.Detail = rg.detail
			x.Evidence = &ruleReportEvidence{ResetCount: k.resetCount, EraIndex: k.eraIndex,
				FirstSeq: rg.first, LastSeq: rg.last}
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
	if a.Overflow != b.Overflow {
		if a.Overflow {
			return 1
		}
		return -1
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
	if c := netip.MustParseAddr(a.SourceAddr).Compare(netip.MustParseAddr(b.SourceAddr)); c != 0 {
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
	if c := cmp.Compare(a.EraIndex, b.EraIndex); c != 0 {
		return c
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
// counts report carries. unplaced_findings is the number of findings that the
// report counted but could not place on an instance, because it held
// maxPlacedKeys placed keys. It is 0 on a run that placed every finding.
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
		Unplaced     uint64            `json:"unplaced_findings"`
		Rules        []ruleReportEntry `json:"rules"`
	}{ReportFormat: RuleReportFormat, Feed: string(r.feed), Version: meta.Version,
		Commit: meta.Commit, ReadError: readErr, CaptureDrops: meta.CaptureDrops,
		Unplaced: r.unplaced, Rules: r.sortedEntries()}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
