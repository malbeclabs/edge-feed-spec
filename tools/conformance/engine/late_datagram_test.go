package engine

// late_datagram_test.go — a datagram that arrives after the reorder window has
// moved past it is late, not backward motion.
//
// The window holds a number of datagrams, not a time. On a long path a delay of a
// few milliseconds is more datagrams than the window holds, so the window pops
// the datagrams after the delayed one, and the gap before them counts it as
// transport loss. When it arrives, its Sequence Number is below the last one.
// The publisher sent it in order, so FRAME.SEQ_RESET_GAP cannot charge it.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// processSeqs sends one heartbeat per Sequence Number on the mktdata port.
func processSeqs(eng *Engine, era uint8, seqs ...uint64) {
	for _, s := range seqs {
		eng.Process(srcA, makeHB(wire.MagicTOB, 1, s, era, 1000+s), core.PortMktData, nil)
	}
}

// TestLateDatagramPastTheWindowIsReorder: seq 3 arrives after 4, 5 and 6, so the
// gap before 4 counted it as lost. Its arrival is Unverifiable, reason reorder.
func TestLateDatagramPastTheWindowIsReorder(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 6)
	processSeqs(eng, 0, 3)
	eng.Flush()

	if n := c.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Fatalf("got %d FRAME.SEQ_RESET_GAP violations, want 0: the gap before 4 counted seq 3 as lost", n)
	}
	got := c.findingsFor("FRAME.SEQ_RESET_GAP")
	if len(got) != 1 || got[0].Status != core.Unverifiable || got[0].Reason != core.ReasonReorder {
		t.Errorf("got %+v, want one Unverifiable, reason reorder", got)
	}
}

// TestBackwardDatagramOutsideAGapIsViolation: seq 3 was never counted as lost, so
// it arriving below the last one is backward motion.
func TestBackwardDatagramOutsideAGapIsViolation(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 4, 5, 7, 8, 9) // the gap before 7 counts 6 only
	processSeqs(eng, 0, 3)
	eng.Flush()

	if n := c.violationsFor("FRAME.SEQ_RESET_GAP"); n != 1 {
		t.Errorf("got %d FRAME.SEQ_RESET_GAP violations, want 1", n)
	}
}

// TestLostRangesEndWithTheEra: a gap of era 0 counted seq 3 as lost. Era 1 starts
// at 10 with no gap, so seq 3 below the last one in era 1 is backward motion: the
// old era's gaps do not excuse it.
func TestLostRangesEndWithTheEra(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 4, 5, 6) // era 0: the gap before 4 counts 3
	processSeqs(eng, 1, 10, 11, 12)    // era 1: no gap
	processSeqs(eng, 1, 3, 13)
	eng.Flush()

	if n := c.violationsFor("FRAME.SEQ_RESET_GAP"); n != 1 {
		t.Errorf("got %d FRAME.SEQ_RESET_GAP violations, want 1 for seq 3 of era 1", n)
	}
}

// TestOnlyTheLastGapsAreKept: a gap older than the last maxLostRanges gaps of the
// era no longer excuses a late datagram.
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
	got := c.findingsFor("FRAME.SEQ_RESET_GAP")
	if len(got) != 2 || got[0].Status != core.Violation || got[1].Status != core.Unverifiable {
		t.Errorf("got %+v, want a violation for seq 1 and an Unverifiable for seq %d", got, seq-1)
	}
}

// TestMergedSeriesStayBackwardMotion: two series far apart on one key, as when
// the checker merged two publishers. The jump from 101 to 5000 is not a gap a
// late datagram can come from, so each jump back is still a violation.
func TestMergedSeriesStayBackwardMotion(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 100, 5000, 101, 5001, 102, 5002, 103)
	eng.Flush()

	if n := c.violationsFor("FRAME.SEQ_RESET_GAP"); n == 0 {
		t.Errorf("got no FRAME.SEQ_RESET_GAP violations for two merged series: the merge must stay loud")
	}
	for _, f := range c.findingsFor("FRAME.SEQ_RESET_GAP") {
		if f.Status == core.Unverifiable {
			t.Errorf("a jump back between merged series was graded as reorder: %s", f.Detail)
		}
	}
}

// TestLateDatagramFromAGapAtTheBound: a gap of exactly maxLateGap still excuses
// a late datagram from it.
func TestLateDatagramFromAGapAtTheBound(t *testing.T) {
	c := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, c)
	processSeqs(eng, 0, 1, 2, 3+maxLateGap, 4+maxLateGap, 5+maxLateGap)
	processSeqs(eng, 0, 3)
	eng.Flush()

	if n := c.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("got %d violations, want 0: the gap before %d counted seq 3 as lost", n, 3+maxLateGap)
	}
}
