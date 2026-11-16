package engine

// mbo_instance_test.go — two publishers that serve one market-by-order channel.
//
// market-by-order/spec.md, "Redundant Channel Instances": a subscriber that
// consumes more than one instance of a channel MUST key all channel and
// instrument state by (source IP address, Channel ID, …). Each instance mints its
// own Per-Instrument Seq, Snapshot ID, Anchor Seq and Reset Count, and its order
// flow is its own. One book for two instances is the failure the spec names: "two
// independent sequence spaces interleave into one state machine, two instances
// mint the same (channel_id, instrument_id, snapshot_id) for unrelated snapshots,
// and … one publisher restarting bumps only its Reset Count".
//
// malbeclabs/edge-feed-spec#82 measured that failure on a live group with two
// publishers: hundreds of thousands of SNAP.* Must violations that no publisher
// committed.
//
// Each test runs twice: with a different Reset Count on each publisher, which is
// the usual case because the Reset Count is per process, and with the same one,
// which hides the two eras from the datagram tracker state. The converse checks are in the
// same tests: a fault inside one instance still fires, so the fix does not switch
// the rules off.

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

const (
	instCh    uint8  = 1
	instInstr uint32 = 101
)

// instanceEras are the Reset Counts of publishers A and B in each subtest.
var instanceEras = []struct {
	name string
	a, b uint8
}{
	{"reset counts differ", 3, 7},
	{"reset counts equal", 5, 5},
}

// mboPub is one publisher: its source address, its Reset Count and the next
// Sequence Number of each of its ports.
type mboPub struct {
	src netip.Addr
	era uint8
	seq map[core.Port]uint64
}

func newMBOPub(src netip.Addr, era uint8) *mboPub {
	return &mboPub{src: src, era: era, seq: make(map[core.Port]uint64)}
}

// send gives the engine one datagram of this publisher on a port.
func (p *mboPub) send(e *Engine, port core.Port, raw []byte) {
	f, sf := wire.Decode(raw, wire.MagicMBO)
	f.Header.Sequence = p.seq[port]
	f.Header.ResetCount = p.era
	p.seq[port]++
	e.Process(p.src, f, port, sf)
}

// skip makes the publisher's next datagram on a port arrive after a gap of n.
func (p *mboPub) skip(port core.Port, n uint64) { p.seq[port] += n }

// restart starts a new Reset Count era: every port's Sequence Number goes back to 0.
func (p *mboPub) restart(era uint8) {
	p.era = era
	clear(p.seq)
}

// ready sends this publisher's reference data for one instrument, so that the
// rules gated on a ready channel can grade it.
func (p *mboPub) ready(e *Engine) { p.refdataSet(e, 1, instInstr) }

// refdataSet sends this publisher's reference data at one Manifest Seq: a
// ManifestSummary, one InstrumentDefinition for each instrument, and a second
// ManifestSummary that classifies the last definition.
func (p *mboPub) refdataSet(e *Engine, manifestSeq uint16, ids ...uint32) {
	manifest := wb.Frame(wire.MagicMBO).Channel(instCh).
		Msg(wire.TypeManifest, 24, manifestBody(1, manifestSeq, uint32(len(ids)))).Bytes()
	p.send(e, core.PortRefData, manifest)
	for _, id := range ids {
		p.send(e, core.PortRefData, wb.Frame(wire.MagicMBO).Channel(instCh).
			Msg(wire.TypeInstrumentDef, 130, func(b *wb.Body) {
				b.U32(id)          // body[0]   Instrument ID
				b.U16(1)           // body[4]   Source ID
				b.Pad(117)         // body[6]   opaque
				b.U8(0)            // body[123] Price Bound: unbounded
				b.U16(manifestSeq) // body[124] Manifest Seq
			}).Bytes())
	}
	p.send(e, core.PortRefData, manifest)
}

// emptyGroup sends one complete snapshot group of an empty book for an instrument.
func (p *mboPub) emptyGroup(e *Engine, instrID, snapID uint32) {
	p.send(e, core.PortSnapshot, buildSnapBeginFull(instCh, instrID, 0, 0, snapID, 0))
	p.send(e, core.PortSnapshot, buildSnapEndFull(instCh, instrID, 0, snapID))
}

func (p *mboPub) add(e *Engine, perSeq uint32, orderID uint64) {
	p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr, perSeq, orderID, 0, 1, 100, 0))
}

func (p *mboPub) cancel(e *Engine, perSeq uint32, orderID uint64) {
	p.send(e, core.PortMktData, buildOrderCancelFull(instCh, instInstr, perSeq, orderID, 1))
}

// snapshot sends one complete snapshot group of one order.
func (p *mboPub) begin(e *Engine, anchor uint64, snapID, lastInstrSeq uint32) {
	p.send(e, core.PortSnapshot, buildSnapBeginFull(instCh, instInstr, anchor, 1, snapID, lastInstrSeq))
}

func (p *mboPub) order(e *Engine, snapID uint32, orderID uint64) {
	p.send(e, core.PortSnapshot, buildSnapOrderFull(instCh, snapID, orderID, 100))
}

func (p *mboPub) end(e *Engine, anchor uint64, snapID uint32) {
	p.send(e, core.PortSnapshot, buildSnapEndFull(instCh, instInstr, anchor, snapID))
}

// violations returns the Violation findings of the rules whose ID starts with prefix.
func violations(ac *allCapture, prefix string) []core.Finding {
	var out []core.Finding
	for _, f := range ac.findings {
		if f.Status == core.Violation && len(f.RuleID) >= len(prefix) && f.RuleID[:len(prefix)] == prefix {
			out = append(out, f)
		}
	}
	return out
}

func describe(fs []core.Finding) string {
	s := ""
	for _, f := range fs {
		s += fmt.Sprintf("\n  %s: %s", f.RuleID, f.Detail)
	}
	return s
}

// twoReadyPubs returns an engine and publishers A and B, each with its own
// reference data, and each with a trusted book: an empty snapshot at Last
// Instrument Seq 0, then two orders added from Per-Instrument Seq 1. A holds orders 1 and 3. B holds orders 2 and 4.
// The two publishers' datagrams alternate, as they do on one socket.
func twoReadyPubs(t *testing.T, eraA, eraB uint8) (*Engine, *allCapture, *mboPub, *mboPub) {
	t.Helper()
	e, ac := newMBOEngineW1()
	a, b := newMBOPub(srcA, eraA), newMBOPub(srcB, eraB)
	a.ready(e)
	b.ready(e)
	a.send(e, core.PortSnapshot, emptySnapshotGroups(instCh, instInstr))
	b.send(e, core.PortSnapshot, emptySnapshotGroups(instCh, instInstr))
	e.Flush()
	a.add(e, 1, 1)
	b.add(e, 1, 2)
	a.add(e, 2, 3)
	b.add(e, 2, 4)
	return e, ac, a, b
}

// TestMBOInstancesKeepTheirOwnSnapshotGroups: the two publishers' snapshot groups
// interleave on the snapshot port. Each group is complete and well formed on its
// own instance. Both publishers announce the same Order ID, because their order
// flow is their own.
func TestMBOInstancesKeepTheirOwnSnapshotGroups(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac := newMBOEngineW1()
			a, b := newMBOPub(srcA, tc.a), newMBOPub(srcB, tc.b)
			a.begin(e, 0, 10, 0)
			b.begin(e, 0, 400000, 0)
			a.order(e, 10, 999)
			b.order(e, 400000, 999)
			a.end(e, 0, 10)
			b.end(e, 0, 400000)
			e.Flush()
			e.EndRun()

			if v := violations(ac, "SNAP."); len(v) != 0 {
				t.Errorf("got %d SNAP.* violations, want 0: the two instances' snapshot groups are merged into one state%s", len(v), describe(v))
			}
		})
	}
}

// TestMBOInstanceSnapshotFaultStillFires is the converse: a group that one
// publisher leaves open is a violation on that publisher, whatever the other one
// sends in between.
func TestMBOInstanceSnapshotFaultStillFires(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac := newMBOEngineW1()
			a, b := newMBOPub(srcA, tc.a), newMBOPub(srcB, tc.b)
			a.begin(e, 0, 10, 0) // A never ends this group.
			b.begin(e, 0, 400000, 0)
			b.order(e, 400000, 999)
			b.end(e, 0, 400000)
			a.begin(e, 0, 11, 0)
			a.order(e, 11, 999)
			a.end(e, 0, 11)
			e.Flush()

			got := violations(ac, "SNAP.BEGIN_ORDER_END_GROUPING")
			if len(got) != 1 {
				t.Fatalf("got %d SNAP.BEGIN_ORDER_END_GROUPING violations, want 1 for A's open group%s", len(got), describe(ac.findings))
			}
			if got[0].InstrumentID != instInstr {
				t.Errorf("violation names instrument %d, want %d", got[0].InstrumentID, instInstr)
			}
		})
	}
}

// TestMBOInstancesKeepTheirOwnBooks: the two publishers add and cancel their own
// orders, each from Per-Instrument Seq 1, and the datagrams alternate. Nothing
// here is a fault. Then A cancels an order that only B added: on A's book that
// order does not exist, so the cancel is a violation.
func TestMBOInstancesKeepTheirOwnBooks(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac, a, b := twoReadyPubs(t, tc.a, tc.b)
			a.cancel(e, 3, 1)
			b.cancel(e, 3, 2)
			e.Flush()

			for _, prefix := range []string{"DELTA.", "REF.", "BATCH."} {
				if v := violations(ac, prefix); len(v) != 0 {
					t.Errorf("got %d %s* violations, want 0: the two instances' order flow is merged into one book%s", len(v), prefix, describe(v))
				}
			}

			clearFindings(ac)
			a.cancel(e, 4, 4) // order 4 is B's
			e.Flush()
			if got := violations(ac, "REF.CANCEL_DANGLING_ORDER"); len(got) != 1 {
				t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1: A cancelled an order only B added%s", len(got), describe(ac.findings))
			}
		})
	}
}

// TestMBOInstanceRestartKeepsTheOther: B restarts, so its Reset Count changes.
// That is B's own era. A's book is current and stays trusted, so a dangling
// cancel on A is still a violation, and A's next delta is not a first value.
func TestMBOInstanceRestartKeepsTheOther(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac, a, b := twoReadyPubs(t, tc.a, tc.b)
			b.restart(tc.b + 1)
			b.add(e, 1, 50)
			a.add(e, 3, 5)
			clearFindings(ac)
			a.cancel(e, 4, 999) // nobody added order 999
			e.Flush()

			if got := violations(ac, "REF.CANCEL_DANGLING_ORDER"); len(got) != 1 {
				t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1: B's restart must not discard A's book%s", len(got), describe(ac.findings))
			}
			for _, f := range findingsFor(ac, "DELTA.PERINSTR_FIRST_VALUE") {
				t.Errorf("A's delta after B's restart graded as a first value (%s): B's reset wiped A's tracker", f.Detail)
			}
		})
	}
}

// TestMBOInstanceLossGatesOnlyItsOwnRules: B loses mktdata datagrams. The loss
// can explain an anomaly in B's book and not in A's, so a dangling cancel on A is
// still a violation.
func TestMBOInstanceLossGatesOnlyItsOwnRules(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac, a, b := twoReadyPubs(t, tc.a, tc.b)
			b.skip(core.PortMktData, 5)
			b.add(e, 3, 6)
			b.add(e, 4, 7)
			a.add(e, 3, 5)
			clearFindings(ac)
			a.cancel(e, 4, 999) // nobody added order 999
			e.Flush()

			if got := violations(ac, "REF.CANCEL_DANGLING_ORDER"); len(got) != 1 {
				t.Errorf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1: loss on B gated a rule on A%s", len(got), describe(ac.findings))
			}
		})
	}
}

// TestMBOInstanceSnapshotLossGatesOnlyItsOwnRules: B loses a snapshot datagram.
// That loss cannot explain a SnapshotEnd that A sends with no SnapshotBegin, so the
// grouping fault on A is still a violation.
func TestMBOInstanceSnapshotLossGatesOnlyItsOwnRules(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac := newMBOEngineW1()
			a, b := newMBOPub(srcA, tc.a), newMBOPub(srcB, tc.b)
			b.emptyGroup(e, instInstr, 400000)
			b.skip(core.PortSnapshot, 5)
			b.emptyGroup(e, instInstr, 400001)
			a.end(e, 0, 10) // no SnapshotBegin on A
			e.Flush()

			if got := violations(ac, "SNAP.BEGIN_ORDER_END_GROUPING"); len(got) != 1 {
				t.Errorf("got %d SNAP.BEGIN_ORDER_END_GROUPING violations, want 1: loss on B gated a snapshot rule on A%s", len(got), describe(ac.findings))
			}
		})
	}
}

// roundRobin returns the SNAP.ROUND_ROBIN_COVERS_MANIFEST findings by status, and
// the unverifiable ones by reason.
func roundRobin(ac *allCapture) (pass, viol int, unver map[string]int) {
	unver = make(map[string]int)
	for _, f := range findingsFor(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		switch f.Status {
		case core.Pass:
			pass++
		case core.Violation:
			viol++
		case core.Unverifiable:
			unver[f.Reason]++
		}
	}
	return pass, viol, unver
}

// TestMBORoundRobinJudgesEachInstance: A and B each declare instruments 101 and
// 102. A snapshots both twice. B snapshots only 101, twice. B's round robin misses
// 102, and A's does not: one violation and three passes.
func TestMBORoundRobinJudgesEachInstance(t *testing.T) {
	e, ac := newMBOEngineW1()
	a, b := newMBOPub(srcA, 3), newMBOPub(srcB, 7)
	a.refdataSet(e, 1, instInstr, instInstr+1)
	b.refdataSet(e, 1, instInstr, instInstr+1)
	for cycle := uint32(0); cycle < 2; cycle++ {
		a.emptyGroup(e, instInstr, 10+2*cycle)
		a.emptyGroup(e, instInstr+1, 11+2*cycle)
		b.emptyGroup(e, instInstr, 400000+cycle)
	}
	e.Flush()
	e.EndRun()

	if pass, viol, unver := roundRobin(ac); pass != 3 || viol != 1 || len(unver) != 0 {
		t.Errorf("got %d passes, %d violations and unverifiable %v; want 3, 1 and none: each publisher runs its own round robin%s",
			pass, viol, unver, describe(ac.findings))
	}
}

// TestMBORoundRobinCoversARefdataOnlyInstance: A snapshots its instruments. C sends
// reference data and nothing else. C ran no snapshot cycle, so each of its
// instruments is reported insufficient_window, not left out.
func TestMBORoundRobinCoversARefdataOnlyInstance(t *testing.T) {
	srcC := netip.MustParseAddr("10.0.0.3")
	e, ac := newMBOEngineW1()
	a, c := newMBOPub(srcA, 3), newMBOPub(srcC, 9)
	a.refdataSet(e, 1, instInstr, instInstr+1)
	c.refdataSet(e, 1, instInstr, instInstr+1)
	for cycle := uint32(0); cycle < 2; cycle++ {
		a.emptyGroup(e, instInstr, 10+2*cycle)
		a.emptyGroup(e, instInstr+1, 11+2*cycle)
	}
	e.Flush()
	e.EndRun()

	if pass, viol, unver := roundRobin(ac); pass != 2 || viol != 0 || unver[core.ReasonInsufficientWindow] != 2 {
		t.Errorf("got %d passes, %d violations and unverifiable %v; want 2, 0 and 2 insufficient_window%s",
			pass, viol, unver, describe(ac.findings))
	}
}

// TestMBOFallbackStateFollowsTheSetItReads: A sends mktdata and no reference data,
// so it reads B's set through forChannel. C sends both. B drops instrument 102 from
// its set. A's tracker of 102 goes with it; C's does not, because C reads its own
// set, which still holds 102.
func TestMBOFallbackStateFollowsTheSetItReads(t *testing.T) {
	srcC := netip.MustParseAddr("10.0.0.3")
	e, _ := newMBOEngineW1()
	a, b, c := newMBOPub(srcA, 3), newMBOPub(srcB, 7), newMBOPub(srcC, 9)
	b.refdataSet(e, 1, instInstr, instInstr+1)
	c.refdataSet(e, 1, instInstr, instInstr+1)
	for _, p := range []*mboPub{a, c} {
		p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr+1, 1, 1, 0, 1, 100, 0))
		p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr, 1, 2, 0, 1, 100, 0))
	}
	e.Flush()
	b.refdataSet(e, 2, instInstr)
	e.Flush()

	gone := instrTrackerKey{instCh, instInstr + 1}
	if _, ok := e.mbos[refdataKey{src: srcA, ch: instCh}].trackers[gone]; ok {
		t.Error("A reads B's set, and B dropped instrument 102, but A still tracks it")
	}
	if _, ok := e.mbos[refdataKey{src: srcC, ch: instCh}].trackers[gone]; !ok {
		t.Error("C reads its own set, which still holds instrument 102, but B's change dropped it")
	}
}

// TestMBOStateIsBoundedWithTheTrackers: the MBO state of an instance goes with the
// last of its trackers when the tracker map evicts them. Its open snapshot group and
// its awaited recovery snapshot are reported Unverifiable, truncated, and are not
// lost. Eviction is not end of run, so neither is a violation.
func TestMBOStateIsBoundedWithTheTrackers(t *testing.T) {
	e, ac := newMBOEngineW1()
	for i := 0; i < maxChannelInstances+64; i++ {
		p := newMBOPub(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 1)
		// The second datagram on each port classifies the first.
		p.send(e, core.PortMktData, buildInstrumentResetFrame(instCh, instInstr, 0))
		p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr+1, 1, 1, 0, 1, 100, 0))
		p.begin(e, 0, 1, 0)
		p.order(e, 1, 11)
	}
	for k := range e.mbos {
		owned := false
		for _, port := range []core.Port{core.PortMktData, core.PortRefData, core.PortSnapshot} {
			if _, ok := e.ports[instanceKey{src: k.src, port: port, ch: k.ch}]; ok {
				owned = true
			}
		}
		if !owned {
			t.Fatalf("MBO state for %v channel %d outlived its trackers", k.src, k.ch)
		}
	}
	for _, rule := range []string{"SNAP.BEGIN_ORDER_END_GROUPING", "RESET.SNAPSHOT_FOLLOWS"} {
		truncated := 0
		for _, f := range findingsFor(ac, rule) {
			if f.Status == core.Violation {
				t.Fatalf("%s: eviction produced a violation: %s", rule, f.Detail)
			}
			if f.Status == core.Unverifiable && f.Reason == core.ReasonTruncated {
				truncated++
			}
		}
		if truncated == 0 {
			t.Errorf("%s: no Unverifiable truncated finding, so the evicted instances' opportunities were lost", rule)
		}
	}
}

// TestMBOFallbackEqualSetKeepsTheOpenGroup: A sends no reference data and reads B's
// set. A's state is created after B's set froze, so that set is its baseline. A
// opens a snapshot group, and C then freezes an equal set. That is not a change to
// the set A reads, so A's group stays open and closes without a violation.
func TestMBOFallbackEqualSetKeepsTheOpenGroup(t *testing.T) {
	srcC := netip.MustParseAddr("10.0.0.3")
	e, ac := newMBOEngineW1()
	a, b, c := newMBOPub(srcA, 3), newMBOPub(srcB, 7), newMBOPub(srcC, 9)
	b.refdataSet(e, 1, instInstr)
	e.Flush()
	a.begin(e, 0, 10, 0)
	a.order(e, 10, 11) // classifies A's SnapshotBegin: the group is open
	c.refdataSet(e, 1, instInstr)
	a.end(e, 0, 10)
	e.Flush()

	if got := violations(ac, "SNAP."); len(got) != 0 {
		t.Errorf("got %d SNAP.* violations, want 0: an equal set discarded A's open group%s", len(got), describe(got))
	}
}

// TestMBORoundRobinCountsASplitPublisherOnce: one publisher sends its MBO datagrams
// from M and its reference data from R. M reads R's set and is judged on it. R is
// the refdata half of the same publisher, so it is not judged again.
func TestMBORoundRobinCountsASplitPublisherOnce(t *testing.T) {
	e, ac := newMBOEngineW1()
	m, r := newMBOPub(srcA, 3), newMBOPub(srcB, 3)
	r.refdataSet(e, 1, instInstr, instInstr+1)
	for cycle := uint32(0); cycle < 2; cycle++ {
		m.emptyGroup(e, instInstr, 10+2*cycle)
		m.emptyGroup(e, instInstr+1, 11+2*cycle)
	}
	e.Flush()
	e.EndRun()

	if pass, viol, unver := roundRobin(ac); pass != 2 || viol != 0 || len(unver) != 0 {
		t.Errorf("got %d passes, %d violations and unverifiable %v; want 2, 0 and none: one publisher's instruments counted twice%s",
			pass, viol, unver, describe(ac.findings))
	}
}
