package report

import "github.com/malbeclabs/edge-feed-spec/tools/conformance/core"

// Reporter consumes both conformance findings and non-finding telemetry
// (transport health, oracle results, gauges). Findings flow through Record;
// the metric methods exist because the spec's metric set is broader than findings
// (transport loss/corruption, snapshot audits, instrument-state gauge, uptime).
type Reporter interface {
	Record(core.Finding)
	TransportLoss(port core.Port)
	TransportCorruption(port core.Port, reason string)
	SnapshotAudit(result string) // match | mismatch_suspected | mismatch_confirmed | unverifiable
	SetInstrumentState(status string, n int)
}

// Multi fans a single stream out to several reporters.
type Multi []Reporter

func (m Multi) Record(f core.Finding) {
	for _, r := range m {
		r.Record(f)
	}
}
func (m Multi) TransportLoss(p core.Port) {
	for _, r := range m {
		r.TransportLoss(p)
	}
}
func (m Multi) TransportCorruption(p core.Port, s string) {
	for _, r := range m {
		r.TransportCorruption(p, s)
	}
}
func (m Multi) SnapshotAudit(s string) {
	for _, r := range m {
		r.SnapshotAudit(s)
	}
}
func (m Multi) SetInstrumentState(s string, n int) {
	for _, r := range m {
		r.SetInstrumentState(s, n)
	}
}

// noopMetrics lets findings-only reporters (slog, json/Aggregator) satisfy Reporter.
type noopMetrics struct{}

func (noopMetrics) TransportLoss(core.Port)               {}
func (noopMetrics) TransportCorruption(core.Port, string) {}
func (noopMetrics) SnapshotAudit(string)                  {}
func (noopMetrics) SetInstrumentState(string, int)        {}

// Aggregator tracks violation counts for the CI exit code and the JSON report.
type Aggregator struct {
	noopMetrics // satisfies Reporter; Aggregator only cares about findings
	mustViol    int
	shouldViol  int
	counts      map[string]map[core.Status]int // ruleID → status → n
}

// Record tallies a finding. NOT safe for concurrent use: like the other
// reporters it relies on the engine driving all Record calls from a single
// goroutine (the Run pipeline). A future concurrent caller would need a mutex
// added here (SlogSink already has one; Prom's CounterVecs are concurrency-safe).
func (a *Aggregator) Record(f core.Finding) {
	if a.counts == nil {
		a.counts = map[string]map[core.Status]int{}
	}
	if a.counts[f.RuleID] == nil {
		a.counts[f.RuleID] = map[core.Status]int{}
	}
	a.counts[f.RuleID][f.Status]++
	if f.Status.CountsAsViolation() {
		switch f.Severity {
		case core.Must:
			a.mustViol++
		case core.Should:
			a.shouldViol++
		}
	}
}

func (a *Aggregator) ExitCode(strict bool) int {
	if a.mustViol > 0 || (strict && a.shouldViol > 0) {
		return 1
	}
	return 0
}

// Counts returns per-rule status counts for the JSON report.
func (a *Aggregator) Counts() map[string]map[core.Status]int {
	return a.counts
}
