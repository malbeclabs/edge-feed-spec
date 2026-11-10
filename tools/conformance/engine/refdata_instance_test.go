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
// The first two tests are converses: that the paths no longer erase each other, and
// that a real Reset Count change still discards that channel's state and leaves the
// other channel's standing.
//
// The tests after them put two instances on ONE Channel ID, told apart only by the
// source address. Keyed by Channel ID, a replacement publisher that starts a new era
// at a lower Manifest Seq inherited the old publisher's latest_seq: each of its
// summaries read as REFDATA.SEQ_MONOTONIC_NO_REGRESS, and each of its definitions as
// REFDATA.STALE_SEQ_TAG_AFTER_BUMP, until its Manifest Seq passed the old one. The
// converse test shows that a regression inside one instance is still reported.

import (
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// refdataPath is one publisher's refdata datagrams: its own source address, its
// own Reset Count, its own sequence space, its own Channel ID. The zero src is srcA.
type refdataPath struct {
	src netip.Addr
	ch  uint8
	era uint8
}

func (a refdataPath) source() netip.Addr {
	if a.src.IsValid() {
		return a.src
	}
	return srcA
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
	e.Process(a.source(), f, core.PortRefData, sf)
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
	e.Process(a.source(), f, core.PortRefData, sf)
}

// readyOn reports whether one channel has reached ready(), with its def count.
func readyOn(e *Engine, ch uint8) (bool, int) {
	if e.refdata == nil {
		return false, 0
	}
	s, ok := e.refdata.channels[refdataKey{src: srcA, ch: ch}]
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

// incidentChannel is the Channel ID that a replaced publisher and its replacement
// both serve in the tests below.
const incidentChannel uint8 = 150

// countStatus counts the findings of one rule with one status.
func countStatus(ac *allCapture, ruleID string, st core.Status) int {
	n := 0
	for _, f := range findingsFor(ac, ruleID) {
		if f.Status == st {
			n++
		}
	}
	return n
}

// establish sends one complete, conformant cycle for a path: a summary at mseq for
// the instruments, each instrument tagged mseq, and one more summary. It returns the
// next datagram sequence number.
func (a refdataPath) establish(e *Engine, seq uint64, mseq uint16, instrs []uint32, sendTS uint64) uint64 {
	a.sendManifest(e, seq, mseq, uint32(len(instrs)), sendTS)
	seq++
	for _, id := range instrs {
		a.sendDef(e, seq, id, mseq, sendTS+nsPerSec/10)
		seq++
	}
	a.sendManifest(e, seq, mseq, uint32(len(instrs)), sendTS+nsPerSec)
	return seq + 1
}

// readyAt reports whether one channel instance has reached ready(), with its
// Manifest Seq.
func readyAt(e *Engine, src netip.Addr, ch uint8) (bool, uint16) {
	if e.refdata == nil {
		return false, 0
	}
	s, ok := e.refdata.channels[refdataKey{src: src, ch: ch}]
	if !ok {
		return false, 0
	}
	return channelReady(s), s.latestSeq
}

// TestRefdataReplacementInstanceStartsCold: a publisher on Channel ID 150 at Manifest
// Seq 206 stops, and a replacement publisher at a new source address starts the same
// Channel ID on the same group and port in a new era at Manifest Seq 4, with every
// definition tagged 4. The replacement is conformant on its own. It must not be
// judged against the old publisher's Manifest Seq.
func TestRefdataReplacementInstanceStartsCold(t *testing.T) {
	old := refdataPath{src: srcA, ch: incidentChannel, era: 3}
	replacement := refdataPath{src: srcB, ch: incidentChannel, era: 0}

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	old.establish(e, 1, 206, []uint32{500, 501}, 0)
	e.Flush()
	if ready, mseq := readyAt(e, srcA, incidentChannel); !ready || mseq != 206 {
		t.Fatalf("setup: old instance ready=%v at Manifest Seq %d, want ready at 206", ready, mseq)
	}

	// The replacement runs several cycles, as it does in service.
	seq := uint64(1)
	for i := uint64(0); i < 3; i++ {
		seq = replacement.establish(e, seq, 4, []uint32{1, 2}, (10+2*i)*nsPerSec)
	}
	e.Flush()

	for _, rule := range []string{"REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP", "REFDATA.SEQ_BUMP_NOT_BY_ONE"} {
		if found := findingsFor(ac, rule); len(found) != 0 {
			t.Errorf("%s: %d finding(s) against a replacement instance that is conformant on its own (%s); it was judged against the old instance's Manifest Seq",
				rule, len(found), found[0].Detail)
		}
	}
	if ready, mseq := readyAt(e, srcB, incidentChannel); !ready || mseq != 4 {
		t.Errorf("replacement instance ready=%v at Manifest Seq %d, want ready at 4", ready, mseq)
	}
	if ready, mseq := readyAt(e, srcA, incidentChannel); !ready || mseq != 206 {
		t.Errorf("old instance ready=%v at Manifest Seq %d, want its own set left as it was at 206", ready, mseq)
	}

	// A quote is judged on the set of the publisher that sent it. Instrument 1 is in
	// the replacement's set and not in the old one.
	quote := func(src netip.Addr, seq uint64) {
		raw := wb.Frame(wire.MagicTOB).Channel(incidentChannel).Seq(seq).SendTS(20*nsPerSec).
			Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 1, 1)).Bytes()
		f, sf := wire.Decode(raw, wire.MagicTOB)
		e.Process(src, f, core.PortMktData, sf)
	}
	quote(srcB, 1)
	e.Flush()
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Pass); n != 1 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: want 1 pass for the replacement's quote of an instrument in its own set, got %d", n)
	}
	quote(srcA, 1)
	e.Flush()
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 1 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: want 1 violation for the old instance's quote of an instrument not in its set, got %d", n)
	}
}

// TestRefdataRedundantInstancesOnOneChannelKeepTheirOwnState: two publishers serve one
// Channel ID at the same time, each with its own Reset Count and its own Manifest Seq,
// and their datagrams interleave on one socket. Each one is conformant on its own, so
// both reach ready and neither is reported.
func TestRefdataRedundantInstancesOnOneChannelKeepTheirOwnState(t *testing.T) {
	pathA := refdataPath{src: srcA, ch: incidentChannel, era: 9}
	pathB := refdataPath{src: srcB, ch: incidentChannel, era: 2}

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	var seqA, seqB uint64
	for i := 0; i < 4; i++ {
		ts := uint64(i) * nsPerSec
		seqA++
		pathA.sendManifest(e, seqA, 206, 2, ts)
		seqB++
		pathB.sendManifest(e, seqB, 4, 2, ts+nsPerSec/4)
		for _, id := range []uint32{1, 2} {
			seqA++
			pathA.sendDef(e, seqA, id, 206, ts+nsPerSec/2)
			seqB++
			pathB.sendDef(e, seqB, id, 4, ts+3*nsPerSec/4)
		}
	}
	e.Flush()

	for _, rule := range []string{"REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP", "REFDATA.SEQ_BUMP_NOT_BY_ONE"} {
		if found := findingsFor(ac, rule); len(found) != 0 {
			t.Errorf("%s: %d finding(s) on two instances that are each conformant (%s); their Manifest Seqs were merged",
				rule, len(found), found[0].Detail)
		}
	}
	for _, p := range []struct {
		src  netip.Addr
		mseq uint16
	}{{srcA, 206}, {srcB, 4}} {
		if ready, mseq := readyAt(e, p.src, incidentChannel); !ready || mseq != p.mseq {
			t.Errorf("instance %v: ready=%v at Manifest Seq %d, want ready at %d", p.src, ready, mseq, p.mseq)
		}
	}
}

// TestRefdataRegressionInsideOneInstanceIsStillReported: the converse. One publisher at
// Manifest Seq 206 goes back to 4 in the same era. The key change must not turn the
// regression rules off: the summary is a regression and the definitions tagged 4 are
// stale.
func TestRefdataRegressionInsideOneInstanceIsStillReported(t *testing.T) {
	path := refdataPath{src: srcA, ch: incidentChannel, era: 3}

	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	seq := path.establish(e, 1, 206, []uint32{1, 2}, 0)
	path.establish(e, seq, 4, []uint32{1, 2}, 10*nsPerSec)
	e.Flush()

	if n := countStatus(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS", core.Violation); n != 2 {
		t.Errorf("REFDATA.SEQ_MONOTONIC_NO_REGRESS: want 2 violations (one per summary at seq 4), got %d", n)
	}
	if n := countStatus(ac, "REFDATA.STALE_SEQ_TAG_AFTER_BUMP", core.Violation); n != 2 {
		t.Errorf("REFDATA.STALE_SEQ_TAG_AFTER_BUMP: want 2 violations (one per definition tagged 4), got %d", n)
	}
}

// TestRefdataStateIsBoundedWithTheTrackers: the refdata state of an instance goes
// with its tracker when the tracker map evicts it. Each publisher replacement can add
// a source address, so without this the state of every replaced instance stays for
// the life of the process.
func TestRefdataStateIsBoundedWithTheTrackers(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)
	// Two summaries from each of more addresses than the cap allows. The second one
	// pushes the first one out of the reorder window, so each address has state.
	for i := 0; i < maxChannelInstances+64; i++ {
		path := refdataPath{src: netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), ch: incidentChannel}
		path.sendManifest(e, 1, 4, 2, uint64(i))
		path.sendManifest(e, 2, 4, 2, uint64(i))
	}
	if e.refdata == nil || len(e.refdata.channels) == 0 {
		t.Fatal("setup: no refdata state was created")
	}
	if got := len(e.refdata.channels); got > maxChannelInstances {
		t.Errorf("refdata state holds %d instances, want at most %d", got, maxChannelInstances)
	}
	for k := range e.refdata.channels {
		if _, ok := e.ports[instanceKey{src: k.src, port: core.PortRefData, ch: k.ch}]; !ok {
			t.Fatalf("refdata state for %v channel %d outlived its tracker", k.src, k.ch)
		}
	}
}

// TestRefdataOneArmShutdownWhileTheOtherServesIsNotAViolation: one arm ends its
// session — EndOfSession on mktdata, then Valid=0 on refdata — and the other arm on
// the same Channel ID keeps sending Valid=1. That is the spec-mandated shutdown of one
// instance. The other arm's Valid=1 must not clear the session-end mark before the
// first arm's verdict is decided.
func TestRefdataOneArmShutdownWhileTheOtherServesIsNotAViolation(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)
	seqs := map[instanceKey]uint64{}
	send := func(src netip.Addr, port core.Port, raw []byte) {
		f, sf := wire.Decode(raw, wire.MagicTOB)
		k := instanceKey{src: src, port: port, ch: f.Header.ChannelID}
		seqs[k]++
		f.Header.Sequence = seqs[k]
		e.Process(src, f, port, sf)
	}
	for _, src := range []netip.Addr{srcA, srcB} {
		send(src, core.PortMktData, buildHeartbeatFrame(wire.MagicTOB, 0, 0))
		send(src, core.PortRefData, buildManifestFrame(wire.MagicTOB, 1, 1, 2))
		send(src, core.PortRefData, buildInstrDefFrameTOB(100, 1))
		send(src, core.PortRefData, buildInstrDefFrameTOB(200, 1))
	}
	// Arm A ends its session. The heartbeat pushes the EndOfSession out of the window.
	send(srcA, core.PortMktData, buildEndOfSessionFrame(wire.MagicTOB))
	send(srcA, core.PortMktData, buildHeartbeatFrame(wire.MagicTOB, 0, 0))
	send(srcA, core.PortRefData, buildManifestFrame(wire.MagicTOB, 0, 1, 2))
	send(srcA, core.PortRefData, buildManifestFrame(wire.MagicTOB, 0, 1, 2))
	// Arm B keeps serving.
	send(srcB, core.PortRefData, buildManifestFrame(wire.MagicTOB, 1, 1, 2))
	send(srcB, core.PortRefData, buildManifestFrame(wire.MagicTOB, 1, 1, 2))
	e.Flush()
	e.EndRun()

	if hasViolation(ac, "REFDATA.VALID_FLAG_WHILE_SERVING") {
		t.Error("REFDATA.VALID_FLAG_WHILE_SERVING: one arm's spec-mandated shutdown was reported because the other arm kept serving")
	}
	if !hasPass(ac, "REFDATA.VALID_FLAG_WHILE_SERVING") {
		t.Error("REFDATA.VALID_FLAG_WHILE_SERVING: no pass for the arm that ended its session")
	}
}
