package engine

import (
	"slices"

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
		t.Fatalf("finding names source IP address %v, want %v", f.SourceAddr, srcA)
	}
	if !f.ResetCountKnown || f.ResetCount != 5 || f.Seq != 10 {
		t.Errorf("finding says era (%d, known=%v) seq %d; want era 5 seq 10: the sequence "+
			"number is in the new era", f.ResetCount, f.ResetCountKnown, f.Seq)
	}
	// The era index counts the eras of the channel instance: 4 was the first, 5 is
	// the second.
	if f.EraIndex != 2 {
		t.Errorf("finding says era index %d, want 2", f.EraIndex)
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
		t.Errorf("channel finding carries source IP address %v and era known=%v; want neither",
			got[0].SourceAddr, got[0].ResetCountKnown)
	}

	// An instance-scoped rule on an instance that has no tracker has no era.
	e.Emit("FRAME.SEQ_RESET_GAP", core.Violation, core.PortSnapshot, 0, ch, 0, "x")
	got = findingsFor(ac, "FRAME.SEQ_RESET_GAP")
	if len(got) != 1 || got[0].ResetCountKnown {
		t.Errorf("finding on an instance with no tracker says era known; want unknown: %+v", got)
	}
}

// TestDrainedFindingCarriesItsOwnEra checks that a datagram the reset drains is
// judged in its own era. With a window of 1, the snapshot-port Heartbeat at Reset
// Count 4, seq 1 is still buffered when Reset Count 5 arrives. Process classifies
// it before it advances the era, and its finding must carry Reset Count 4. The
// datagram in front of the engine is in Reset Count 5, so an emit that read the
// era from that datagram would put the seq 1 finding in the wrong era.
func TestDrainedFindingCarriesItsOwnEra(t *testing.T) {
	e, ac := newMBOEngineW1()
	for _, fr := range []struct {
		era uint8
		seq uint64
	}{{4, 0}, {4, 1}, {5, 0}} {
		f, sf := wire.Decode(buildResetCountFrame(1, fr.era, fr.seq), wire.MagicMBO)
		e.Process(srcA, f, core.PortSnapshot, sf)
	}
	e.Flush()

	var drained, next []core.Finding
	for _, f := range ac.findings {
		switch {
		case f.Seq == 1:
			drained = append(drained, f)
		case f.ResetCount == 5:
			next = append(next, f)
		}
	}
	if len(drained) == 0 || len(next) == 0 {
		t.Fatalf("got %d findings at seq 1 and %d in the next era, want both", len(drained), len(next))
	}
	for _, f := range drained {
		if !f.ResetCountKnown || f.ResetCount != 4 || f.EraIndex != 1 {
			t.Errorf("%s at seq 1 says Reset Count %d (known=%v) era index %d; want Reset Count 4, "+
				"era index 1", f.RuleID, f.ResetCount, f.ResetCountKnown, f.EraIndex)
		}
	}
	for _, f := range next {
		if f.EraIndex != 2 {
			t.Errorf("%s in Reset Count 5 says era index %d, want 2", f.RuleID, f.EraIndex)
		}
	}
}

// TestEraIndexCountsTheErasOfOneInstance checks that the era index counts the
// eras of one channel instance, not of its channel. Mktdata sees Reset Counts 4,
// 5 and 6. The snapshot port of the same source IP address and Channel ID sends
// only in 4 and 6. A reader that counts the snapshot instance's eras counts two,
// so the snapshot findings carry 1 and 2, and the mktdata findings 1, 2 and 3.
func TestEraIndexCountsTheErasOfOneInstance(t *testing.T) {
	const ch = uint8(1)
	e, ac := newMBOEngineW1()
	send := func(port core.Port, era uint8) {
		f, sf := wire.Decode(buildResetCountFrame(ch, era, 0), wire.MagicMBO)
		e.Process(srcA, f, port, sf)
		e.Emit("FRAME.SEQ_RESET_GAP", core.Pass, port, 0, ch, 0, "probe")
	}
	send(core.PortMktData, 4)
	send(core.PortSnapshot, 4)
	send(core.PortMktData, 5)
	send(core.PortMktData, 6)
	send(core.PortSnapshot, 6)

	got := map[core.Port][]uint64{}
	for _, f := range findingsFor(ac, "FRAME.SEQ_RESET_GAP") {
		got[f.Port] = append(got[f.Port], f.EraIndex)
	}
	want := map[core.Port][]uint64{core.PortMktData: {1, 2, 3}, core.PortSnapshot: {1, 2}}
	for port, w := range want {
		if !slices.Equal(got[port], w) {
			t.Errorf("%v era indexes %v, want %v", port, got[port], w)
		}
	}
}

// TestEraIndexSurvivesTrackerEviction checks that the era index does not restart
// when the engine evicts a tracker. The instance is in its second era (Reset
// Count 5) when its tracker is evicted. The tracker comes back in Reset Count 5,
// so it continues the second era, and Reset Count 6 is the third. A tracker's own
// era ordinal restarts at 1 after eviction and would say 1 and 2.
func TestEraIndexSurvivesTrackerEviction(t *testing.T) {
	const ch = uint8(1)
	e, ac := newMBOEngineW1()
	send := func(era uint8, seq uint64) {
		f, sf := wire.Decode(buildResetCountFrame(ch, era, seq), wire.MagicMBO)
		e.Process(srcA, f, core.PortMktData, sf)
		e.Emit("FRAME.SEQ_RESET_GAP", core.Pass, core.PortMktData, seq, ch, 0, "probe")
	}
	send(4, 0)
	send(5, 0)
	e.Flush()
	delete(e.ports, instanceKey{src: srcA, port: core.PortMktData, ch: ch}) // the eviction
	send(5, 1)
	send(6, 0)

	var got []uint64
	for _, f := range findingsFor(ac, "FRAME.SEQ_RESET_GAP") {
		got = append(got, f.EraIndex)
	}
	if want := []uint64{1, 2, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("era indexes %v, want %v", got, want)
	}
}
