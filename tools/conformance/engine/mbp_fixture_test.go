package engine

import (
	"path/filepath"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/input"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// Runs the consumer over `testdata/nonconformant_mbp.pcap` — a real capture from
// a real publisher on live venue data, not a hand-built frame sequence.
//
// **Why a real capture earns its size here.** The synthetic tests beside this one
// cover every rule with a conformant and a violating vector, and they passed
// while the consumer had three separate defects: it rewound the per-instrument
// tracker on every snapshot, it reported duplicates the spec says to discard, and
// it compared against a journal that had not reached the snapshot's `Last
// Instrument Seq`. All three were found by pointing it at this data. Frames built
// by the same person who wrote the decoder share that person's misreadings; a
// venue's do not.
//
// The capture is deliberately **not** conformant, and named for it. It was taken
// before malbeclabs/kalshi#63, so it carries two real publisher defects, which is
// what makes it a regression test rather than a smoke test.
func TestNonconformantMBPCapture(t *testing.T) {
	path := filepath.Join("..", "testdata", "nonconformant_mbp.pcap")
	src, err := input.NewPcapSource(path, map[int]core.Port{
		31000: core.PortMktData, 41000: core.PortRefData, 51000: core.PortSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBP, SourceRegistry: stubRegistry{}}, ac)
	for {
		dg, ok, err := src.Next()
		if err != nil || !ok {
			break
		}
		f, sf := wire.Decode(dg.Raw, wire.MagicMBP)
		e.Process(f, dg.Port, sf)
	}
	e.Flush()

	viol := map[string]int{}
	for _, fn := range ac.findings {
		if fn.Status == core.Violation {
			viol[fn.RuleID]++
		}
	}

	// The two defects the capture was taken before the fix for. Frames of 1,464
	// bytes against the family's 1,232 cap, and `ManifestSummary` on the refdata
	// port with the snapshot flag set.
	for _, rule := range []string{"FRAME.LENGTH_CONSISTENCY", "MSG.SNAPSHOT_FLAG_MATCHES_PORT"} {
		if viol[rule] == 0 {
			t.Errorf("%s: expected the capture's known defect to be reported", rule)
		}
	}

	// **The reconstruction must agree.** This is the positive claim, and the one
	// the whole consumer exists to make: the ladder rebuilt independently from the
	// delta stream matches the publisher's own snapshots. A regression that breaks
	// replay shows up here and nowhere else.
	if n := viol["MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"]; n != 0 {
		t.Errorf("reconstruction diverged from the snapshots %d time(s); the publisher's book was sound in this capture", n)
	}

	// And it must have actually compared something. Zero findings is also what a
	// run where every instrument sat unverifiable would produce, so without this
	// the assertion above is satisfied by doing nothing.
	if e.mbp == nil || e.mbp.oracleRuns == 0 {
		t.Fatal("the oracle never compared: the clean result above would be vacuous")
	}
	t.Logf("reconstruction comparisons: %d, violations: %v", e.mbp.oracleRuns, viol)
}
