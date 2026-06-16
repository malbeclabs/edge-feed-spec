package report

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// newTestSink builds a SlogSink writing to buf at Debug level (so level
// filtering never hides a line) with the given throttle interval.
func newTestSink(buf *bytes.Buffer, throttle time.Duration) *SlogSink {
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return NewSlogSink(slog.New(h), throttle)
}

func countFindings(out string) int { return strings.Count(out, "msg=finding") }

func lastFindingLine(out string) string {
	var last string
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, "msg=finding") {
			last = ln
		}
	}
	return last
}

// TestSlogLevelByStatus locks in the firehose fix: log level is driven by
// status (and severity only to grade a Violation), NOT by severity alone. The
// key case is a must-rule Unverifiable finding logging at INFO, not ERROR.
func TestSlogLevelByStatus(t *testing.T) {
	cases := []struct {
		name   string
		sev    core.Severity
		status core.Status
		want   string
	}{
		{"must violation", core.Must, core.Violation, "ERROR"},
		{"should violation", core.Should, core.Violation, "WARN"},
		{"info violation", core.Info, core.Violation, "INFO"},
		{"suspected", core.Must, core.Suspected, "WARN"},
		{"must unverifiable", core.Must, core.Unverifiable, "INFO"},
		{"should unverifiable", core.Should, core.Unverifiable, "INFO"},
		{"pass", core.Must, core.Pass, "DEBUG"},
		{"na", core.Must, core.NA, "DEBUG"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := newTestSink(&buf, 0)
			s.Record(core.Finding{RuleID: "R", Severity: c.sev, Status: c.status})
			if got := buf.String(); !strings.Contains(got, "level="+c.want) {
				t.Errorf("sev=%v status=%v: want level=%s, got %q", c.sev, c.status, c.want, got)
			}
		})
	}
}

// TestSlogThrottleSuppresses verifies that within one throttle window only the
// first finding for a (rule,status) key is logged, and the next line after the
// window carries the suppressed count.
func TestSlogThrottleSuppresses(t *testing.T) {
	var buf bytes.Buffer
	s := newTestSink(&buf, time.Second)
	cur := time.Unix(1000, 0)
	s.now = func() time.Time { return cur }

	f := core.Finding{RuleID: "R", Severity: core.Must, Status: core.Violation, Detail: "x"}
	for range 5 {
		s.Record(f)
	}
	if n := countFindings(buf.String()); n != 1 {
		t.Fatalf("want 1 line within throttle window, got %d\n%s", n, buf.String())
	}

	cur = cur.Add(2 * time.Second)
	s.Record(f)
	out := buf.String()
	if n := countFindings(out); n != 2 {
		t.Fatalf("want 2 lines after window, got %d\n%s", n, out)
	}
	if !strings.Contains(lastFindingLine(out), "suppressed=4") {
		t.Errorf("want suppressed=4 on post-window line, got %q", lastFindingLine(out))
	}
}

// TestSlogThrottleFirstLineNoSuppressed ensures the suppressed attribute is
// omitted when zero (no clutter on the common path).
func TestSlogThrottleFirstLineNoSuppressed(t *testing.T) {
	var buf bytes.Buffer
	s := newTestSink(&buf, time.Second)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	s.Record(core.Finding{RuleID: "R", Severity: core.Must, Status: core.Violation})
	if strings.Contains(buf.String(), "suppressed=") {
		t.Errorf("first line must not carry a suppressed attr, got %q", buf.String())
	}
}

// TestSlogThrottleDistinctKeys verifies different (rule,status) keys throttle
// independently.
func TestSlogThrottleDistinctKeys(t *testing.T) {
	var buf bytes.Buffer
	s := newTestSink(&buf, time.Second)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	s.Record(core.Finding{RuleID: "A", Severity: core.Must, Status: core.Violation})
	s.Record(core.Finding{RuleID: "B", Severity: core.Must, Status: core.Violation})
	s.Record(core.Finding{RuleID: "A", Severity: core.Must, Status: core.Unverifiable})
	if n := countFindings(buf.String()); n != 3 {
		t.Fatalf("distinct (rule,status) keys must not throttle each other; want 3, got %d", n)
	}
}

// TestSlogThrottleZeroDisables verifies throttle=0 logs every finding.
func TestSlogThrottleZeroDisables(t *testing.T) {
	var buf bytes.Buffer
	s := newTestSink(&buf, 0)
	f := core.Finding{RuleID: "R", Severity: core.Must, Status: core.Violation}
	for range 5 {
		s.Record(f)
	}
	if n := countFindings(buf.String()); n != 5 {
		t.Fatalf("throttle=0 must log all findings; want 5, got %d", n)
	}
}
