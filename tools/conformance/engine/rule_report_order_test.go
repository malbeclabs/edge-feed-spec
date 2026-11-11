package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
)

// TestRuleReportIsTheSameOnEveryRun checks that one input gives one rule report.
// Three instruments wait for a recovery snapshot at the end of the run, so
// checkResetSnapshotFollows emits three findings into one entry, and the entry
// keeps the detail of the first. The engine used to emit them in map order, which
// made the detail change from run to run.
func TestRuleReportIsTheSameOnEveryRun(t *testing.T) {
	const ch = uint8(1)
	instruments := []uint32{301, 302, 303}
	run := func() []byte {
		rr := report.NewRuleReport(core.FeedMBO, map[core.Port]uint16{
			core.PortMktData: 40000, core.PortSnapshot: 40002})
		e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, rr)
		// Bind the snapshot port, so that the end-of-run check grades rather than
		// reporting na.
		runStream(e, []streamEntry{
			snapEntry(buildSnapBeginFull(ch, 900, 0, 0, 88, 0), 1),
			snapEntry(buildSnapEndFull(ch, 900, 0, 88), 2),
		})
		seq := uint64(1)
		for _, id := range instruments {
			runStream(e, []streamEntry{mktdataEntrySeq(buildOrderAddFrame(ch, id, 1), seq)})
			seq++
		}
		for _, id := range instruments {
			runStream(e, []streamEntry{mktdataEntrySeq(buildInstrumentResetFrame(ch, id, seq), seq)})
			seq++
		}
		e.Flush()
		e.EndRun()
		path := filepath.Join(t.TempDir(), "rule-report.json")
		if err := report.WriteRuleReport(rr, path, report.Meta{}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := run()
	if !bytes.Contains(first, []byte("RESET.SNAPSHOT_FOLLOWS")) {
		t.Fatal("the run emitted no RESET.SNAPSHOT_FOLLOWS; the test does not reach the end-of-run loop")
	}
	for i := range 30 {
		if got := run(); !bytes.Equal(got, first) {
			t.Fatalf("run %d gave a different rule report", i+2)
		}
	}
}
