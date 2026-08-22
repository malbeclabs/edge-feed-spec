package main

// main_test.go — the `--version` output shape, and the build metadata a report is
// attributed with.
//
// Both are cross-repo couplings rather than internal invariants, which is why they
// get a test: malbeclabs/infra's `dz_conformance` role parses `--version` to decide
// whether to re-download the binary, and a JSON report with no build stamp and no
// `strict` cannot be traced to a build or resolved to an exit code.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
)

// The release form and the unstamped default are different strings, so they are
// asserted separately: one regex covering both would have to be loose enough to
// assert nothing.
var (
	// A stamped build: the release workflow validates `vMAJOR.MINOR.PATCH[-PRERELEASE]`
	// and stamps a 12-hex commit, and the print always appends `+<commit>`.
	releaseVersionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?\+[0-9a-f]{7,40}$`)
	// An unstamped `go build`.
	defaultVersionRe = regexp.MustCompile(`^dev\+none$`)
	// The consumer's regex, verbatim from malbeclabs/infra
	// playbooks/roles/dz_conformance/tasks/main.yml, applied after its leading-`v` trim.
	roleVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$`)
)

// buildDZConformance builds the binary with the given -ldflags (empty = unstamped)
// and returns its path.
func buildDZConformance(t *testing.T, ldflags string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dz-conformance")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, ".")
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("go build %v: %v\n%s", args, err, out)
	}
	return bin
}

// TestVersionFlagShape runs the real binary rather than the format string, because
// the coupling includes the -X variable names the release workflow stamps.
func TestVersionFlagShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ldflags string
		want    *regexp.Regexp
		stamped bool
	}{
		{"unstamped", "", defaultVersionRe, false},
		{"release", "-X main.version=v0.2.0 -X main.commit=85fd9b6d40cf", releaseVersionRe, true},
		// A prerelease is a supported release (the workflow sets --prerelease for a
		// `-` in the tag), and `+<commit>` after `-rc.1` is what it actually prints.
		{"prerelease", "-X main.version=v0.2.1-rc.1 -X main.commit=abc123def456", releaseVersionRe, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command(buildDZConformance(t, tc.ldflags), "--version").Output()
			if err != nil {
				t.Fatalf("--version: %v", err)
			}
			got := strings.TrimSpace(string(out))
			if !tc.want.MatchString(got) {
				t.Errorf("--version printed %q, which does not match %v: the release stamp and "+
					"the `+<commit>` suffix are what attribute a report to a build", got, tc.want)
			}
			if !tc.stamped {
				return
			}
			if trimmed := strings.TrimPrefix(got, "v"); !roleVersionRe.MatchString(trimmed) {
				t.Errorf("--version printed %q; %q does not match the dz_conformance role's %v, "+
					"so dz_conformance_download_required is permanently true and every Ansible "+
					"run re-downloads the binary and restarts every instance", got, trimmed, roleVersionRe)
			}
		})
	}
}

// jsonReport is the report's top-level shape, as a reader sees it.
type jsonReport struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Strict    bool   `json:"strict"`
	ReadError string `json:"read_error"`
	Rules     []struct {
		RuleID   string         `json:"rule_id"`
		Severity string         `json:"severity"`
		Counts   map[string]int `json:"counts"`
	} `json:"rules"`
}

func readJSONReport(t *testing.T, path string) jsonReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var rep jsonReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	return rep
}

// reconstructExitCode applies the rule the README documents for a reader of a stored
// report: a read error means the run errored, otherwise must-violations always fail and
// should-violations fail only under strict.
func reconstructExitCode(rep jsonReport) int {
	if rep.ReadError != "" {
		return 2
	}
	var must, should int
	for _, r := range rep.Rules {
		switch r.Severity {
		case "must":
			must += r.Counts["violation"]
		case "should":
			should += r.Counts["violation"]
		}
	}
	if must > 0 || (rep.Strict && should > 0) {
		return 1
	}
	return 0
}

// TestJSONReportCarriesBuildAndStrict replays the committed market-by-price capture,
// which violates rules, and checks the report says which build produced it and which
// exit-code policy it ran under.
func TestJSONReportCarriesBuildAndStrict(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "lenient"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			code := Run(RunOpts{
				Cfg: engine.Config{Feed: core.FeedMBP, Strict: strict, OracleConfirmCycles: 2, ReorderWindow: 1},
				// Ports of the committed capture (see engine/mbp_fixture_test.go).
				PcapPath:     filepath.Join("testdata", "nonconformant_mbp.pcap"),
				MktDataPort:  31000,
				RefDataPort:  41000,
				SnapshotPort: 51000,
				JSONReport:   path,
				Version:      "v0.0.0-test",
				Commit:       "abc123def456",
			})
			rep := readJSONReport(t, path)
			if rep.Version != "v0.0.0-test" || rep.Commit != "abc123def456" {
				t.Errorf("report says version=%q commit=%q, want the build stamp it ran with: "+
					"an unattributable report is findings 8.2", rep.Version, rep.Commit)
			}
			if rep.Strict != strict {
				t.Errorf("report says strict=%v, ran with %v: without the effective setting a "+
					"should-violation resolves to either exit code", rep.Strict, strict)
			}
			if len(rep.Rules) == 0 {
				t.Fatal("no rules in the report; the capture is non-conformant and must produce some")
			}
			for _, r := range rep.Rules {
				if r.Severity == "" {
					t.Errorf("rule %s has no severity, so a reader cannot tell whether its "+
						"violations move the exit code", r.RuleID)
				}
			}
			if rep.ReadError != "" {
				t.Errorf("report says read_error=%q, but the replay consumed the whole capture; "+
					"a spurious read error makes a complete run reconstruct as exit 2", rep.ReadError)
			}
			// This capture violates must rules, so the code is 1 either way; the flag
			// only changes what the report records here. TestJSONReportDeterminesExitCode
			// covers the case where it changes the code.
			if code != 1 {
				t.Errorf("Run returned %d, want 1 on a capture with must-violations", code)
			}
		})
	}
}

// TestJSONReportDeterminesExitCode is the findings 8.1 assertion: with severity per
// rule and strict at the top level, a reader reconstructs the exit code from the
// report alone. It uses a should-violation and no must-violation, the one case where
// the two modes disagree — no committed capture produces that shape.
func TestJSONReportDeterminesExitCode(t *testing.T) {
	const shouldRule = "HEARTBEAT.CHANNEL_ID_MATCH"
	if meta, ok := core.Lookup(shouldRule); !ok || meta.Severity != core.Should {
		t.Fatalf("%s is no longer a should rule; pick another for this test", shouldRule)
	}
	for _, strict := range []bool{false, true} {
		agg := &report.Aggregator{}
		agg.Record(core.Finding{RuleID: shouldRule, Severity: core.Should, Status: core.Violation})
		agg.Record(core.Finding{RuleID: "FRAME.MAGIC_MISMATCH", Severity: core.Must, Status: core.Pass})

		path := filepath.Join(t.TempDir(), "report.json")
		if err := report.JSONReport(agg, path, report.Meta{Version: "v1.2.3", Commit: "deadbeef1234", Strict: strict}); err != nil {
			t.Fatalf("JSONReport: %v", err)
		}
		rep := readJSONReport(t, path)

		reconstructed := reconstructExitCode(rep)
		if want := agg.ExitCode(strict); reconstructed != want {
			t.Errorf("strict=%v: report reconstructs exit code %d, binary exits %d "+
				"(strict recorded as %v, read_error=%q)",
				strict, reconstructed, want, rep.Strict, rep.ReadError)
		}
	}
}

// TestJSONReportRecordsReadError closes the exit-2 hole in that reconstruction. A read
// error writes the report and then returns 2, so with the counts as the only evidence a
// run that died on a truncated capture reconstructs as 0 — a clean pass, which is the
// failure this tool exists to catch. The Ansible/Alloy wrapper reads the JSON and not
// the exit code, so the report has to carry it.
func TestJSONReportRecordsReadError(t *testing.T) {
	dir := t.TempDir()
	pcapPath := writeMBOPcap(t, dir)

	// Truncate inside the packet record: the pcap file header still parses, so the
	// source opens and the run fails mid-read rather than at startup.
	st, err := os.Stat(pcapPath)
	if err != nil {
		t.Fatalf("stat pcap: %v", err)
	}
	if err := os.Truncate(pcapPath, st.Size()-8); err != nil {
		t.Fatalf("truncate pcap: %v", err)
	}

	reportPath := filepath.Join(dir, "report.json")
	code := Run(RunOpts{
		Cfg:         engine.Config{Feed: core.FeedMBO, ReorderWindow: 8},
		MktDataPort: testMktDataUDPPort,
		PcapPath:    pcapPath,
		JSONReport:  reportPath,
		Version:     "v0.0.0-test",
		Commit:      "abc123def456",
	})
	if code != 2 {
		t.Fatalf("Run returned %d on a truncated capture, want 2", code)
	}

	rep := readJSONReport(t, reportPath)
	if rep.ReadError == "" {
		t.Errorf("the run died mid-capture and exited 2, but the report records no read_error, "+
			"so a reader reconstructs %d and reads the truncated run as a pass",
			reconstructExitCode(rep))
	}
	if got := reconstructExitCode(rep); got != code {
		t.Errorf("report reconstructs exit code %d, binary exits %d (read_error=%q)",
			got, code, rep.ReadError)
	}
}
