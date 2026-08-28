package engine

// refdata_instance_test.go — the refdata set-state machine under two publishers.
//
// Reset Count belongs to the channel instance — `(source address, Channel ID,
// destination port)` — the same axis datagram sequencing keys on (GLOSSARY.md, and
// "Redundant Channel Instances" in market-by-price/spec.md). Two publishers
// serving one group and one port advance independent Reset Counts, and neither
// is the port's.
//
// Tracked per port instead, every alternation between the paths reads as an era
// change, and each one discards both paths' definition sets. The damage is silent
// end to end: manifests re-seed `valid` a second later so nothing looks stuck,
// definitions arriving in their cycle land on an invalid channel and
// onInstrumentDef drops them without a finding, and `ready()` — `valid &&
// len(defs) == expected_count` — is never reached. The rules that consume
// refdata then report NA rather than pass or violation, which is indistinguishable
// from a healthy feed in every counter an operator watches.
//
// The two tests are converses: that the paths no longer erase each other, and that
// a real Reset Count change still discards that channel's state and leaves the
// other channel's standing. It does not prove per-instance isolation on one
// Channel ID: `refdataState.channels` is Channel-ID-keyed, so two instances
// sharing an id would still share a set. That is pre-existing and inert while
// redundant paths carry distinct ids.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// refdataPath is one publisher's refdata datagrams: its own Reset Count, its own
// sequence space, its own Channel ID.
type refdataPath struct {
	ch  uint8
	era uint8
}

// sendManifest feeds one ManifestSummary(valid=1) for this path.
func (a refdataPath) sendManifest(e *Engine, seq uint64, mseq uint16, count uint32, sendTS uint64) {
	raw := wb.Frame(wire.MagicTOB).
		Channel(a.ch).
		Seq(seq).
		ResetCount(a.era).
		SendTS(sendTS).
		Msg(0x07, 24, manifestBody(1, mseq, count)).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	e.Process(srcA, f, core.PortRefData, sf)
}

// sendDef feeds one InstrumentDefinition for this path.
func (a refdataPath) sendDef(e *Engine, seq uint64, instrID uint32, mseq uint16, sendTS uint64) {
	raw := wb.Frame(wire.MagicTOB).
		Channel(a.ch).
		Seq(seq).
		ResetCount(a.era).
		SendTS(sendTS).
		Msg(0x02, 130, instrDefTOBBody(instrID, mseq)).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	e.Process(srcA, f, core.PortRefData, sf)
}

// readyOn reports whether one channel has reached ready(), with its def count.
func readyOn(e *Engine, ch uint8) (bool, int) {
	if e.refdata == nil {
		return false, 0
	}
	s, ok := e.refdata.channels[ch]
	if !ok {
		return false, 0
	}
	return channelReady(s), len(s.defs)
}

// TestRefdataEraKeysOnTheChannelInstance: two paths on one port, each with its own
// Reset Count, interleaved as they arrive on one socket. Both must establish their
// sets. Keyed per port, every alternation resets both and neither ever does.
func TestRefdataEraKeysOnTheChannelInstance(t *testing.T) {
	pathA := refdataPath{ch: chArmA, era: 9}
	pathB := refdataPath{ch: chArmB, era: 5}

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	// Manifests alternate at 1/s per path, as the publishers pace them.
	var seq uint64
	for i := 0; i < 4; i++ {
		seq++
		pathA.sendManifest(e, seq, 3, 2, uint64(i)*nsPerSec)
		seq++
		pathB.sendManifest(e, seq, 2, 2, uint64(i)*nsPerSec+nsPerSec/2)
	}
	// Definitions arrive later in the cycle, after several alternations.
	for _, instr := range []uint32{1, 2} {
		seq++
		pathA.sendDef(e, seq, instr, 3, 5*nsPerSec)
		seq++
		pathB.sendDef(e, seq, instr, 2, 5*nsPerSec)
	}
	// One more manifest per path, then flush: the state must survive both.
	seq++
	pathA.sendManifest(e, seq, 3, 2, 6*nsPerSec)
	seq++
	pathB.sendManifest(e, seq, 2, 2, 6*nsPerSec)
	e.Flush()

	for _, path := range []refdataPath{pathA, pathB} {
		ready, defs := readyOn(e, path.ch)
		if !ready {
			t.Errorf("channel %d: ready() is false with %d/2 definitions; the other path's "+
				"Reset Count is erasing this path's set on every alternation", path.ch, defs)
		}
	}
}

// TestRefdataResetDiscardsOnlyItsOwnInstance: the converse. A real Reset Count
// change on one path must still discard that path's set — the fix must not turn
// reset handling off — and must leave the other path's set established.
func TestRefdataResetDiscardsOnlyItsOwnInstance(t *testing.T) {
	pathA := refdataPath{ch: chArmA, era: 9}
	pathB := refdataPath{ch: chArmB, era: 5}

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	var seq uint64
	for _, path := range []refdataPath{pathA, pathB} {
		seq++
		path.sendManifest(e, seq, uint16(path.ch), 2, 0)
		for _, instr := range []uint32{1, 2} {
			seq++
			path.sendDef(e, seq, instr, uint16(path.ch), nsPerSec)
		}
	}
	seq++
	pathA.sendManifest(e, seq, uint16(pathA.ch), 2, 2*nsPerSec)
	e.Flush()

	if ready, defs := readyOn(e, pathB.ch); !ready {
		t.Fatalf("setup: channel %d never established its set (%d/2 definitions)", pathB.ch, defs)
	}

	// Path A restarts: same channel, new Reset Count.
	restarted := refdataPath{ch: chArmA, era: 10}
	seq++
	restarted.sendManifest(e, seq, 77, 2, 3*nsPerSec)
	seq++
	restarted.sendManifest(e, seq, 77, 2, 4*nsPerSec)
	e.Flush()

	if ready, defs := readyOn(e, pathA.ch); ready {
		t.Errorf("channel %d: ready() survived its own Reset Count change with %d definitions; "+
			"a reset must discard the cached set", pathA.ch, defs)
	}
	if ready, defs := readyOn(e, pathB.ch); !ready {
		t.Errorf("channel %d: ready() was lost when the OTHER path reset (%d/2 definitions); "+
			"a reset belongs to the instance that sent it", pathB.ch, defs)
	}
}
