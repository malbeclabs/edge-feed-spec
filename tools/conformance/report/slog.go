package report

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

func statusString(s core.Status) string {
	switch s {
	case core.Pass:
		return "pass"
	case core.Violation:
		return "violation"
	case core.Suspected:
		return "suspected"
	case core.Unverifiable:
		return "unverifiable"
	case core.NA:
		return "na"
	default:
		return "unknown"
	}
}

// levelFor maps a finding to a log level by status — graded by severity only
// for a Violation. This is deliberately NOT severity-driven: a must-rule that
// produces an Unverifiable finding (loss/cold-start could not be ruled out) is
// not an error, so it must not log at ERROR. With the default WARN threshold
// the steady-state firehose (Unverifiable on a mid-stream join) is silent and
// surfaces only in the unverifiable_total metric; -v (INFO) brings it back.
//
//	ERROR  must-severity Violation        (confirmed serious non-conformance)
//	WARN   should-severity Violation, Suspected
//	INFO   info-severity Violation, Unverifiable
//	DEBUG  Pass, NA                        (observability / not-applicable)
func levelFor(f core.Finding) slog.Level {
	switch f.Status {
	case core.Violation:
		switch f.Severity {
		case core.Must:
			return slog.LevelError
		case core.Should:
			return slog.LevelWarn
		default:
			return slog.LevelInfo
		}
	case core.Suspected:
		return slog.LevelWarn
	case core.Unverifiable:
		return slog.LevelInfo
	case core.Pass, core.NA:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// throttleKey collapses findings that differ only in instrument/seq/detail into
// one rate-limit bucket. Keying on (rule,status) — not instrument — is the
// point: it bounds log volume when one rule fires across thousands of
// instruments (e.g. dangling-order Unverifiable during a mid-stream join).
type throttleKey struct {
	rule   string
	status core.Status
}

type throttleState struct {
	lastLogged time.Time
	suppressed int // findings dropped for this key since the last logged line
}

// SlogSink writes one structured log line per finding, with status-driven log
// levels and optional per-(rule,status) rate limiting for long-running use.
//
// Rate limiting affects ONLY the log line: SlogSink is one sink among several
// in the Multi fan-out, so the Aggregator (exit code) and Prom (metrics) still
// observe every finding. Suppressed findings are always counted in
// dz_conformance_*_total; the log is a human-readable sample, the metric is the
// source of truth.
type SlogSink struct {
	noopMetrics
	log      *slog.Logger
	throttle time.Duration // 0 disables throttling (log every finding)

	now func() time.Time // injectable clock; defaults to time.Now

	mu   sync.Mutex
	last map[throttleKey]throttleState
}

// NewSlogSink wraps the provided logger. Pass slog.Default() for the default
// logger. throttle is the minimum wall-clock interval between log lines for an
// identical (rule,status); 0 disables throttling.
func NewSlogSink(log *slog.Logger, throttle time.Duration) *SlogSink {
	return &SlogSink{
		log:      log,
		throttle: throttle,
		now:      time.Now,
		last:     make(map[throttleKey]throttleState),
	}
}

// Record logs the finding at its status-derived level, subject to throttling.
func (s *SlogSink) Record(f core.Finding) {
	level := levelFor(f)

	if s.throttle <= 0 {
		s.emit(level, f, 0)
		return
	}

	s.mu.Lock()
	key := throttleKey{rule: f.RuleID, status: f.Status}
	st := s.last[key]
	now := s.now()
	if !st.lastLogged.IsZero() && now.Sub(st.lastLogged) < s.throttle {
		st.suppressed++
		s.last[key] = st
		s.mu.Unlock()
		return
	}
	suppressed := st.suppressed
	s.last[key] = throttleState{lastLogged: now}
	s.mu.Unlock()

	s.emit(level, f, suppressed)
}

// emit writes a single finding line. suppressed (>0) reports how many findings
// for this (rule,status) were dropped since the previous logged line.
func (s *SlogSink) emit(level slog.Level, f core.Finding, suppressed int) {
	attrs := []slog.Attr{
		slog.String("rule_id", f.RuleID),
		slog.String("severity", f.Severity.String()),
		slog.String("status", statusString(f.Status)),
		slog.String("feed", string(f.Feed)),
		slog.String("port", f.Port.String()),
		slog.Int("channel_id", int(f.ChannelID)),
		slog.Uint64("instrument_id", uint64(f.InstrumentID)),
		slog.Uint64("seq", f.Seq),
		slog.String("detail", f.Detail),
	}
	if suppressed > 0 {
		attrs = append(attrs, slog.Int("suppressed", suppressed))
	}
	s.log.LogAttrs(context.Background(), level, "finding", attrs...)
}
