package engine

// tob_refdata_reorder_test.go — TOB.QUOTE.REFDATA_KNOWN across the two reorder
// buffers (#140).
//
// A publisher that adds an instrument sends the ManifestSummary with the new
// Manifest Seq first, then the first quotes for the new Instrument ID. Each
// (source, port, channel) series has its own reorder buffer, and the refdata port
// sends a few datagrams a second, so the engine classifies the summary seconds
// after it arrives. The busy mktdata port classifies the quotes first. Judged at
// once, they are judged against the previous set, which is still ready and does
// not hold the new ID. That is a Violation that a subscriber reading in arrival
// order never sees.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// sendTOBQuote feeds one Quote for instrID on the mktdata port of srcA.
func sendTOBQuote(e *Engine, ch uint8, seq uint64, instrID uint32, sendTS uint64) {
	raw := wb.Frame(wire.MagicTOB).
		Channel(ch).
		Seq(seq).
		SendTS(sendTS).
		Msg(wire.TypeQuote, 60, func(b *wb.Body) {
			b.U32(instrID)
			b.U16(1)
			b.U8(0x03)
			b.U8(0)
			b.U64(1_000_000_000)
			b.I64(100)
			b.U64(10)
			b.I64(200)
			b.U64(10)
			b.U16(1)
			b.U16(1)
			b.Pad(4)
		}).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	e.Process(srcA, f, core.PortMktData, sf)
}

// newReorderTOBEngine returns a TOB engine at the default reorder window, with a
// ready set {500} at Manifest Seq 1 on channel ch, and refdata summaries once a
// second up to 10 s. It returns the next refdata sequence number.
func newReorderTOBEngine(t *testing.T, ch uint8) (*Engine, *allCapture, refdataPath, uint64) {
	t.Helper()
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 8}, ac)
	path := refdataPath{ch: ch}
	seq := path.establish(e, 1, 1, []uint32{500}, 0)
	for ts := uint64(2); ts <= 10; ts++ {
		path.sendManifest(e, seq, 1, 1, ts*nsPerSec)
		seq++
	}
	if ready, mseq := readyAt(e, srcA, ch); !ready || mseq != 1 {
		t.Fatalf("setup: set at Manifest Seq 1 not ready (ready=%v, seq=%d)", ready, mseq)
	}
	return e, ac, path, seq
}

// TestTOBRefdataKnownNewInstrumentAfterBumpIsNotAViolation is the shape of #140.
// The publisher bumps the set to {500, 501} at 10.5 s and quotes 501 from 10.6 s.
// Twenty quotes arrive while the bump is still in the refdata reorder buffer, so
// the engine classifies twelve of them before the bump. They must not be judged
// against the set at Manifest Seq 1. When the bump is classified, the new set is
// not ready yet, so the subscriber has not collected 501: NA. Quotes for 501 that
// are classified after the new set is ready pass. Quotes for 999, which is in no
// set, still violate.
func TestTOBRefdataKnownNewInstrumentAfterBumpIsNotAViolation(t *testing.T) {
	const ch = uint8(41)
	e, ac, path, seq := newReorderTOBEngine(t, ch)
	clearFindings(ac)

	path.sendManifest(e, seq, 2, 2, 10*nsPerSec+nsPerSec/2)
	seq++
	mkt := uint64(1)
	for i := uint64(1); i <= 20; i++ {
		sendTOBQuote(e, ch, mkt, 501, 10*nsPerSec+6*nsPerSec/10+i*1_000_000)
		mkt++
	}
	// The engine classified the first twelve quotes, and the bump is still buffered.
	if ready, mseq := readyAt(e, srcA, ch); !ready || mseq != 1 {
		t.Fatalf("setup: the bump was classified before the quotes (ready=%v, seq=%d)", ready, mseq)
	}
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 0 {
		t.Fatalf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s) for quotes judged against the set the publisher had replaced before it sent them", n)
	}

	path.sendDef(e, seq, 500, 2, 11*nsPerSec)
	seq++
	path.sendDef(e, seq, 501, 2, 11*nsPerSec+nsPerSec/2)
	seq++
	for ts := uint64(12); ts <= 30; ts++ {
		path.sendManifest(e, seq, 2, 2, ts*nsPerSec)
		seq++
	}
	// Mid-run, before Flush: the bump is classified, so the held verdicts are out.
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.NA); n != 12 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d NA before Flush, want 12 (the quotes classified before the bump)", n)
	}
	for i := uint64(1); i <= 5; i++ {
		sendTOBQuote(e, ch, mkt, 501, 30*nsPerSec+i*1_000_000)
		mkt++
	}
	for i := uint64(1); i <= 3; i++ {
		sendTOBQuote(e, ch, mkt, 999, 30*nsPerSec+10_000_000+i*1_000_000)
		mkt++
	}
	e.Flush()

	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.NA); n != 12 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d NA, want 12", n)
	}
	// The other eight early quotes are classified after the new set is ready, and
	// it holds 501.
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Pass); n != 13 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d pass, want 13 (8 early quotes and 5 late quotes for 501, judged on the new set)", n)
	}
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 3 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s), want 3 (the quotes for 999, which is in no set)", n)
	}
	for _, f := range findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		if f.Port != core.PortMktData {
			t.Errorf("finding on port %v, want the quote's mktdata port", f.Port)
		}
	}
}

// TestTOBRefdataKnownUnknownInstrumentStillViolates: with no set change, a quote
// for an Instrument ID that is in no set is a Violation. The verdict waits for the
// refdata sent before the quote, and comes out during the run, not only at Flush.
func TestTOBRefdataKnownUnknownInstrumentStillViolates(t *testing.T) {
	const ch = uint8(41)
	e, ac, path, seq := newReorderTOBEngine(t, ch)
	clearFindings(ac)

	mkt := uint64(1)
	for i := uint64(1); i <= 20; i++ {
		sendTOBQuote(e, ch, mkt, 999, 10*nsPerSec+6*nsPerSec/10+i*1_000_000)
		mkt++
	}
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 0 {
		t.Fatalf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s) while refdata sent before the quotes is still buffered", n)
	}
	for ts := uint64(11); ts <= 20; ts++ {
		path.sendManifest(e, seq, 1, 1, ts*nsPerSec)
		seq++
	}
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 12 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s) before Flush, want 12 (the quotes classified so far)", n)
	}
	e.Flush()
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 20 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s), want 20", n)
	}
	if n := len(findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN")); n != 20 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d finding(s), want one for each of the 20 quotes", n)
	}
}

// TestTOBRefdataKnownStoppedRefdataIsUnverifiable: the refdata path stops with
// datagrams in its reorder buffer. A buffer is released only by later datagrams
// of its path, or at Flush, so a held verdict would wait for the end of the run,
// and a process that never exits would never report it. A quote for an unknown ID
// sent long after the path stopped is Unverifiable, reason pending, at once.
func TestTOBRefdataKnownStoppedRefdataIsUnverifiable(t *testing.T) {
	const ch = uint8(41)
	e, ac, _, _ := newReorderTOBEngine(t, ch)
	clearFindings(ac)

	sendTOBQuote(e, ch, 1, 999, 10*nsPerSec+nsPerSec/2)
	sendTOBQuote(e, ch, 2, 999, 40*nsPerSec)
	// ReorderWindow 8: neither quote is classified yet, so push them through.
	for i := uint64(3); i <= 10; i++ {
		sendTOBQuote(e, ch, i, 500, 40*nsPerSec+i)
	}
	fs := findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN")
	var unv int
	for _, f := range fs {
		switch f.Status {
		case core.Unverifiable:
			unv++
			if f.Reason != core.ReasonPending {
				t.Errorf("TOB.QUOTE.REFDATA_KNOWN: reason %q, want %q", f.Reason, core.ReasonPending)
			}
		case core.Violation:
			t.Errorf("TOB.QUOTE.REFDATA_KNOWN: violation while datagrams sent before the quote are buffered: %s", f.Detail)
		}
	}
	if unv != 2 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d unverifiable before Flush, want 2", unv)
	}
}

// TestTOBRefdataKnownEvictionReportsHeldVerdicts: the refdata instance is evicted
// while verdicts wait for its buffer. Eviction discards the buffer, so each held
// verdict is Unverifiable, reason truncated, and none is lost or a Violation.
func TestTOBRefdataKnownEvictionReportsHeldVerdicts(t *testing.T) {
	const ch = uint8(41)
	e, ac, _, _ := newReorderTOBEngine(t, ch)
	clearFindings(ac)

	for i := uint64(1); i <= 20; i++ {
		sendTOBQuote(e, ch, i, 999, 10*nsPerSec+6*nsPerSec/10+i*1_000_000)
	}
	if n := len(findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN")); n != 0 {
		t.Fatalf("setup: %d verdict(s) before eviction, want all held", n)
	}
	floodInstances(e, wire.MagicTOB)
	if _, ok := e.refdata.channels[refdataKey{src: srcA, ch: ch}]; ok {
		t.Fatal("setup: the refdata instance was not evicted")
	}
	if n := countStatus(ac, "TOB.QUOTE.REFDATA_KNOWN", core.Violation); n != 0 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d violation(s) decided at eviction", n)
	}
	var truncated int
	for _, f := range findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		if f.Status == core.Unverifiable && f.Reason == core.ReasonTruncated {
			truncated++
		}
	}
	if truncated != 12 {
		t.Errorf("TOB.QUOTE.REFDATA_KNOWN: %d unverifiable (truncated), want 12 (the held verdicts)", truncated)
	}
}
