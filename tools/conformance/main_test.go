package main

// main_test.go — the `--version` output shape.
//
// It is a cross-repo coupling rather than an internal invariant, which is why it gets
// a test: malbeclabs/infra's `dz_conformance` role parses this output to decide whether
// to re-download the binary.

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
