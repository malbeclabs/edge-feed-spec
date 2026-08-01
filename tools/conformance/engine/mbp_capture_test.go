package engine

import (
	"os"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/input"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// Runs the consumer over a real capture and reports how many reconstruction
// comparisons actually ran.
//
// A clean run means nothing unless the oracle compared something: every
// instrument sitting unverifiable would also produce zero findings. Skipped
// unless MBP_PCAP points at a capture.
func TestMBPCaptureOracleActuallyRuns(t *testing.T) {
	path := os.Getenv("MBP_PCAP")
	if path == "" {
		t.Skip("set MBP_PCAP to a market-by-price capture")
	}
	src, err := input.NewPcapSource(path, map[int]core.Port{
		31000: core.PortMktData, 41000: core.PortRefData, 51000: core.PortSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
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

	runs := 0
	if e.mbp != nil {
		runs = e.mbp.oracleRuns
	}
	viol := map[string]int{}
	for _, fn := range ac.findings {
		if fn.Status == core.Violation {
			viol[fn.RuleID]++
		}
	}
	t.Logf("reconstruction comparisons: %d", runs)
	t.Logf("violations: %v", viol)
	if runs == 0 {
		t.Fatal("the oracle never compared: a clean result here would be vacuous")
	}
}
