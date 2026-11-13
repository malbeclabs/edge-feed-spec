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
// which hides the two eras from the frame state. The converse checks are in the
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
func (p *mboPub) ready(e *Engine) {
	manifest := wb.Frame(wire.MagicMBO).Channel(instCh).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).Bytes()
	def := wb.Frame(wire.MagicMBO).Channel(instCh).
		Msg(wire.TypeInstrumentDef, 130, func(b *wb.Body) {
			b.U32(instInstr) // body[0]   Instrument ID
			b.U16(1)         // body[4]   Source ID
			b.Pad(117)       // body[6]   opaque
			b.U8(0)          // body[123] Price Bound: unbounded
			b.U16(1)         // body[124] Manifest Seq
		}).Bytes()
	p.send(e, core.PortRefData, manifest)
	p.send(e, core.PortRefData, def)
	p.send(e, core.PortRefData, manifest)
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
