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
	e.EndRun()

	viol, unver := map[string]int{}, map[string]int{}
	for _, fn := range ac.findings {
		switch fn.Status {
		case core.Violation:
			viol[fn.RuleID]++
		case core.Unverifiable:
			unver[fn.RuleID]++
		}
	}

	// **Pinned exactly, not asserted non-zero.** A floor of "> 0" passes while a
	// regression quietly guts coverage, which is the failure this fixture exists to
	// prevent — the consumer once compared 102 of 344 groups and every test still
	// passed. Every number below is what a correct run produces on these bytes; a
	// change to any of them is either a real regression or a deliberate improvement
	// that belongs in the same commit as the new number.
	//
	// The first two are the capture's known publisher defects: frames of 1,464 bytes
	// against the family's 1,232 cap, and `ManifestSummary` on the refdata port with
	// the snapshot flag set.
	for _, c := range []struct {
		rule string
		want int
	}{
		{"FRAME.LENGTH_CONSISTENCY", 619},
		{"MSG.SNAPSHOT_FLAG_MATCHES_PORT", 6},
		// **Zero, and that is the point of the loss gates.** The snapshot port in this
		// capture is clean, its 38 completed groups are all well-formed, and one lost
		// datagram used to turn any of them into ~746 MUST findings.
		{"MBP.SNAP.GROUP_STRUCTURE", 0},
		// **The reconstruction agrees.** The positive claim the whole consumer exists to
		// make: the ladder rebuilt independently from the delta stream matches the
		// publisher's own snapshots. A regression that breaks replay shows up here and
		// nowhere else.
		{"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", 0},
		{"MBP.DELTA.PERINSTR_DENSITY", 0},
		{"MBP.DELTA.PERINSTR_NO_SNAPSHOT_RESET", 0},
		{"MBP.DELTA.ABSOLUTE_APPLY", 0},
	} {
		if got := viol[c.rule]; got != c.want {
			t.Errorf("%s: %d violations, want exactly %d", c.rule, got, c.want)
		}
	}

	// The capture ends mid-group, as every finite capture does: 39 `SnapshotBegin`
	// against 38 `SnapshotEnd`. That is reported, and reported as unverifiable —
	// the observation window closing is not something the publisher did.
	if got := unver["MBP.SNAP.GROUP_STRUCTURE"]; got != 1 {
		t.Errorf("truncated final group: %d unverifiable, want exactly 1", got)
	}

	// And the oracle must have actually compared something: zero reconstruction
	// findings is also what a run where every instrument sat unverifiable produces,
	// so without a floor the assertion above is satisfied by doing nothing.
	//
	// Five of the 38 completed groups. The other 33 are not skipped for lack of
	// trust: 29 are an instrument's first group, adopted as its baseline because
	// there is nothing yet to replay against, and 4 are groups whose `Last
	// Instrument Seq` the consumer had not yet applied through when they were
	// classified — the ports drain independently. The capture spans about one
	// snapshot cycle, so one comparison per instrument that got a second group is
	// the ceiling the bytes allow.
	if e.mbp == nil {
		t.Fatal("no market-by-price state: the consumer never ran")
	}
	if e.mbp.oracleRuns < 5 {
		t.Errorf("the oracle compared %d group(s), want at least 5; the clean result above is vacuous below that",
			e.mbp.oracleRuns)
	}
	t.Logf("reconstruction comparisons: %d, violations: %v, unverifiable: %v", e.mbp.oracleRuns, viol, unver)
}
