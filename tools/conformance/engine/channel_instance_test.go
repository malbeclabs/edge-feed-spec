package engine

// channel_instance_test.go — more than one publisher serving one channel.
//
// Sequencing keys on the channel instance — `(source address, Channel ID,
// destination port)` — and never on the channel (GLOSSARY.md, and "Redundant
// Channel Instances" in market-by-price/spec.md). Two publishers may serve one
// channel on one group and one port, each advancing its own sequence space and
// its own Reset Count, and they are told apart by transport: the source address
// is the discriminator, `Channel ID` names the instrument set and may be equal
// on both.
//
// Keying the frame state any less finely merges the two series, and the merge is
// loud in one direction and silent in the other: nearly every alternation reads
// as a forward gap (transport loss, which latches dirtyWindow — cleared only on
// a publisher reset, so every gated detector reports Unverifiable for the
// process lifetime) or as backward motion (FRAME.SEQ_RESET_GAP, a Must), while a
// total heartbeat outage on one publisher is covered by the other's heartbeats
// and never fires HEARTBEAT.CADENCE.
//
// The tests are each other's converses: that the merge is gone, that the
// detectors were not merely switched off, and that a source address appearing
// for the first time opens a series rather than breaking one.

import (
	"net/netip"
	"sort"
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// Two publisher addresses serving one channel, and the channel ids of the two
// arms of one Kalshi stream as deployed today. Both axes are exercised because
// only the source address is guaranteed to differ: the topology design puts the
// arm on source IP and the instrument set on Channel ID, so a stream whose arms
// carry distinct ids today may carry one id tomorrow.
var (
	srcA = netip.MustParseAddr("10.0.0.1")
	srcB = netip.MustParseAddr("10.0.0.2")
)

const (
	chArmA uint8 = 1
	chArmB uint8 = 101
)

// hbFrame is one heartbeat on the wire: the instance that sent it, that
// instance's own sequence number and Reset Count, and the SendTS it carries.
type hbFrame struct {
	src    netip.Addr
	ch     uint8
	seq    uint64
	era    uint8
	sendTS uint64
}

// replayInterleaved feeds the datagrams to the mktdata port in SendTS order —
// the order two publishers' datagrams reach one socket — and flushes.
// ReorderWindow is 1, so each datagram classifies the previous one of its own
// series.
func replayInterleaved(frames []hbFrame, cfg Config) (*Engine, *captureAll) {
	cap := &captureAll{}
	cfg.ReorderWindow = 1
	eng := New(cfg, cap)
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].sendTS < frames[j].sendTS })
	for _, x := range frames {
		eng.Process(x.src, makeHB(wire.MagicTOB, x.ch, x.seq, x.era, x.sendTS), core.PortMktData, nil)
	}
	eng.Flush()
	return eng, cap
}

// cadenceOn returns the HEARTBEAT.CADENCE findings attributed to one channel.
func cadenceOn(cap *captureAll, ch uint8) []core.Finding {
	var out []core.Finding
	for _, f := range cap.findingsFor("HEARTBEAT.CADENCE") {
		if f.ChannelID == ch {
			out = append(out, f)
		}
	}
	return out
}

// assertNoFabricatedLoss checks the four ways a merged series invents a fault the
// publishers did not commit. The gate is last and is the one that is silent: a
// fabricated gap latches it, it is cleared only on a publisher reset, and every
// rule gated on it then reports Unverifiable for the process lifetime while
// checks_total{result="pass"} keeps advancing.
func assertNoFabricatedLoss(t *testing.T, eng *Engine, cap *captureAll, ch uint8) {
	t.Helper()
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("got %d FRAME.SEQ_RESET_GAP violations, want 0: two independent sequence series are being merged, so each alternation reads as backward motion", n)
	}
	if n := cap.violationsFor("FRAME.SEQ_DUP_DIVERGENT"); n != 0 {
		t.Errorf("got %d FRAME.SEQ_DUP_DIVERGENT violations, want 0: the two series' sequence numbers are colliding in one tracker", n)
	}
	if n := len(cap.lostPorts); n != 0 {
		t.Errorf("got %d transport losses, want 0: a merged series reads the other publisher's next datagram as a gap", n)
	}
	if !eng.gateDetector(ch) {
		t.Errorf("the channel-level gate on channel %d is latched: every rule gated on it grades nothing for the rest of the run", ch)
	}
}

// twoStreams interleaves two heartbeat streams whose sequence spaces are
// unrelated: 100 ms apart each, `bSeq` far from `aSeq`, and A silent for 3.05 s
// in the middle while B publishes right through it.
func twoStreams(a, b hbFrame, aSeq, bSeq uint64) []hbFrame {
	const base = 10 * nsPerSec
	frames := []hbFrame{
		{a.src, a.ch, aSeq, a.era, base},
		{a.src, a.ch, aSeq + 1, a.era, base + 305*nsPerSec/100},
		{a.src, a.ch, aSeq + 2, a.era, base + 315*nsPerSec/100},
	}
	for i := uint64(0); i < 36; i++ {
		frames = append(frames, hbFrame{b.src, b.ch, bSeq + i, b.era, base + nsPerSec/20 + i*nsPerSec/10})
	}
	return frames
}

// TestTwoInstancesOnOneChannelKeepSeparateSeries: two publishers on one port
// **and one Channel ID**, told apart only by source address. This is the case
// `Channel ID` cannot separate, and the one the topology design settles on.
func TestTwoInstancesOnOneChannelKeepSeparateSeries(t *testing.T) {
	frames := twoStreams(hbFrame{src: srcA, ch: chArmA}, hbFrame{src: srcB, ch: chArmA}, 0, 5000)
	eng, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	assertNoFabricatedLoss(t, eng, cap, chArmA)

	// The gated detector must grade. Publisher A was silent for 3.05 s against a
	// 1 s expectation; a latched dirtyWindow would report that Unverifiable, and a
	// heartbeat baseline shared with B would not report it at all.
	hb := cadenceOn(cap, chArmA)
	if len(hb) != 1 {
		t.Fatalf("got %d HEARTBEAT.CADENCE findings, want 1: with one heartbeat baseline per port, publisher B's heartbeats mask publisher A's outage", len(hb))
	}
	if hb[0].Status != core.Violation {
		t.Errorf("publisher A's heartbeat outage graded %v, want violation: a gap in the merged series latched the gate", hb[0].Status)
	}
}

// TestTwoChannelsOnOnePortKeepSeparateSeries: the other axis — one publisher
// address, two Channel IDs on one port, which is how the Kalshi arms are
// deployed today and how a sharded instrument set arrives.
func TestTwoChannelsOnOnePortKeepSeparateSeries(t *testing.T) {
	frames := twoStreams(hbFrame{src: srcA, ch: chArmA}, hbFrame{src: srcA, ch: chArmB}, 0, 5000)
	eng, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	assertNoFabricatedLoss(t, eng, cap, chArmA)
	assertNoFabricatedLoss(t, eng, cap, chArmB)

	if hb := cadenceOn(cap, chArmA); len(hb) != 1 || hb[0].Status != core.Violation {
		t.Errorf("channel %d cadence findings %v, want one violation for its 3.05 s outage", chArmA, hb)
	}
	if n := len(cadenceOn(cap, chArmB)); n != 0 {
		t.Errorf("got %d HEARTBEAT.CADENCE findings on channel %d, want 0: it heartbeated every 100 ms", n, chArmB)
	}
}

// TestGapWithinOneInstanceStillDetected is the converse: keying per instance must
// not stop the detectors, and the damage must stay on the instance that took it.
// Publisher A loses datagrams (a forward gap) and receives a late one (backward
// motion); publisher B shares its channel, is clean throughout, and its own
// heartbeat outage must still grade.
func TestGapWithinOneInstanceStillDetected(t *testing.T) {
	const base = 10 * nsPerSec
	frames := []hbFrame{
		{srcA, chArmA, 10, 0, base},
		{srcB, chArmA, 5000, 0, base + nsPerSec/20},
		{srcA, chArmA, 20, 0, base + nsPerSec/10}, // forward gap of 10 within A
		{srcB, chArmA, 5001, 0, base + 15*nsPerSec/100},
		{srcA, chArmA, 5, 0, base + nsPerSec/5}, // late datagram: backward motion within A
		{srcA, chArmA, 21, 0, base + 2*nsPerSec},
		{srcB, chArmA, 5002, 0, base + 25*nsPerSec/10}, // 2.35 s after B's previous heartbeat
		{srcB, chArmA, 5003, 0, base + 26*nsPerSec/10},
	}

	_, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	rg := cap.findingsFor("FRAME.SEQ_RESET_GAP")
	if len(rg) != 1 {
		t.Fatalf("got %d FRAME.SEQ_RESET_GAP findings, want 1 for the late datagram within publisher A's series", len(rg))
	}
	if len(cap.lostPorts) == 0 {
		t.Error("no transport loss recorded for the forward gap within publisher A's series")
	}

	// Both findings are on one channel, so they are told apart by status: the
	// heartbeat gate is per instance, because the baseline it compares against is.
	var graded, gated int
	for _, f := range cadenceOn(cap, chArmA) {
		switch f.Status {
		case core.Violation:
			graded++
		case core.Unverifiable:
			gated++
		}
	}
	if graded != 1 {
		t.Errorf("got %d graded cadence findings, want 1: publisher B is gapless, so publisher A's loss must not gate it", graded)
	}
	if gated != 1 {
		t.Errorf("got %d gated cadence findings, want 1: publisher A's own gap must downgrade its own outage", gated)
	}
}

// TestNewSourceOpensFreshSeries: a publisher's address is a tunnel lease that
// gets reassigned under a live host, so an address that has not been seen before
// must open a new series and stay silent about it — never a gap, never backward
// motion, never a latched window, and never a datagram discarded.
//
// The new publisher starts its own Reset Count at 0 while the old series was at
// 3, which is what a freshly started process does. A tracker that cannot tell
// the two apart reads every datagram of the new one as an older-era straggler,
// quarantines it, and counts it as transport loss — the feed goes dark and the
// loss counter climbs, on nothing but an address change.
func TestNewSourceOpensFreshSeries(t *testing.T) {
	const base = 10 * nsPerSec
	frames := []hbFrame{
		{srcA, chArmA, 5000, 3, base},
		{srcA, chArmA, 5001, 3, base + nsPerSec/10},
		{srcA, chArmA, 5002, 3, base + 2*nsPerSec/10},
		// Same channel, same port, new address: its own sequence space and era.
		{srcB, chArmA, 0, 0, base + 3*nsPerSec/10},
		{srcB, chArmA, 1, 0, base + 4*nsPerSec/10},
		// A 3.1 s heartbeat gap on the new series, so the assertion below is not
		// vacuous: the fresh instance must be immediately verifiable.
		{srcB, chArmA, 2, 0, base + 35*nsPerSec/10},
		{srcB, chArmA, 3, 0, base + 36*nsPerSec/10},
	}

	eng, cap := replayInterleaved(frames, Config{Feed: core.FeedTOB, ExpectHeartbeat: time.Second})

	assertNoFabricatedLoss(t, eng, cap, chArmA)
	if n := cap.violationsFor("FRAME.SEND_TS_MONOTONIC"); n != 0 {
		t.Errorf("got %d FRAME.SEND_TS_MONOTONIC violations, want 0 across an address change", n)
	}
	hb := cadenceOn(cap, chArmA)
	if len(hb) != 1 || hb[0].Status != core.Violation {
		t.Errorf("cadence findings %v, want one violation: a new instance is verifiable from its first datagram", hb)
	}
}

// TestInstanceMapIsBounded: the source address is part of the key and nothing
// authorises it — an any-source join accepts datagrams from any sender — so the
// tracker map must not grow with what arrives on the wire.
func TestInstanceMapIsBounded(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, cap)
	// One datagram from each of more addresses than the cap allows.
	for i := 0; i < maxChannelInstances+64; i++ {
		src := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		eng.Process(src, makeHB(wire.MagicTOB, chArmA, 0, 0, uint64(i)), core.PortMktData, nil)
	}
	if got := len(eng.ports); got > maxChannelInstances {
		t.Errorf("tracker map holds %d instances, want at most %d", got, maxChannelInstances)
	}
}

// The tests below are the other half of keying frame state per instance: the
// taint has to be written where its readers look. Every reader of dirtyWindow
// (dirtyOn, and gateDetector/gateDetectorSnap through it) ORs across the
// channel's instances, so a taint written on one instance that no reader
// associates with the channel is a taint that reaches nobody — and the rules that
// consult it then grade transport reality as publisher misconduct, on Must rules.
// Neither case was reachable while one tracker per port meant the taint and every
// reader of it shared a single flag.

// TestCorruptTaintReachesRealChannel: a datagram shorter than the frame header
// decodes to an all-zero header (wire/decode.go), so it keys the instance ch=0 —
// a channel that need not exist on the wire. The transport taint must reach the
// channel whose snapshot series actually took the corruption, because
// SNAP.TOTAL_ORDERS_COUNT_MATCH and MBP.SNAP.GROUP_STRUCTURE, both Must, read it
// to tell a truncated group from a publisher that miscounted its own orders.
func TestCorruptTaintReachesRealChannel(t *testing.T) {
	const ch uint8 = 5
	eng := New(Config{Feed: core.FeedMBP, ReorderWindow: 1}, &captureAll{})
	eng.Process(srcA, makeHB(wire.MagicMBP, ch, 0, 0, 1000), core.PortSnapshot, nil)
	runt, sf := wire.Decode([]byte{1, 2, 3}, wire.MagicMBP) // header does not decode
	if runt.Header.ChannelID == ch {
		t.Fatalf("premise broken: a runt datagram decoded channel %d, so it no longer keys a phantom instance", runt.Header.ChannelID)
	}
	eng.Process(srcA, runt, core.PortSnapshot, sf)
	eng.Flush()

	if !eng.dirtyOn(core.PortSnapshot, ch) {
		t.Errorf("channel %d's snapshot series is clean after corruption on it: the taint landed on the phantom instance ch=%d that the runt header decoded, and no reader of dirtyOn looks there", ch, runt.Header.ChannelID)
	}
	if eng.gateDetectorSnap(ch) {
		t.Errorf("gateDetectorSnap(%d) is open after corruption on the channel's snapshot series: a truncated snapshot group grades as a publisher Violation", ch)
	}
}

// TestSnapTaintIsChannelWide: the Reset Count is channel-wide, so when another
// port observes the reset first the taint that keeps the wipe's own consequences
// off the publisher's record has to reach the channel's snapshot series wherever
// it is keyed. An exact-source lookup misses it in the case this keying exists
// for: the mktdata series moves to a new address (a reassigned tunnel lease) while
// the lower-rate snapshot series is still keyed under the old one.
func TestSnapTaintIsChannelWide(t *testing.T) {
	eng := New(Config{Feed: core.FeedMBP, ReorderWindow: 1}, &captureAll{})
	eng.Process(srcB, makeHB(wire.MagicMBP, chArmA, 0, 0, 1000), core.PortSnapshot, nil)
	eng.Process(srcA, makeHB(wire.MagicMBP, chArmA, 0, 0, 2000), core.PortMktData, nil)
	eng.Process(srcA, makeHB(wire.MagicMBP, chArmA, 0, 1, 3000), core.PortMktData, nil) // reset
	eng.Flush()

	if !eng.dirtyOn(core.PortSnapshot, chArmA) {
		t.Errorf("the reset on channel %d tainted no snapshot instance: its snapshot series is keyed under %v and the mktdata series under %v, so an exact-source lookup finds nothing and no reader learns of the reset", chArmA, srcB, srcA)
	}
}
