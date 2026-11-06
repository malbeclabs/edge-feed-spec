package engine

// refdata_replace_test.go — the refdata set-state machine when a publisher on a new
// source IP address replaces the publisher of a channel.
//
// The state machine is keyed by Channel ID, so every instance of a channel feeds
// one set. The Reset Count of each instance tells a new session from the same
// session seen on another address, the same rule the market-by-price state uses
// (see Engine.stampRefdataSession):
//
//   - A new address that starts in a Reset Count other than the one the set is
//     built for is a new session. It discards the set. Its `Manifest Seq` MAY
//     restart from any value (reference-data supplement, step 7).
//   - A new address in the same Reset Count is the same session on another path
//     (a lease move, or a redundant path). It keeps the set.
//   - A new address that starts in the Reset Count the channel left is behind
//     the reset for one reorder window of datagrams, and then a new session.
//   - A datagram of a session the channel left feeds nothing, and is reported
//     Unverifiable on the rules it would reach.
//
// The session of a datagram is decided in arrival order, at intake. Each
// instance has its own reorder buffer, so classification order is not arrival
// order.

import (
	"net/netip"
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// refdataPublisher is one publisher's refdata datagrams on one channel: its own
// source IP address, its own Reset Count and its own sequence space.
type refdataPublisher struct {
	src netip.Addr
	ch  uint8
	era uint8
	seq uint64
}

// send sends one datagram that carries one message.
func (p *refdataPublisher) send(e *Engine, sendTS uint64, typ, declaredLen uint8, body func(*wb.Body)) {
	p.seq++
	raw := wb.Frame(wire.MagicTOB).Channel(p.ch).Seq(p.seq).ResetCount(p.era).SendTS(sendTS).
		Msg(typ, declaredLen, body).Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	e.Process(p.src, f, core.PortRefData, sf)
}

// cycle sends one ManifestSummary(Valid=1) and one InstrumentDefinition per
// instrument, all tagged with mseq.
func (p *refdataPublisher) cycle(e *Engine, sendTS uint64, mseq uint16, instrs []uint32) {
	p.send(e, sendTS, 0x07, 24, manifestBody(1, mseq, uint32(len(instrs))))
	for _, id := range instrs {
		p.send(e, sendTS, 0x02, 130, instrDefTOBBody(id, mseq))
	}
}

// violations returns the Violations of a rule.
func violations(ac *allCapture, ruleID string) []core.Finding {
	var out []core.Finding
	for _, f := range findingsFor(ac, ruleID) {
		if f.Status == core.Violation {
			out = append(out, f)
		}
	}
	return out
}

// channelSet reports whether channel ch is ready, its Manifest Seq and its
// definition count.
func channelSet(e *Engine, ch uint8) (ready bool, mseq uint16, defs int) {
	if e.refdata == nil {
		return false, 0, 0
	}
	s, ok := e.refdata.channels[ch]
	if !ok {
		return false, 0, 0
	}
	return channelReady(s), s.latestSeq, len(s.defs)
}

// TestRefdataPublisherReplacedOnNewAddress: the old publisher serves the channel
// at Manifest Seq 5 in Reset Count 35, and stops. A new publisher starts on the
// same Channel ID from another source IP address, in Reset Count 1, at Manifest
// Seq 1. That is a new session, and the new publisher did nothing wrong.
//
// Before the fix the new instance only seeded its own Reset Count and kept the
// old set. Each summary of the new publisher then read as a regress from 5 and
// was discarded, so `latestSeq` stayed 5 until the process restarted, and each
// definition read as tagged with a stale seq.
func TestRefdataPublisherReplacedOnNewAddress(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	instrs := []uint32{1, 2, 3}
	old := &refdataPublisher{src: srcA, ch: 1, era: 35}
	for i := uint64(0); i < 3; i++ {
		old.cycle(e, i*nsPerSec, 5, instrs)
	}
	e.Flush()
	if ready, mseq, _ := channelSet(e, 1); !ready || mseq != 5 {
		t.Fatalf("setup: the old publisher's set is not ready at Manifest Seq 5 (ready=%v, seq=%d)", ready, mseq)
	}

	repl := &refdataPublisher{src: srcB, ch: 1, era: 1}
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
	}
	e.Flush()
	e.EndRun()

	for _, rule := range []string{"REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP"} {
		if v := violations(ac, rule); len(v) != 0 {
			t.Errorf("%s: %d Violation(s) for a new publisher in a new Reset Count, first: %s", rule, len(v), v[0].Detail)
		}
	}
	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 1 {
		t.Errorf("the channel did not take the new publisher's set: ready=%v, Manifest Seq=%d, %d definitions; want ready at 1",
			ready, mseq, defs)
	}
}

// TestRefdataSameSessionOnNewAddressKeepsTheSet: the converse. A new address in
// the Reset Count the set is built for is the same session on another path. It
// keeps the set, so the rules that consume it stay ready through the move. And a
// summary on it that goes back is still a regress: the fix must not excuse one.
func TestRefdataSameSessionOnNewAddressKeepsTheSet(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	instrs := []uint32{1, 2, 3}
	first := &refdataPublisher{src: srcA, ch: 1, era: 7}
	for i := uint64(0); i < 2; i++ {
		first.cycle(e, i*nsPerSec, 5, instrs)
	}
	e.Flush()

	moved := &refdataPublisher{src: srcB, ch: 1, era: 7}
	moved.send(e, 2*nsPerSec, 0x07, 24, manifestBody(1, 5, 3))
	e.Flush()
	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 5 || defs != 3 {
		t.Errorf("the set was discarded on a move in the same Reset Count: ready=%v, Manifest Seq=%d, %d definitions",
			ready, mseq, defs)
	}

	moved.send(e, 3*nsPerSec, 0x07, 24, manifestBody(1, 4, 3))
	e.Flush()
	if len(violations(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS")) != 1 {
		t.Errorf("REFDATA.SEQ_MONOTONIC_NO_REGRESS: want 1 Violation for 5 -> 4 in one session, got %d",
			len(violations(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS")))
	}
}

// TestRefdataResetSeenOnTwoAddressesDiscardsOnce: one reset of a session that two
// addresses carry. Address A resets from Reset Count 1 to 2 and rebuilds the set.
// Address B then sends its first datagram, still in Reset Count 1 (it was behind
// the reset), and then enters 2. The set A rebuilt in era 2 must survive both:
// B's era 1 is the era the channel left, and B's era 2 is the era the set is
// built for.
func TestRefdataResetSeenOnTwoAddressesDiscardsOnce(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	instrs := []uint32{1, 2}
	a := &refdataPublisher{src: srcA, ch: 1, era: 1}
	a.cycle(e, 0, 3, instrs)
	a.era, a.seq = 2, 0
	a.cycle(e, nsPerSec, 1, instrs)
	e.Flush()
	if ready, mseq, _ := channelSet(e, 1); !ready || mseq != 1 {
		t.Fatalf("setup: the set of era 2 is not ready at Manifest Seq 1 (ready=%v, seq=%d)", ready, mseq)
	}

	// B's era-1 datagram carries a Heartbeat only, so what it changes is the era
	// bookkeeping and nothing in the set.
	b := &refdataPublisher{src: srcB, ch: 1, era: 1}
	b.send(e, 0, wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(3).U64(0) })
	b.era, b.seq = 2, 0
	b.send(e, nsPerSec, 0x07, 24, manifestBody(1, 1, 2))
	e.Flush()

	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 1 || defs != 2 {
		t.Errorf("one reset discarded the set twice: ready=%v, Manifest Seq=%d, %d definitions; want ready at 1 with 2",
			ready, mseq, defs)
	}
}

// TestRefdataStoppedPublisherBufferDoesNotFeedTheNewSet: a publisher that stops
// leaves its last datagrams in its instance's reorder buffer, and Flush classifies
// them after the publisher that replaced it. They belong to the session the
// channel discarded, so they feed nothing. Fed to the set, the old Manifest Seq 5
// came back over the new publisher's 1: a SEQ_BUMP_NOT_BY_ONE from 1 to 5, and a
// regress on each datagram of the new publisher drained after them.
func TestRefdataStoppedPublisherBufferDoesNotFeedTheNewSet(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 8}, ac)

	instrs := []uint32{1, 2, 3}
	old := &refdataPublisher{src: srcA, ch: 1, era: 35}
	for i := uint64(0); i < 5; i++ {
		old.cycle(e, i*nsPerSec, 5, instrs)
	}
	repl := &refdataPublisher{src: srcB, ch: 1, era: 1}
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
	}
	e.Flush()

	for _, rule := range []string{"REFDATA.SEQ_BUMP_NOT_BY_ONE", "REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP"} {
		if v := violations(ac, rule); len(v) != 0 {
			t.Errorf("%s: %d Violation(s) after Flush drained the stopped publisher, first: %s", rule, len(v), v[0].Detail)
		}
	}
	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 1 {
		t.Errorf("the stopped publisher's buffer changed the new set: ready=%v, Manifest Seq=%d, %d definitions; want ready at 1",
			ready, mseq, defs)
	}
}

// TestRefdataStoppedPublisherBufferAfterTheNewOneResets: the same, but the new
// publisher also resets once before Flush. The era of the stopped publisher is
// then not the era the channel left last: it is an era before that. It still
// feeds nothing, because only the era the set is built for does.
func TestRefdataStoppedPublisherBufferAfterTheNewOneResets(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 8}, ac)

	instrs := []uint32{1, 2, 3}
	old := &refdataPublisher{src: srcA, ch: 1, era: 31}
	for i := uint64(0); i < 5; i++ {
		old.cycle(e, i*nsPerSec, 6, instrs)
	}
	repl := &refdataPublisher{src: srcB, ch: 1, era: 1}
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
	}
	repl.era, repl.seq = 2, 0
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (200+i)*nsPerSec, 1, instrs)
	}
	e.Flush()

	for _, rule := range []string{"REFDATA.SEQ_BUMP_NOT_BY_ONE", "REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP"} {
		if v := violations(ac, rule); len(v) != 0 {
			t.Errorf("%s: %d Violation(s) after Flush drained the stopped publisher, first: %s", rule, len(v), v[0].Detail)
		}
	}
	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 1 {
		t.Errorf("the stopped publisher's buffer changed the new set: ready=%v, Manifest Seq=%d, %d definitions; want ready at 1",
			ready, mseq, defs)
	}
}

// assertNewSet checks that a replacement left no sequence findings, and that the
// channel holds the new publisher's set: ready, at Manifest Seq 1, with all of
// instrs.
func assertNewSet(t *testing.T, e *Engine, ac *allCapture, instrs []uint32) {
	t.Helper()
	for _, rule := range []string{"REFDATA.SEQ_BUMP_NOT_BY_ONE", "REFDATA.SEQ_MONOTONIC_NO_REGRESS", "REFDATA.STALE_SEQ_TAG_AFTER_BUMP"} {
		if v := violations(ac, rule); len(v) != 0 {
			t.Errorf("%s: %d Violation(s), first: %s", rule, len(v), v[0].Detail)
		}
	}
	if ready, mseq, defs := channelSet(e, 1); !ready || mseq != 1 || defs != len(instrs) {
		t.Errorf("the channel does not hold the new publisher's set: ready=%v, Manifest Seq=%d, %d definitions; want ready at 1 with %d",
			ready, mseq, defs, len(instrs))
	}
}

// TestRefdataReplacementResetsIntoTheOldResetCount: the old publisher stops in
// Reset Count 31 at Manifest Seq 6. Its replacement starts in Reset Count 30, then
// resets to 31 at Manifest Seq 1. The value 31 then names two sessions. The old
// publisher's datagrams that Flush drains from its buffer are of the first one,
// and must feed nothing to the set of the second one.
func TestRefdataReplacementResetsIntoTheOldResetCount(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 8}, ac)

	instrs := []uint32{1, 2, 3}
	old := &refdataPublisher{src: srcA, ch: 1, era: 31}
	for i := uint64(0); i < 5; i++ {
		old.cycle(e, i*nsPerSec, 6, instrs)
	}
	repl := &refdataPublisher{src: srcB, ch: 1, era: 30}
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
	}
	repl.era, repl.seq = 31, 0
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (200+i)*nsPerSec, 1, instrs)
	}
	e.Flush()

	assertNewSet(t, e, ac, instrs)
}

// TestRefdataShortPublisherClassifiedAfterItsReplacement: the old publisher sends
// one cycle of four datagrams and stops. With a reorder window of 8, all four stay
// in its buffer, and Flush classifies them after the five cycles of its
// replacement. Arrival order decides which session is the newer one, not the
// order of classification. Decided at classification, the old publisher's era
// discarded the new set at Flush, and then the era check rejected every datagram
// of the new publisher until it reset.
func TestRefdataShortPublisherClassifiedAfterItsReplacement(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 8}, ac)

	instrs := []uint32{1, 2, 3}
	old := &refdataPublisher{src: srcA, ch: 1, era: 35}
	old.cycle(e, 0, 5, instrs)
	repl := &refdataPublisher{src: srcB, ch: 1, era: 1}
	for i := uint64(0); i < 5; i++ {
		repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
	}
	e.Flush()
	// The new publisher keeps sending, in the same Reset Count.
	for i := uint64(0); i < 2; i++ {
		repl.cycle(e, (105+i)*nsPerSec, 1, instrs)
	}
	e.Flush()

	assertNewSet(t, e, ac, instrs)
}

// quote sends one Quote for instrument instr on the mktdata port of the
// publisher, in its Reset Count and in a sequence space of its own.
func (p *refdataPublisher) quote(e *Engine, mseq *uint64, sendTS uint64, instr uint32) {
	*mseq++
	raw := wb.Frame(wire.MagicTOB).Channel(p.ch).Seq(*mseq).ResetCount(p.era).SendTS(sendTS).
		Msg(wire.TypeQuote, 60, func(b *wb.Body) {
			b.U32(instr).U16(1).U8(0x03).U8(0).U64(1_000_000_000)
			b.I64(100).U64(10).I64(200).U64(10).U16(1).U16(1).Pad(4)
		}).Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	e.Process(p.src, f, core.PortMktData, sf)
}

// statusCount counts the findings of a rule by status.
func statusCount(ac *allCapture, ruleID string) map[core.Status]int {
	out := make(map[core.Status]int)
	for _, f := range findingsFor(ac, ruleID) {
		out[f.Status]++
	}
	return out
}

// TestRefdataReplacementInTheResetCountItsPredecessorLeft: a publisher starts in
// Reset Count 1, resets to 2 and stops. Its replacement, on another source IP
// address, starts in Reset Count 1 with other instruments. Its first datagrams
// read as behind the reset, as a datagram in transit would. But a run of them
// longer than the reorder window is not a delay: it is a new session, and the
// channel takes the replacement's set.
//
// Before the fix, the replacement stayed behind the reset for its whole era. The
// channel kept the stopped publisher's set, and every quote of the replacement
// was a TOB.QUOTE.REFDATA_KNOWN Violation.
func TestRefdataReplacementInTheResetCountItsPredecessorLeft(t *testing.T) {
	for _, window := range []int{1, 4} {
		ac := &allCapture{}
		e := New(Config{Feed: core.FeedTOB, ReorderWindow: window}, ac)

		old := &refdataPublisher{src: srcA, ch: 1, era: 1}
		old.cycle(e, 0, 1, []uint32{1, 2, 3})
		old.era, old.seq = 2, 0
		old.cycle(e, nsPerSec, 1, []uint32{1, 2, 3})
		e.Flush()

		instrs := []uint32{4, 5, 6}
		repl := &refdataPublisher{src: srcB, ch: 1, era: 1}
		var mseq uint64
		quotes := 0
		for i := uint64(0); i < 10; i++ {
			repl.cycle(e, (100+i)*nsPerSec, 1, instrs)
			if i < 5 {
				continue // the replacement's set needs a cycle to complete
			}
			for _, id := range instrs {
				repl.quote(e, &mseq, (100+i)*nsPerSec, id)
				quotes++
			}
		}
		e.Flush()

		if v := violations(ac, "TOB.QUOTE.REFDATA_KNOWN"); len(v) != 0 {
			t.Errorf("window %d: %d of %d quotes of the replacement are TOB.QUOTE.REFDATA_KNOWN Violations, first: %s",
				window, len(v), quotes, v[0].Detail)
		}
		if got := statusCount(ac, "TOB.QUOTE.REFDATA_KNOWN")[core.Pass]; got != quotes {
			t.Errorf("window %d: TOB.QUOTE.REFDATA_KNOWN passed %d of %d quotes of the replacement", window, got, quotes)
		}
		if ready, _, defs := channelSet(e, 1); !ready || defs != len(instrs) {
			t.Errorf("window %d: the channel does not hold the replacement's set: ready=%v, %d definitions", window, ready, defs)
		}
	}
}

// TestRefdataPathLeftInAnOlderSessionIsReported: two paths carry one session in
// Reset Count 1. Path A restarts into Reset Count 2, which is a new session, and
// path B keeps sending in Reset Count 1. B's datagrams are of a session the
// channel left, so they feed nothing to the set. They are still opportunities of
// the rules they would reach: a Manifest Seq regress on B is reported, as
// Unverifiable, and not dropped without a finding.
func TestRefdataPathLeftInAnOlderSessionIsReported(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	instrs := []uint32{1, 2, 3}
	a := &refdataPublisher{src: srcA, ch: 1, era: 1}
	b := &refdataPublisher{src: srcB, ch: 1, era: 1, seq: 1000}
	for i := uint64(0); i < 2; i++ {
		a.cycle(e, i*nsPerSec, 5, instrs)
		b.cycle(e, i*nsPerSec, 5, instrs)
	}
	a.era, a.seq = 2, 0
	for i := uint64(2); i < 5; i++ {
		a.cycle(e, i*nsPerSec, 1, instrs)
		b.cycle(e, i*nsPerSec, 5, instrs)
	}
	b.send(e, 5*nsPerSec, 0x07, 24, manifestBody(1, 4, 3))
	regress := b.seq
	a.cycle(e, 5*nsPerSec, 1, instrs)
	e.Flush()

	found := false
	for _, f := range findingsFor(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS") {
		if f.Seq != regress {
			continue
		}
		found = true
		if f.Status != core.Unverifiable || f.Reason != core.ReasonTransition {
			t.Errorf("the regress on the path left in Reset Count 1 is %v (%s), want Unverifiable (%s)",
				f.Status, f.Reason, core.ReasonTransition)
		}
	}
	if !found {
		t.Errorf("the Manifest Seq regress on the path left in Reset Count 1 gave no REFDATA.SEQ_MONOTONIC_NO_REGRESS finding")
	}
	if ready, mseq, _ := channelSet(e, 1); !ready || mseq != 1 {
		t.Errorf("the path left in Reset Count 1 changed the set of Reset Count 2: ready=%v, Manifest Seq=%d", ready, mseq)
	}
}

// TestRefdataSecondPublisherKeepsTheServingOneAccounted: a publisher serves the
// channel, and a second publisher on another address, in another Reset Count,
// sends more than one reorder window of datagrams. The second one starts a new
// session, so the serving publisher's datagrams are of a session the channel
// left. Its definition cycles are still opportunities of
// REFDATA.DEFINITION_CYCLE_COVERAGE: each is reported, Unverifiable once it is
// no longer graded, and the rule's denominator does not fall.
func TestRefdataSecondPublisherKeepsTheServingOneAccounted(t *testing.T) {
	const cycles = 10
	run := func(second bool) map[core.Status]int {
		ac := &allCapture{}
		e := New(Config{Feed: core.FeedTOB, ReorderWindow: 2, ExpectDefinitionCycle: time.Second}, ac)
		instrs := []uint32{1, 2, 3}
		serving := &refdataPublisher{src: srcA, ch: 1, era: 7}
		other := &refdataPublisher{src: srcB, ch: 1, era: 9}
		for i := uint64(0); i <= cycles; i++ {
			serving.cycle(e, i*nsPerSec, 5, instrs)
			if second && i == 1 {
				other.cycle(e, i*nsPerSec+1, 1, []uint32{8})
				other.cycle(e, i*nsPerSec+2, 1, []uint32{8})
			}
		}
		e.Flush()
		return statusCount(ac, "REFDATA.DEFINITION_CYCLE_COVERAGE")
	}
	alone, with := run(false), run(true)
	if alone[core.Pass] < cycles-2 {
		t.Fatalf("setup: the serving publisher alone passed %d cycles", alone[core.Pass])
	}
	total := func(m map[core.Status]int) int { return m[core.Pass] + m[core.Violation] + m[core.Unverifiable] }
	if total(with) < total(alone) {
		t.Errorf("REFDATA.DEFINITION_CYCLE_COVERAGE accounts for %d cycles with a second publisher (%v), and for %d without (%v)",
			total(with), with, total(alone), alone)
	}
	if with[core.Unverifiable] == 0 {
		t.Errorf("REFDATA.DEFINITION_CYCLE_COVERAGE reports no Unverifiable for the cycles it no longer grades: %v", with)
	}
}

// TestRefdataPathsInOneResetCountShareTheNewestSession: path A sends first, in
// Reset Count 2. Path B sends in Reset Count 1, which starts a new session, and
// then enters Reset Count 2, which starts another. A is in Reset Count 2 too, so
// it is a path of that session, and is graded with B: a Manifest Seq regress on A
// is a Violation.
//
// Before the fix, A kept the first session while its own Reset Count did not
// change, and its datagrams fed nothing, with no finding.
func TestRefdataPathsInOneResetCountShareTheNewestSession(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)

	instrs := []uint32{1, 2, 3}
	a := &refdataPublisher{src: srcA, ch: 1, era: 2}
	b := &refdataPublisher{src: srcB, ch: 1, era: 1, seq: 1000}
	a.cycle(e, 0, 5, instrs)
	b.cycle(e, 0, 5, instrs)
	b.era, b.seq = 2, 2000
	for i := uint64(1); i < 4; i++ {
		b.cycle(e, i*nsPerSec, 5, instrs)
		a.cycle(e, i*nsPerSec, 5, instrs)
	}
	a.send(e, 4*nsPerSec, 0x07, 24, manifestBody(1, 4, 3))
	regress := a.seq
	b.cycle(e, 4*nsPerSec, 5, instrs)
	e.Flush()

	found := false
	for _, f := range violations(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS") {
		if f.Seq == regress {
			found = true
		}
	}
	if !found {
		t.Errorf("the Manifest Seq regress on path A in Reset Count 2 is not a Violation: %v",
			statusCount(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS"))
	}
}
