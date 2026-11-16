package engine

// mbo_attribution_test.go — market-by-order findings name the publisher.
//
// The market-by-order state is keyed on (source IP address, Channel ID): one
// publisher of the channel, across its port roles. A rule that reads it judges that
// publisher, and the catalog lists it as StateMBOInstance. These
// tests pin the address on each of the three paths that emit such a finding: in
// front of a datagram, at end of run, and at eviction. Each also checks the
// invariant over every finding of the run: a finding carries an address if and
// only if the catalog says that its rule judges one instance.

import (
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// assertAttributionFollowsCatalog checks every finding against its catalog entry.
func assertAttributionFollowsCatalog(t *testing.T, findings []core.Finding) {
	t.Helper()
	for _, f := range findings {
		meta, ok := core.Lookup(f.RuleID)
		if !ok {
			t.Errorf("finding for unknown rule %q", f.RuleID)
			continue
		}
		if got, want := f.SourceAddr.IsValid(), meta.State.InstanceScoped(); got != want {
			t.Errorf("%s (state kind %v): carries an address = %v, want %v", f.RuleID, meta.State, got, want)
		}
	}
}

// TestMBODatagramFindingNamesItsPublisher: A cancels an order that only B added.
// The dangling cancel is A's fault, so the violation names A and never B.
func TestMBODatagramFindingNamesItsPublisher(t *testing.T) {
	for _, tc := range instanceEras {
		t.Run(tc.name, func(t *testing.T) {
			e, ac, a, _ := twoReadyPubs(t, tc.a, tc.b)
			a.cancel(e, 3, 4) // order 4 is B's
			e.Flush()
			got := violations(ac, "REF.CANCEL_DANGLING_ORDER")
			if len(got) != 1 {
				t.Fatalf("got %d REF.CANCEL_DANGLING_ORDER violations, want 1%s", len(got), describe(ac.findings))
			}
			if got[0].SourceAddr != srcA {
				t.Errorf("violation names %v, want %v: A sent the cancel", got[0].SourceAddr, srcA)
			}
			assertAttributionFollowsCatalog(t, ac.findings)
		})
	}
}

// TestMBOEndOfRunFindingNamesItsInstance: A covers both instruments in its round
// robin and B covers one. The violation at end of run names B, and the passes name
// the instance that earned each one.
func TestMBOEndOfRunFindingNamesItsInstance(t *testing.T) {
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

	byAddr := map[netip.Addr]map[core.Status]int{}
	for _, f := range findingsFor(ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST") {
		if byAddr[f.SourceAddr] == nil {
			byAddr[f.SourceAddr] = map[core.Status]int{}
		}
		byAddr[f.SourceAddr][f.Status]++
	}
	if byAddr[srcA][core.Pass] != 2 || byAddr[srcA][core.Violation] != 0 ||
		byAddr[srcB][core.Pass] != 1 || byAddr[srcB][core.Violation] != 1 {
		t.Errorf("round robin by address = %v; want A: 2 passes, B: 1 pass and 1 violation", byAddr)
	}
	assertAttributionFollowsCatalog(t, ac.findings)
}

// TestMBOEvictionFindingNamesTheEvictedInstance: the verdicts that eviction
// decides name the instance that the tracker map evicted.
func TestMBOEvictionFindingNamesTheEvictedInstance(t *testing.T) {
	e, ac := newMBOEngineW1()
	for i := 0; i < maxChannelInstances+64; i++ {
		p := newMBOPub(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 1)
		p.send(e, core.PortMktData, buildInstrumentResetFrame(instCh, instInstr, 0))
		p.send(e, core.PortMktData, buildOrderAddFull(instCh, instInstr+1, 1, 1, 0, 1, 100, 0))
		p.begin(e, 0, 1, 0)
		p.order(e, 1, 11)
	}
	truncated := 0
	for _, f := range ac.findings {
		if f.Reason != core.ReasonTruncated {
			continue
		}
		truncated++
		if !f.SourceAddr.IsValid() {
			t.Fatalf("%s at eviction names no publisher", f.RuleID)
		}
		if _, ok := e.mbos[refdataKey{src: f.SourceAddr, ch: instCh}]; ok {
			t.Fatalf("%s at eviction names %v, which was not evicted", f.RuleID, f.SourceAddr)
		}
	}
	if truncated == 0 {
		t.Fatal("eviction decided no verdict, so the attribution is untested")
	}
	assertAttributionFollowsCatalog(t, ac.findings)
}

// TestMBOVerdictsCarryTheirOwnEvidence (from review): a finding that names a
// publisher is placed in the rule report with its sequence range in its era. A
// verdict must carry the Sequence Number of the datagram it is about, and a verdict
// that no single datagram decided must carry no evidence, never Sequence 0 in a
// live era.
func TestMBOVerdictsCarryTheirOwnEvidence(t *testing.T) {
	one := func(t *testing.T, ac *allCapture, rule string) core.Finding {
		t.Helper()
		got := findingsFor(ac, rule)
		if len(got) != 1 {
			t.Fatalf("got %d %s findings, want 1%s", len(got), rule, describe(ac.findings))
		}
		if got[0].SourceAddr != srcA {
			t.Fatalf("%s names %v, want %v", rule, got[0].SourceAddr, srcA)
		}
		return got[0]
	}

	t.Run("batch atomicity: the BatchBoundary datagram", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		e.Flush()
		a.add(e, 1, 7)
		e.Flush()
		// An orphan no delta can make: a live order with nothing remaining.
		bk := e.mbos[refdataKey{src: srcA, ch: instCh}].book.book(instCh, instInstr)
		lo := bk.live[7]
		lo.remaining = 0
		bk.live[7] = lo
		clearFindings(ac)
		seq := a.seq[core.PortMktData]
		a.send(e, core.PortMktData, buildBatchBoundaryFrame(instCh, 1))
		e.Flush()
		if f := one(t, ac, "BATCH.ATOMICITY_CONSISTENCY"); f.Seq != seq || !f.ResetCountKnown {
			t.Errorf("evidence seq %d (era known %v), want %d", f.Seq, f.ResetCountKnown, seq)
		}
	})

	t.Run("open group at end of run: its last datagram", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		a.begin(e, 0, 2, 0)
		last := a.seq[core.PortSnapshot]
		a.order(e, 2, 11) // the group never ends
		e.Flush()
		clearFindings(ac)
		e.EndRun()
		if f := one(t, ac, "SNAP.BEGIN_ORDER_END_GROUPING"); f.Seq != last || !f.ResetCountKnown {
			t.Errorf("evidence seq %d (era known %v), want %d", f.Seq, f.ResetCountKnown, last)
		}
	})

	t.Run("awaited recovery at end of run: the InstrumentReset datagram", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		a.add(e, 1, 7)
		reset := a.seq[core.PortMktData]
		a.send(e, core.PortMktData, buildInstrumentResetFrame(instCh, instInstr, reset))
		e.Flush()
		clearFindings(ac)
		e.EndRun()
		if f := one(t, ac, "RESET.SNAPSHOT_FOLLOWS"); f.Seq != reset || !f.ResetCountKnown {
			t.Errorf("evidence seq %d (era known %v), want %d", f.Seq, f.ResetCountKnown, reset)
		}
	})

	t.Run("round robin: no evidence", func(t *testing.T) {
		e, ac, a := readyPub(t)
		a.group(e, 0, 1, 0)
		a.group(e, 0, 2, 0)
		e.Flush()
		clearFindings(ac)
		e.EndRun()
		if f := one(t, ac, "SNAP.ROUND_ROBIN_COVERS_MANIFEST"); f.ResetCountKnown {
			t.Errorf("round robin carries era %d and seq %d, want no evidence", f.ResetCount, f.Seq)
		}
	})
}
