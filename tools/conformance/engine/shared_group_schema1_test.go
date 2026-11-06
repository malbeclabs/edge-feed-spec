package engine

// shared_group_schema1_test.go — the shape deployed on the Hyperliquid MBO
// groups: two publishers on one group and one port, one Channel ID, a schema-1
// wire format, and **a different Reset Count on each publisher**.
//
// The differing Reset Count is the axis the other instance tests leave at zero.
// It matters because Reset Count is per publisher process and persisted per
// host, so two hosts serving one channel are all but certain to disagree on it.
// A port-keyed engine reads that disagreement as an era change rather than as a
// sequence fault, and an era change is the more destructive of the two: it
// quarantines the datagram without grading it, or it advances the era and
// clears the sequence, send-timestamp and dedup state outright. Neither path
// emits a Must, so the loss counter climbs while the alert that selects on Must
// severity stays quiet.
//
// Both tests therefore assert on silence being absent rather than on a finding
// being present: the failure this pins reports nothing.

import (
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// The two publishers' Reset Counts. Unequal, and far enough apart that neither
// eraRelation direction is the ambiguous one: 3 reads as older against 7, and 7
// as newer against 3, so an era-keyed engine takes both destructive paths.
const (
	eraPubA uint8 = 3
	eraPubB uint8 = 7
)

// mboHB1 is one schema-1 MBO heartbeat from a given instance.
func mboHB1(ch uint8, seq uint64, era uint8, sendTS uint64) *wire.Frame {
	raw := wb.Frame(wire.MagicMBO).
		Schema(1).
		Channel(ch).
		Seq(seq).
		ResetCount(era).
		SendTS(sendTS).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) {
			b.U8(ch)
			b.Pad(11)
		}).
		Bytes()
	f, _ := wire.Decode(raw, wire.MagicMBO)
	return f
}

// instrDefMBOBody1 builds the schema-1 InstrumentDefinition body (80-byte
// message, 76-byte body), per tag top-of-book/v1.0.0 "0x02
// InstrumentDefinition (80 bytes)":
//
//	Body[0:4]   Instrument ID (u32 LE)   ← spec offset 4
//	Body[4:73]  Symbol char[16], legs and the rest, opaque here
//	Body[73]    Price Bound (u8)         ← spec offset 77
//	Body[74:76] Manifest Seq (u16 LE)    ← spec offset 78
//
// Schema 3 puts the last two at body 123 and 124. A decoder reading the
// schema-3 offsets out of this message runs off the end and takes 0 for both,
// which is why the manifest match below is the assertion that catches it.
func instrDefMBOBody1(instrID uint32, manifestSeq uint16) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID)
		b.Pad(69)
		b.U8(0) // Price Bound: 0 is Unbounded, which is what the HL publisher emits
		b.U16(manifestSeq)
	}
}

// TestSharedGroupSchema1KeepsPublishersApart: interleave two publishers'
// heartbeats on one channel and one port, each on its own Reset Count and its
// own unrelated sequence space, and grade nothing against them. Every finding
// here would be fabricated by the merge, not committed by a publisher.
func TestSharedGroupSchema1KeepsPublishersApart(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, cap)

	// A runs from 0, B from 5000, 100 ms apart, alternating on the socket.
	// Unrelated sequence spaces are the point: a merged tracker reads each
	// alternation as a ~5000-frame jump in one direction or the other.
	const base = 10 * nsPerSec
	for i := uint64(0); i < 24; i++ {
		ts := base + i*nsPerSec/10
		eng.Process(srcA, mboHB1(chArmA, i, eraPubA, ts), core.PortMktData, nil)
		eng.Process(srcB, mboHB1(chArmA, 5000+i, eraPubB, ts+nsPerSec/20), core.PortMktData, nil)
	}
	eng.Flush()

	assertNoFabricatedLoss(t, eng, cap, chArmA)
}

// TestSharedGroupSchema1RefdataSurvives: the reference-data half. Two
// publishers serve one channel's instrument set, disagreeing on Reset Count.
// The set must establish.
//
// This pins two fixes, with one assertion each, because the two failures do not
// look alike.
//
// The schema-1 offset is pinned by the manifest match. A definition is admitted
// only when its Manifest Seq equals the manifest's, so reading the schema-3
// offset out of an 80-byte message yields 0, matches nothing, and leaves the set
// empty.
//
// The per-instance era is pinned by the transport-loss count, and ready() cannot
// do it. Merge the two instances and the publisher whose Reset Count reads as
// older is quarantined at intake rather than wiping the set, so the surviving
// publisher still builds a complete set and ready() is still true. Half the
// fleet then goes ungraded with no finding anywhere. The counter is the only
// trace.
func TestSharedGroupSchema1RefdataSurvives(t *testing.T) {
	ac := &captureAll{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, ac)

	const manifestSeq uint16 = 3
	send := func(src netip.Addr, seq uint64, era uint8, sendTS uint64, build func(*wb.Body), typ uint8, msgLen uint8) {
		raw := wb.Frame(wire.MagicMBO).
			Schema(1).
			Channel(chArmA).
			Seq(seq).
			ResetCount(era).
			SendTS(sendTS).
			Msg(typ, msgLen, build).
			Bytes()
		f, sf := wire.Decode(raw, wire.MagicMBO)
		e.Process(src, f, core.PortRefData, sf)
	}

	var seqA, seqB uint64
	// Manifests alternate, as the two publishers pace them independently.
	for i := 0; i < 4; i++ {
		send(srcA, seqA, eraPubA, uint64(i)*nsPerSec, manifestBody(1, manifestSeq, 2), 0x07, 24)
		seqA++
		send(srcB, 5000+seqB, eraPubB, uint64(i)*nsPerSec+nsPerSec/2, manifestBody(1, manifestSeq, 2), 0x07, 24)
		seqB++
	}
	// Definitions arrive later in the cycle, after several alternations.
	for _, instr := range []uint32{1, 2} {
		send(srcA, seqA, eraPubA, 5*nsPerSec, instrDefMBOBody1(instr, manifestSeq), 0x02, 80)
		seqA++
		send(srcB, 5000+seqB, eraPubB, 5*nsPerSec, instrDefMBOBody1(instr, manifestSeq), 0x02, 80)
		seqB++
	}
	// One more manifest from each publisher, then flush. The trailing
	// alternation is what production does — the manifests cycle continuously —
	// and it is what makes this test see the era fix: without it the run ends
	// on a definition and the set survives on ordering alone.
	send(srcA, seqA, eraPubA, 6*nsPerSec, manifestBody(1, manifestSeq, 2), 0x07, 24)
	send(srcB, 5000+seqB, eraPubB, 6*nsPerSec+nsPerSec/2, manifestBody(1, manifestSeq, 2), 0x07, 24)
	e.Flush()

	if ready, defs := readyOn(e, chArmA); !ready {
		t.Errorf("channel %d: ready() is false with %d/2 definitions. Either the other "+
			"publisher's Reset Count erased the set on every alternation, or Manifest Seq "+
			"was read at the schema-3 offset and came back 0", chArmA, defs)
	}

	// The converse: the definitions are the right length for schema 1, so the
	// Must rule that fires when a schema-3 length is assumed must stay silent.
	// Both publishers' refdata must be graded, not just the one whose Reset
	// Count happens to read as newer. Merge the two instances and the other
	// publisher's datagrams are quarantined as older-era stragglers: they never
	// reach the state machine at all, the surviving publisher still builds a
	// complete set, and the only trace is this counter. So the set being ready
	// is not sufficient on its own — this is the assertion that sees the loss.
	if n := len(ac.lostPorts); n != 0 {
		t.Errorf("got %d transport losses on refdata, want 0: one publisher's "+
			"Reset Count is reading as an older era, so its datagrams are quarantined ungraded", n)
	}

	if n := ac.violationsFor("MSG.LENGTH_PER_TYPE"); n != 0 {
		t.Errorf("got %d MSG.LENGTH_PER_TYPE violations, want 0: an 80-byte "+
			"InstrumentDefinition is correct at schema 1 and is being graded against 130", n)
	}
}
