package engine

// late_datagram_test.go — a backward datagram that a gap counted as lost, and
// that the publisher sent before the last one, is named as reordered, not as a
// reset.
//
// The reorder window holds a number of datagrams, not a time. A datagram delayed
// by more than the window holds is counted as lost by the gap before the
// datagrams that passed it, and when it arrives its Sequence Number is below the
// last one. The publisher did not move its series backward: the datagrams were
// reordered. FRAME.SEQ_RESET_GAP still reports it, since this recorder received
// the series out of order, and its detail says which of the two it saw.

import (
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// processSeqs sends one heartbeat per Sequence Number on the mktdata port, with
// a Send Timestamp that grows with the Sequence Number, as one publisher sends.
func processSeqs(eng *Engine, era uint8, seqs ...uint64) {
	for _, s := range seqs {
		eng.Process(srcA, makeHB(wire.MagicTOB, 1, s, era, 1000+s), core.PortMktData, nil)
	}
}

// processInSendOrder sends one heartbeat per Sequence Number on the mktdata port,
// with a Send Timestamp that grows in the order given, as two publishers merged
// into one series send.
func processInSendOrder(eng *Engine, seqs ...uint64) {
	for i, s := range seqs {
		eng.Process(srcA, makeHB(wire.MagicTOB, 1, s, 0, uint64(1000+i)), core.PortMktData, nil)
	}
}

// seqResetGaps returns the FRAME.SEQ_RESET_GAP findings, and fails the test if
// one of them is not a violation.
func seqResetGaps(t *testing.T, c *captureAll) []core.Finding {
	t.Helper()
	got := c.findingsFor("FRAME.SEQ_RESET_GAP")
	for _, f := range got {
		if f.Status != core.Violation {
			t.Errorf("FRAME.SEQ_RESET_GAP %v, want a violation: %s", f.Status, f.Detail)
		}
	}
	return got
}

func isReordered(f core.Finding) bool { return strings.Contains(f.Detail, "reordered") }

// TestLateDatagramIsNamedReordered: seq 3 arrives after 4, 5 and 6, so the gap
// before 4 counted it as lost. It is a violation named as reordered.
func TestLateDatagramIsNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 6)
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || !isReordered(got[0]) {
		t.Errorf("got %+v, want one violation named as reordered", got)
	}
}

// TestBackwardDatagramOutsideAGapIsNotNamedReordered: seq 3 was never counted as
// lost, so it is backward motion.
func TestBackwardDatagramOutsideAGapIsNotNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 4, 5, 7, 8, 9) // the gap before 7 counts 6 only
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || isReordered(got[0]) {
		t.Errorf("got %+v, want one violation not named as reordered", got)
	}
}

// TestLostRangesEndWithTheEra: a gap of era 0 counted seq 3 as lost. Era 1 starts
// at 10 with no gap, so seq 3 below the last one in era 1 is backward motion: the
// old era's gaps do not explain it.
func TestLostRangesEndWithTheEra(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 6) // era 0: the gap before 4 counts 3
	processSeqs(eng, 1, 10, 11, 12)    // era 1: no gap
	processSeqs(eng, 1, 3, 13)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || isReordered(got[0]) {
		t.Errorf("got %+v, want one violation for seq 3 of era 1, not named as reordered", got)
	}
}

// TestOnlyTheLastGapsAreKept: a gap older than the last maxLostRanges gaps of the
// era no longer explains a late datagram.
func TestOnlyTheLastGapsAreKept(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	// Gaps at 1, 3, 5, ...: each even seq is sent, each odd one lost.
	seq := uint64(0)
	for i := 0; i <= maxLostRanges; i++ {
		processSeqs(eng, 0, seq)
		seq += 2
	}
	processSeqs(eng, 0, seq, seq+1)
	eng.Flush()
	c.findings = nil

	processSeqs(eng, 0, 1)     // the oldest gap: evicted
	processSeqs(eng, 0, seq-1) // the newest gap: kept
	eng.Flush()
	got := seqResetGaps(t, c)
	if len(got) != 2 || isReordered(got[0]) || !isReordered(got[1]) {
		t.Errorf("got %+v, want seq 1 not named as reordered and seq %d named as reordered", got, seq-1)
	}
}

// TestMergedSeriesAreNotNamedReordered: two series far apart on one key, as when
// the checker merged two publishers. The jump from 101 to 5000 is not a gap a
// late datagram can come from, so each jump back is backward motion.
func TestMergedSeriesAreNotNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 100, 5000, 101, 5001, 102, 5002, 103)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) == 0 {
		t.Fatal("got no FRAME.SEQ_RESET_GAP violations for two merged series")
	}
	for _, f := range got {
		if isReordered(f) {
			t.Errorf("a jump back between merged series was named as reordered: %s", f.Detail)
		}
	}
}

// TestLateDatagramFromAGapAtTheBound: a gap of exactly maxLateGap still explains a
// late datagram from it.
func TestLateDatagramFromAGapAtTheBound(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 3+maxLateGap, 4+maxLateGap, 5+maxLateGap)
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || !isReordered(got[0]) {
		t.Errorf("got %+v, want one violation named as reordered", got)
	}
}

// TestLateDatagramFromAGapOverTheBound: a gap of maxLateGap+1 is not one a late
// datagram can come from, so seq 3 gets the plain detail.
func TestLateDatagramFromAGapOverTheBound(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4+maxLateGap, 5+maxLateGap, 6+maxLateGap)
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || isReordered(got[0]) {
		t.Errorf("got %+v, want one violation not named as reordered", got)
	}
}

// TestMergedSeriesWithCloseCountersAreNotNamedReordered: two publishers merged on
// one key, at 10 and at 100, alternate. The jump from 11 to 100 is a gap of 88,
// within maxLateGap, so the counters cannot tell the merge from reordering. The
// Send Timestamps can: each jump back was sent after the datagram before it.
func TestMergedSeriesWithCloseCountersAreNotNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processInSendOrder(eng, 10, 100, 11, 101, 12, 102, 13, 103)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) == 0 {
		t.Fatal("got no FRAME.SEQ_RESET_GAP violations for two merged series")
	}
	for _, f := range got {
		if isReordered(f) {
			t.Errorf("a jump back between merged series was named as reordered: %s", f.Detail)
		}
	}
}

// TestSecondCopyOfALateDatagramIsNotNamedReordered: seq 3 arrives late and is
// named as reordered. After the dedup ring has evicted it, a second copy arrives.
// No gap counted that copy as lost, so it gets the plain detail.
func TestSecondCopyOfALateDatagramIsNotNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 6)
	processSeqs(eng, 0, 3)
	for s := uint64(7); s < 27; s++ { // more than the 16-entry dedup ring
		processSeqs(eng, 0, s)
	}
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 2 || !isReordered(got[0]) || isReordered(got[1]) {
		t.Errorf("got %+v, want the first copy named as reordered and the second not", got)
	}
}

// TestLateDatagramWithTheSameSendTimestampIsNamedReordered: Send Timestamp must
// not decrease, so a publisher can stamp a batch of datagrams with one value.
// Seq 3 of such a batch arrives late. It was sent no later than the last one,
// so it is named as reordered.
func TestLateDatagramWithTheSameSendTimestampIsNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	for _, s := range []uint64{1, 2, 4, 5, 6, 3} {
		eng.Process(srcA, makeHB(wire.MagicTOB, 1, s, 0, 1000), core.PortMktData, nil)
	}
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || !isReordered(got[0]) {
		t.Errorf("got %+v, want one violation named as reordered", got)
	}
}

// TestLateDatagramFromAnOlderKeptGapIsNamedReordered: two gaps, before 4 and
// before 7. Seq 3 comes from the older one, which the ring still keeps, so it is
// named as reordered.
func TestLateDatagramFromAnOlderKeptGapIsNamedReordered(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 7, 8, 9)
	processSeqs(eng, 0, 3)
	eng.Flush()

	got := seqResetGaps(t, c)
	if len(got) != 1 || !isReordered(got[0]) {
		t.Errorf("got %+v, want one violation named as reordered", got)
	}
}
