package engine

// tob_test.go — Tests for the TOB feed validator (Task 16).
//
// Covers TOB.QUOTE.REFDATA_KNOWN:
//
//   - Before refdata reaches ready(): status must be NA, never Violation.
//   - After ready(), instrument absent from def set: Violation.
//   - After ready(), instrument in def set: silent (no finding emitted).
//
// Uses the same Engine-level helpers as cadence_test.go.
// ReorderWindow is set to 1 so each frame is classified on the next Process call,
// with a final Flush() before inspecting findings.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- helpers ---

// newTOBEngine creates a TOB-feed engine with ReorderWindow=1, wired to an
// allCapture reporter.
func newTOBEngine() (*Engine, *allCapture) {
	ac := &allCapture{}
	cfg := Config{
		Feed:          core.FeedTOB,
		ReorderWindow: 1,
	}
	return New(cfg, ac), ac
}

// buildQuoteFrame builds a minimal conformant Quote (0x03) frame for TOB.
// instrID is placed at body offset 0 (spec offset 4).
// updateFlags: bit0=bidUpd, bit1=askUpd; bidPrice < askPrice for non-crossed.
func buildQuoteFrame(magic uint16, ch uint8, instrID uint32) []byte {
	return wb.Frame(magic).
		Channel(ch).
		Msg(wire.TypeQuote, 60, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID (body off 0)
			b.U16(1)             // SourceID (body off 4)
			b.U8(0x03)           // UpdateFlags: bid+ask updated (body off 6)
			b.U8(0)              // Reserved (body off 7)
			b.U64(1_000_000_000) // SourceTimestamp (body off 8)
			b.I64(100)           // BidPrice (body off 16)
			b.U64(10)            // BidQty (body off 24)
			b.I64(200)           // AskPrice (body off 32)
			b.U64(10)            // AskQty (body off 40)
			b.U16(1)             // BidSourceCount (body off 48)
			b.U16(1)             // AskSourceCount (body off 50)
			b.Pad(4)             // Reserved (body off 52) → total body 56 → msg 60
		}).
		Bytes()
}

// buildTradeFrame builds a minimal conformant Trade (0x04) frame for TOB.
// instrID is placed at body offset 0 (spec offset 4).
func buildTradeFrame(magic uint16, ch uint8, instrID uint32) []byte {
	return wb.Frame(magic).
		Channel(ch).
		Msg(wire.TypeTrade, 52, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID (body off 0)
			b.U16(1)             // SourceID (body off 4)
			b.U8(1)              // AggressorSide (body off 6)
			b.U8(0)              // TradeFlags (body off 7)
			b.U64(1_000_000_000) // SourceTimestamp (body off 8)
			b.I64(150)           // TradePrice (body off 16)
			b.U64(5)             // TradeQuantity (body off 24)
			b.U64(42)            // TradeID (body off 32)
			b.U64(100)           // CumulativeVolume (body off 40)
			// total body 48 bytes → msg 52
		}).
		Bytes()
}

// reachedReadyTOB drives an engine through the refdata bootstrap for channel ch,
// registering instrument instrID. Returns the next frame seq to use.
//
// Sequence: Manifest(valid=1,seq=1,count=1) → InstrDef(instrID,seq=1)
// → Manifest(valid=1,seq=1,count=1) to close the cycle.
// With ReorderWindow=1, each seq n+1 pops seq n from the buffer.
func reachedReadyTOB(e *Engine, ch uint8, instrID uint32, startSeq uint64) uint64 {
	seq := startSeq
	// ManifestSummary: valid=1, manifestSeq=1, count=1.
	raw := wb.Frame(wire.MagicTOB).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = seq
	e.Process(f, core.PortRefData, sf)
	seq++

	// InstrumentDefinition: instrID at manifestSeq=1.
	rawDef := buildInstrDefFrameWithTS(1_000_000_000, instrID, 1, ch)
	fDef, sfDef := wire.Decode(rawDef, wire.MagicTOB)
	fDef.Header.Sequence = seq
	e.Process(fDef, core.PortRefData, sfDef)
	seq++

	// Second ManifestSummary to close the cycle and confirm ready state.
	raw2 := wb.Frame(wire.MagicTOB).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	f2, sf2 := wire.Decode(raw2, wire.MagicTOB)
	f2.Header.Sequence = seq
	e.Process(f2, core.PortRefData, sf2)
	seq++

	return seq
}

// --- tests ---

// TestTOBRefdataKnownBeforeReady: before the refdata for the channel is ready,
// TOB.QUOTE.REFDATA_KNOWN must emit NA, never a Violation.
func TestTOBRefdataKnownBeforeReady(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Send a Quote with NO refdata frames at all (cold start).
	raw := buildQuoteFrame(wire.MagicTOB, ch, instrID)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		if fn.Status == core.Violation {
			t.Errorf("TOB.QUOTE.REFDATA_KNOWN: must NOT be Violation before refdata ready; got %v", fn.Status)
		}
	}
}

// TestTOBRefdataKnownAfterReadyKnownInstrument: once refdata is ready and the
// instrument is in the def set, no finding should be emitted.
func TestTOBRefdataKnownAfterReadyKnownInstrument(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Bootstrap refdata for instrID on a separate port counter.
	// We use a high refdata seq range so we can track mktdata seq separately.
	reachedReadyTOB(e, ch, instrID, 100)

	clearFindings(ac)

	// Now send a Quote for the known instrument on the mktdata port.
	raw := buildQuoteFrame(wire.MagicTOB, ch, instrID)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		if fn.Status == core.Violation {
			t.Errorf("TOB.QUOTE.REFDATA_KNOWN: must NOT fire Violation for known instrument, got %v", fn.Status)
		}
	}
}

// TestTOBRefdataKnownAfterReadyUnknownInstrument: once refdata is ready but the
// Quote carries an instrument absent from the def set, a Violation must fire.
func TestTOBRefdataKnownAfterReadyUnknownInstrument(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const knownInstr = uint32(42)
	const unknownInstr = uint32(999) // not in the def set

	// Bootstrap refdata: only instrID=42 is defined.
	reachedReadyTOB(e, ch, knownInstr, 100)

	clearFindings(ac)

	// Quote for unknownInstr=999 — absent from def set → Violation.
	raw := buildQuoteFrame(wire.MagicTOB, ch, unknownInstr)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	if !hasViolation(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		t.Error("TOB.QUOTE.REFDATA_KNOWN: expected Violation for instrument absent from def set, got none")
	}
}

// TestTOBRefdataKnownTradeUnknownInstrument: same rule applies to Trade messages.
// Once refdata is ready, a Trade for an absent instrument must fire Violation.
func TestTOBRefdataKnownTradeUnknownInstrument(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const knownInstr = uint32(42)
	const unknownInstr = uint32(777)

	reachedReadyTOB(e, ch, knownInstr, 100)
	clearFindings(ac)

	raw := buildTradeFrame(wire.MagicTOB, ch, unknownInstr)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	if !hasViolation(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		t.Error("TOB.QUOTE.REFDATA_KNOWN: expected Violation for Trade with absent instrument, got none")
	}
}

// TestTOBRefdataKnownTradeKnownInstrument: Trade for a known instrument → silent.
func TestTOBRefdataKnownTradeKnownInstrument(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	reachedReadyTOB(e, ch, instrID, 100)
	clearFindings(ac)

	raw := buildTradeFrame(wire.MagicTOB, ch, instrID)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "TOB.QUOTE.REFDATA_KNOWN") {
		if fn.Status == core.Violation {
			t.Errorf("TOB.QUOTE.REFDATA_KNOWN: must NOT fire Violation for known Trade instrument, got %v", fn.Status)
		}
	}
}

// TestTOBRefdataKnownNoTier1Duplication: checkTOB must NOT re-emit any Tier-1
// finding that checkTier1TOB already covers (no duplicate findings for structural
// rules like TOB.QUOTE.STRUCT_LEN_TYPE).
func TestTOBRefdataKnownNoTier1Duplication(t *testing.T) {
	e, ac := newTOBEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	reachedReadyTOB(e, ch, instrID, 100)
	clearFindings(ac)

	// Send a normal Quote for the known instrument.
	raw := buildQuoteFrame(wire.MagicTOB, ch, instrID)
	f, sf := wire.Decode(raw, wire.MagicTOB)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	// Tier-1 structural rules must appear at most once per message.
	tier1Rules := []string{
		"TOB.QUOTE.STRUCT_LEN_TYPE",
		"TOB.QUOTE.GONE_VS_ZERO_PRICE",
		"TOB.QUOTE.CROSSED_LOCKED",
		"TOB.QUOTE.UPDATE_FLAGS_COHERENCE",
		"TOB.QUOTE.SOURCE_ID_REGISTRY",
	}
	for _, ruleID := range tier1Rules {
		fs := findingsFor(ac, ruleID)
		if len(fs) > 1 {
			t.Errorf("%s: emitted %d times (duplicate finding from checkTOB)", ruleID, len(fs))
		}
	}
}
