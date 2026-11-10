package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// TestFindingCarriesTheEraOfItsSequence checks that a finding about one channel
// instance names the Reset Count of the era its sequence number is in. A sequence
// number has no meaning without its era, and the rule report places evidence by
// both.
//
// FRAME.MKTDATA_SEQ_START is the hard case. Process emits it after it advances
// the era, while the datagram in front of the engine opens the new era. The
// finding cites the new era's sequence number, so it must carry the new era.
func TestFindingCarriesTheEraOfItsSequence(t *testing.T) {
	const ch = uint8(1)
	e, ac := newMBOEngineW1()
	for _, fr := range []struct {
		era uint8
		seq uint64
	}{{4, 5}, {5, 10}} {
		f, sf := wire.Decode(buildResetCountFrame(ch, fr.era, fr.seq), wire.MagicMBO)
		e.Process(srcA, f, core.PortMktData, sf)
	}
	e.Flush()

	got := findingsFor(ac, "FRAME.MKTDATA_SEQ_START")
	if len(got) != 1 {
		t.Fatalf("got %d FRAME.MKTDATA_SEQ_START findings, want 1", len(got))
	}
	f := got[0]
	if f.SourceAddr != srcA {
		t.Fatalf("finding names source %v, want %v", f.SourceAddr, srcA)
	}
	if !f.ResetCountKnown || f.ResetCount != 5 || f.Seq != 10 {
		t.Errorf("finding says era (%d, known=%v) seq %d; want era 5 seq 10: the sequence "+
			"number is in the new era", f.ResetCount, f.ResetCountKnown, f.Seq)
	}
}

// TestChannelFindingCarriesNoEra checks that a finding about the channel, and not
// about one path of it, carries no era. Every path of the channel has its own
// sequence series, so no single era applies.
func TestChannelFindingCarriesNoEra(t *testing.T) {
	const ch = uint8(1)
	e, ac := newMBOEngineW1()
	f, sf := wire.Decode(buildResetCountFrame(ch, 3, 0), wire.MagicMBO)
	e.Process(srcA, f, core.PortMktData, sf)

	// BATCH.ID_MONOTONIC reads a counter the engine keys on the channel.
	e.Emit("BATCH.ID_MONOTONIC", core.Violation, core.PortMktData, 0, ch, 0, "x")
	got := findingsFor(ac, "BATCH.ID_MONOTONIC")
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if got[0].SourceAddr.IsValid() || got[0].ResetCountKnown {
		t.Errorf("channel finding carries source %v and era known=%v; want neither",
			got[0].SourceAddr, got[0].ResetCountKnown)
	}

	// An instance-scoped rule on an instance that has no tracker has no era.
	e.Emit("FRAME.SEQ_RESET_GAP", core.Violation, core.PortSnapshot, 0, ch, 0, "x")
	got = findingsFor(ac, "FRAME.SEQ_RESET_GAP")
	if len(got) != 1 || got[0].ResetCountKnown {
		t.Errorf("finding on an instance with no tracker says era known; want unknown: %+v", got)
	}
}
