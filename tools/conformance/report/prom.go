package report

import (
	"net/http"
	"slices"
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

	startTime time.Time
}

// NewProm creates and registers all dz_conformance_* metrics into the provided registry.
// version and commit label values are set on build_info immediately. feed selects which
// rules are published to rule_info (only rules applicable to the active feed).
func NewProm(reg *prometheus.Registry, version, commit string, feed core.Feed) *Prom {
	p := &Prom{
		registry:  reg,
		startTime: time.Now(),
	}

	p.violations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "violations_total",
		Help:      "Total confirmed conformance violations by feed, rule, and severity.",
	}, []string{"feed", "rule_id", "severity"})

	p.unverifiable = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "unverifiable_total",
		Help:      "Total checks that could not be verified, by feed, rule, and reason.",
	}, []string{"feed", "rule_id", "reason"})

	p.checks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "checks_total",
		Help:      "Total checks by feed, rule, and result.",
	}, []string{"feed", "rule_id", "result"})

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
		p.ruleInfo.WithLabelValues(r.ID, r.Severity.String(), doc.Summary, core.SpecURL(r.ID)).Set(1)
	}

	return p
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

	p.checks.WithLabelValues(feed, f.RuleID, result).Inc()

	switch f.Status {
	case core.Violation:
		p.violations.WithLabelValues(feed, f.RuleID, sev).Inc()
	case core.Unverifiable:
		// reason MUST be a bounded enum (Finding.Reason), never the free-form Detail,
		// to keep this metric's cardinality bounded.
		reason := f.Reason
		if reason == "" {
			reason = "unspecified"
		}
		p.unverifiable.WithLabelValues(feed, f.RuleID, reason).Inc()
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
