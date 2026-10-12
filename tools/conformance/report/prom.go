package report

import (
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const promNamespace = "dz_conformance"

// Prom is a prometheus-backed Reporter. Construct with NewProm; never use the global
// default registry — the registry is always injected.
type Prom struct {
	registry *prometheus.Registry

	// Finding metrics
	violations    *prometheus.CounterVec
	unverifiable  *prometheus.CounterVec
	checks        *prometheus.CounterVec
	transportLoss *prometheus.CounterVec
	transportCorr *prometheus.CounterVec
	snapshotAudit *prometheus.CounterVec

	// Gauge metrics
	instrumentState *prometheus.GaugeVec
	buildInfo       *prometheus.GaugeVec
	ruleInfo        *prometheus.GaugeVec
	uptimeSeconds   prometheus.GaugeFunc

	// srcAddrs holds the publisher addresses that have their own source_addr
	// label value. See maxSourceAddrLabels. srcMu guards it, because the
	// CounterVecs are safe for concurrent use and this map must not change that.
	srcMu    sync.Mutex
	srcAddrs map[netip.Addr]struct{}

	startTime time.Time
}

// NewProm creates and registers all dz_conformance_* metrics into the provided registry.
// version and commit label values are set on build_info immediately. feed selects which
// rules are published to rule_info (only rules applicable to the active feed).
func NewProm(reg *prometheus.Registry, version, commit string, feed core.Feed) *Prom {
	p := &Prom{
		registry:  reg,
		srcAddrs:  make(map[netip.Addr]struct{}),
		startTime: time.Now(),
	}

	p.violations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "violations_total",
		Help:      "Total confirmed conformance violations by feed, rule, severity, and publisher source address. `source_addr` names the publisher whose datagrams the rule judged; it is EMPTY for a rule decided over state every path of the channel fills (the book, the snapshot groups, the reference data, the counters keyed on the channel), where no single publisher is the subject. An empty value is a statement, not an unknown. The value `overflow` holds every publisher past the first 32 addresses.",
	}, []string{"feed", "rule_id", "severity", "source_addr"})

	p.unverifiable = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "unverifiable_total",
		Help:      "Total checks that could not be verified, by feed, rule, cause, and publisher source address. `reason` is a closed enum; it breaks a rule's shortfall against checks_total down by what stopped it. See violations_total for what an empty `source_addr` means.",
	}, []string{"feed", "rule_id", "reason", "source_addr"})

	p.checks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "checks_total",
		Help:      "Total checks by feed, rule, result (pass|violation|suspected|unverifiable|na), and publisher source address. Summed over `result` this is a rule's denominator: for a rule whose execution is conditional, compare result=\"pass\" against it to read the coverage actually achieved rather than assuming silence means clean. Summing over `source_addr` as well gives the channel's denominator; see violations_total for what an empty value means.",
	}, []string{"feed", "rule_id", "result", "source_addr"})

	p.transportLoss = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "transport_loss_total",
		Help:      "Total transport packet loss events by port.",
	}, []string{"port"})

	p.transportCorr = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "transport_corruption_total",
		Help:      "Total transport corruption events by port and reason.",
	}, []string{"port", "reason"})

	p.snapshotAudit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "snapshot_audits_total",
		Help:      "Total snapshot audits by result.",
	}, []string{"result"})

	p.instrumentState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: promNamespace,
		Name:      "instruments_state",
		Help:      "Current number of instruments in each state.",
	}, []string{"status"})

	p.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: promNamespace,
		Name:      "build_info",
		Help:      "Build metadata (always 1).",
	}, []string{"version", "commit"})

	p.ruleInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: promNamespace,
		Name:      "rule_info",
		Help:      "Static per-rule metadata (always 1): severity, one-line summary, and spec link. Join by rule_id to label the finding metrics.",
	}, []string{"rule_id", "severity", "summary", "spec_url"})

	p.uptimeSeconds = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: promNamespace,
		Name:      "uptime_seconds",
		Help:      "Seconds since the process started.",
	}, func() float64 {
		return time.Since(p.startTime).Seconds()
	})

	reg.MustRegister(
		p.violations,
		p.unverifiable,
		p.checks,
		p.transportLoss,
		p.transportCorr,
		p.snapshotAudit,
		p.instrumentState,
		p.buildInfo,
		p.ruleInfo,
		p.uptimeSeconds,
	)

	// Set build_info immediately so the label set is not dead.
	p.buildInfo.WithLabelValues(version, commit).Set(1)

	// Publish static rule_info for every rule applicable to the active feed, so
	// a Grafana table can list all rules (even those that never fire a finding)
	// and join their summary + spec link by rule_id.
	for _, r := range core.Rules {
		if !feedApplies(feed, r.Feeds) {
			continue
		}
		doc, _ := core.Doc(r.ID)
		p.ruleInfo.WithLabelValues(r.ID, r.Severity.String(), doc.Summary, core.SpecURL(r.ID, feed)).Set(1)
	}

	return p
}

// maxSourceAddrLabels is the number of publisher addresses that get their own
// source_addr label value. A redundant feed has two publishers, so the limit is
// far above a real deployment.
//
// The limit is necessary because the address is not ours to trust. An any-source
// join accepts a datagram from any sender, and a publisher address is a tunnel
// lease that can change under a live host. The engine evicts an old instance
// (engine.maxChannelInstances), but a CounterVec keeps each label value until
// the process stops. Without the limit, each new address adds series for the
// life of the process.
const maxSourceAddrLabels = 32

// sourceAddrOverflow is the source_addr label value for a finding that names a
// publisher after maxSourceAddrLabels addresses have a value of their own. It is
// not an address and it is not the empty value: the rule does have one path as
// its subject, and the reporter does not name it. The finding is still counted,
// so the overflow costs attribution and never coverage.
const sourceAddrOverflow = "overflow"

// sourceAddrLabel renders the publisher that a finding judges. It returns "" when
// the finding names no publisher, which is the case for a rule that is not
// instance-scoped (core.StateKind.InstanceScoped).
//
// **The empty string is the honest value and must stay empty.** A placeholder such
// as "unknown" or "0.0.0.0" reads as an address the checker failed to determine,
// when what it actually means is that the rule has no single path as its subject.
// A consumer grouping by this label gets one series for the channel-scoped rules,
// which is exactly one bucket for a set of verdicts that share a subject.
//
// Cardinality is bounded by maxSourceAddrLabels and not by the publishers on the
// group. The first maxSourceAddrLabels addresses each get a label value. Each
// later address gets sourceAddrOverflow. Only an instance-scoped rule carries an
// address, so the other rules stay at one series apiece.
func (p *Prom) sourceAddrLabel(f core.Finding) string {
	if !f.SourceAddr.IsValid() {
		return ""
	}
	p.srcMu.Lock()
	defer p.srcMu.Unlock()
	if _, ok := p.srcAddrs[f.SourceAddr]; !ok {
		if len(p.srcAddrs) >= maxSourceAddrLabels {
			return sourceAddrOverflow
		}
		p.srcAddrs[f.SourceAddr] = struct{}{}
	}
	return f.SourceAddr.String()
}

// feedApplies reports whether feed is in the rule's applicable-feeds list.
func feedApplies(feed core.Feed, feeds []core.Feed) bool {
	return slices.Contains(feeds, feed)
}

// Record implements Reporter. It always bumps checks_total; additionally bumps
// violations_total for Violation and unverifiable_total for Unverifiable.
func (p *Prom) Record(f core.Finding) {
	feed := string(f.Feed)
	sev := f.Severity.String()
	result := statusString(f.Status)
	src := p.sourceAddrLabel(f)

	p.checks.WithLabelValues(feed, f.RuleID, result, src).Inc()

	switch f.Status {
	case core.Violation:
		p.violations.WithLabelValues(feed, f.RuleID, sev, src).Inc()
	case core.Unverifiable:
		// reason MUST be a bounded enum (Finding.Reason), never the free-form Detail,
		// to keep this metric's cardinality bounded.
		reason := f.Reason
		if reason == "" {
			reason = "unspecified"
		}
		p.unverifiable.WithLabelValues(feed, f.RuleID, reason, src).Inc()
	}
}

// TransportLoss implements Reporter.
func (p *Prom) TransportLoss(port core.Port) {
	p.transportLoss.WithLabelValues(port.String()).Inc()
}

// TransportCorruption implements Reporter.
func (p *Prom) TransportCorruption(port core.Port, reason string) {
	p.transportCorr.WithLabelValues(port.String(), reason).Inc()
}

// SnapshotAudit implements Reporter.
func (p *Prom) SnapshotAudit(result string) {
	p.snapshotAudit.WithLabelValues(result).Inc()
}

// SetInstrumentState implements Reporter. It sets the gauge for the given status
// to n (replacing any previous value).
func (p *Prom) SetInstrumentState(status string, n int) {
	p.instrumentState.WithLabelValues(status).Set(float64(n))
}

// Handler returns an http.Handler that exposes the injected registry over HTTP.
func (p *Prom) Handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// Healthz returns a simple liveness handler that always responds 200 OK.
func (p *Prom) Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
