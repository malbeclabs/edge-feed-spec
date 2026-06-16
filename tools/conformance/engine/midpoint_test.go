package engine

// midpoint_test.go — Tests for the Midpoint feed validator (Task 17).
//
// Covers:
//   - MID.METHOD0_REQUIRES_DEFAULT: fires for Method=0 when instrument's Default
//     Method is also 0; silent when Method!=0; NA before refdata is ready.
//   - MID.PRICE_BOUND: fires for negative Mid Price when Price Bound==1 or 2;
//     silent when Price Bound==0; NA before refdata is ready.
//
// Uses the same Engine-level helpers (allCapture, findingsFor, etc.) defined in
// refdata_test.go and the wirebuild package for frame construction.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- helpers ---

// newMidEngine creates a Midpoint-feed engine with ReorderWindow=1.
func newMidEngine() (*Engine, *allCapture) {
	ac := &allCapture{}
	cfg := Config{
		Feed:          core.FeedMidpoint,
		ReorderWindow: 1,
	}
	return New(cfg, ac), ac
}

// instrDefMidBodyWithMeta builds a Midpoint InstrumentDefinition body (64-byte msg,
// 60-byte body) with explicit Default Method and Price Bound fields.
//
// Midpoint InstrumentDefinition body layout (60 bytes):
//
//	Body[0:4]   = Instrument ID (u32 LE)
//	Body[4:38]  = other fields (opaque — zeroed)
//	Body[38]    = Default Method (u8)        ← spec offset 42
//	Body[39]    = Price Bound   (u8)         ← spec offset 43
//	Body[40:56] = other fields (opaque — zeroed)
//	Body[56:58] = Manifest Seq (u16 LE)      ← spec offset 60
//	Body[58:60] = Reserved (zeroed)
func instrDefMidBodyWithMeta(instrID uint32, manifestSeq uint16, defaultMethod, priceBound uint8) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID)      // Body[0:4]   Instrument ID
		b.Pad(34)           // Body[4:38]  opaque fields
		b.U8(defaultMethod) // Body[38]    Default Method
		b.U8(priceBound)    // Body[39]    Price Bound
		b.Pad(16)           // Body[40:56] opaque fields
		b.U16(manifestSeq)  // Body[56:58] Manifest Seq
		b.Pad(2)            // Body[58:60] Reserved → total 60 bytes body → 64-byte msg
	}
}

// reachedReadyMid bootstraps the Midpoint feed refdata for channel ch,
// registering instrument instrID with the given defaultMethod and priceBound.
// Returns the next frame sequence number to use.
//
// Sequence: Manifest(valid=1,seq=1,count=1) → InstrDef(instrID,seq=1,defMethod,pb)
// → Manifest(valid=1,seq=1,count=1) to close the cycle.
func reachedReadyMid(e *Engine, ch uint8, instrID uint32, defaultMethod, priceBound uint8, startSeq uint64) uint64 {
	seq := startSeq

	// ManifestSummary: valid=1, manifestSeq=1, count=1.
	rawMf := wb.Frame(wire.MagicMid).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	fMf, sfMf := wire.Decode(rawMf, wire.MagicMid)
	fMf.Header.Sequence = seq
	e.Process(fMf, core.PortRefData, sfMf)
	seq++

	// InstrumentDefinition with metadata.
	rawDef := wb.Frame(wire.MagicMid).
		Channel(ch).
		Msg(wire.TypeInstrumentDef, 64, instrDefMidBodyWithMeta(instrID, 1, defaultMethod, priceBound)).
		Bytes()
	fDef, sfDef := wire.Decode(rawDef, wire.MagicMid)
	fDef.Header.Sequence = seq
	e.Process(fDef, core.PortRefData, sfDef)
	seq++

	// Second ManifestSummary to close the cycle.
	rawMf2 := wb.Frame(wire.MagicMid).
		Channel(ch).
		Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
		Bytes()
	fMf2, sfMf2 := wire.Decode(rawMf2, wire.MagicMid)
	fMf2.Header.Sequence = seq
	e.Process(fMf2, core.PortRefData, sfMf2)
	seq++

	return seq
}

// buildMidpointFrame builds a minimal conformant Midpoint (0x03) frame.
// method: 0 = use instrument default; 1–4 specific methods.
// midPrice: raw i64 Mid Price (Body[24]).
func buildMidpointFrame(ch uint8, instrID uint32, method uint8, midPrice int64) []byte {
	return wb.Frame(wire.MagicMid).
		Channel(ch).
		Msg(wire.TypeMidpoint, 40, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID  Body[0:4]
			b.Pad(2)             // Reserved      Body[4:6]
			b.U8(method)         // Method        Body[6]
			b.U8(0)              // QualityFlags  Body[7]
			b.U64(1_000_000_000) // BookTS        Body[8:16]
			b.U64(1_000_000_001) // ComputeTS     Body[16:24]  (>= BookTS → pass MID.TIMESTAMP_ORDERING)
			b.I64(midPrice)      // MidPrice      Body[24:32]
			b.Pad(4)             // Reserved      Body[32:36]  → total body 36 → msg 40
		}).
		Bytes()
}

// --- MID.METHOD0_REQUIRES_DEFAULT tests ---

// TestMidMethod0RequiresDefaultBeforeReady: before refdata is ready, both rules
// must emit NA, never Violation.
func TestMidMethod0RequiresDefaultBeforeReady(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Send a Midpoint with Method=0 but NO refdata at all (cold start).
	raw := buildMidpointFrame(ch, instrID, 0, 500)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.METHOD0_REQUIRES_DEFAULT") {
		if fn.Status == core.Violation {
			t.Errorf("MID.METHOD0_REQUIRES_DEFAULT: must NOT be Violation before refdata ready; got %v", fn.Status)
		}
	}
	for _, fn := range findingsFor(ac, "MID.PRICE_BOUND") {
		if fn.Status == core.Violation {
			t.Errorf("MID.PRICE_BOUND: must NOT be Violation before refdata ready; got %v", fn.Status)
		}
	}
}

// TestMidMethod0RequiresDefaultFires: Method=0 with Default Method=0 → Violation.
func TestMidMethod0RequiresDefaultFires(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Bootstrap: instrument has Default Method=0 (no default defined), Price Bound=0.
	reachedReadyMid(e, ch, instrID, 0 /*defaultMethod=0*/, 0 /*priceBound=0*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 0 /*method=0*/, 500)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	if !hasViolation(ac, "MID.METHOD0_REQUIRES_DEFAULT") {
		t.Error("MID.METHOD0_REQUIRES_DEFAULT: expected Violation for Method=0 with Default Method=0, got none")
	}
}

// TestMidMethod0RequiresDefaultSilentWhenDefaultSet: Method=0 with non-zero
// Default Method → no Violation.
func TestMidMethod0RequiresDefaultSilentWhenDefaultSet(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Bootstrap: instrument has Default Method=1 (VWAP or similar), Price Bound=0.
	reachedReadyMid(e, ch, instrID, 1 /*defaultMethod=1*/, 0 /*priceBound=0*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 0 /*method=0: use instrument default*/, 500)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.METHOD0_REQUIRES_DEFAULT") {
		if fn.Status == core.Violation {
			t.Errorf("MID.METHOD0_REQUIRES_DEFAULT: must NOT fire for Method=0 when Default Method=1; got %v", fn.Status)
		}
	}
}

// TestMidMethod0RequiresDefaultNotApplicableNonZeroMethod: when Method != 0, the
// rule must not fire regardless of Default Method.
func TestMidMethod0RequiresDefaultNotApplicableNonZeroMethod(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// Bootstrap: Default Method=0, but Method in the Midpoint message is 1 (not 0).
	reachedReadyMid(e, ch, instrID, 0 /*defaultMethod=0*/, 0 /*priceBound=0*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 1 /*method=1: not 0*/, 500)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.METHOD0_REQUIRES_DEFAULT") {
		if fn.Status == core.Violation {
			t.Errorf("MID.METHOD0_REQUIRES_DEFAULT: must NOT fire for Method=1 (non-zero); got %v", fn.Status)
		}
	}
}

// --- MID.PRICE_BOUND tests ---

// TestMidPriceBound1NegativePrice: Price Bound=1 and negative raw price → Violation.
func TestMidPriceBound1NegativePrice(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(99)

	// Bootstrap: Default Method=1 (non-zero, so METHOD0_REQUIRES_DEFAULT is NA),
	// Price Bound=1 (bounded [0,1]).
	reachedReadyMid(e, ch, instrID, 1 /*defaultMethod*/, 1 /*priceBound=1*/, 100)
	clearFindings(ac)

	// Negative price → Violation.
	raw := buildMidpointFrame(ch, instrID, 1 /*method=1*/, -1 /*midPrice negative*/)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	if !hasViolation(ac, "MID.PRICE_BOUND") {
		t.Error("MID.PRICE_BOUND: expected Violation for negative price with Bound=1, got none")
	}
}

// TestMidPriceBound1NonNegativePrice: Price Bound=1 and non-negative price → silent.
func TestMidPriceBound1NonNegativePrice(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(99)

	reachedReadyMid(e, ch, instrID, 1, 1 /*priceBound=1*/, 100)
	clearFindings(ac)

	// Price=500 (positive) → no violation.
	raw := buildMidpointFrame(ch, instrID, 1, 500)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.PRICE_BOUND") {
		if fn.Status == core.Violation {
			t.Errorf("MID.PRICE_BOUND: must NOT fire Violation for non-negative price with Bound=1; got %v", fn.Status)
		}
	}
}

// TestMidPriceBound2NegativePrice: Price Bound=2 (non-negative) and negative price → Violation.
func TestMidPriceBound2NegativePrice(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(77)

	reachedReadyMid(e, ch, instrID, 1, 2 /*priceBound=2*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 1, -999)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	if !hasViolation(ac, "MID.PRICE_BOUND") {
		t.Error("MID.PRICE_BOUND: expected Violation for negative price with Bound=2, got none")
	}
}

// TestMidPriceBound2PositivePrice: Price Bound=2, positive price → silent.
func TestMidPriceBound2PositivePrice(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(77)

	reachedReadyMid(e, ch, instrID, 1, 2 /*priceBound=2*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 1, 12345)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.PRICE_BOUND") {
		if fn.Status == core.Violation {
			t.Errorf("MID.PRICE_BOUND: must NOT fire for positive price with Bound=2; got %v", fn.Status)
		}
	}
}

// TestMidPriceBound0NoCheck: Price Bound=0 → no price-bound check regardless of price.
func TestMidPriceBound0NoCheck(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(11)

	reachedReadyMid(e, ch, instrID, 1, 0 /*priceBound=0: no constraint*/, 100)
	clearFindings(ac)

	raw := buildMidpointFrame(ch, instrID, 1, -9999)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.PRICE_BOUND") {
		if fn.Status == core.Violation {
			t.Errorf("MID.PRICE_BOUND: must NOT fire for any price when Bound=0; got %v", fn.Status)
		}
	}
}

// TestMidPriceBoundBeforeReady: both rules must be NA before refdata is ready.
func TestMidPriceBoundBeforeReady(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	// No refdata at all.
	raw := buildMidpointFrame(ch, instrID, 0, -1)
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	for _, fn := range findingsFor(ac, "MID.PRICE_BOUND") {
		if fn.Status == core.Violation {
			t.Errorf("MID.PRICE_BOUND: must NOT be Violation before refdata ready; got %v", fn.Status)
		}
	}
}

// TestMidNoTier1Duplication: checkMidpoint must NOT re-emit any Tier-1 finding
// that checkTier1Mid already covers.  This test deliberately sends a Midpoint
// that violates MID.QUALITY_FLAGS (reserved bits 4-7 set) so that checkTier1Mid
// emits exactly one finding.  If checkMidpoint also fired the same rule, we'd
// see two findings.
func TestMidNoTier1Duplication(t *testing.T) {
	e, ac := newMidEngine()
	const ch = uint8(1)
	const instrID = uint32(42)

	reachedReadyMid(e, ch, instrID, 1, 0, 100)
	clearFindings(ac)

	// Build a Midpoint with QualityFlags having reserved bits 4-7 set (0xF0).
	// This causes checkTier1Mid to emit exactly one MID.QUALITY_FLAGS finding.
	raw := wb.Frame(wire.MagicMid).
		Channel(ch).
		Msg(wire.TypeMidpoint, 40, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID  Body[0:4]
			b.Pad(2)             // Reserved      Body[4:6]
			b.U8(1)              // Method=1      Body[6]
			b.U8(0xF0)           // QualityFlags: reserved bits set → MID.QUALITY_FLAGS
			b.U64(1_000_000_000) // BookTS        Body[8:16]
			b.U64(1_000_000_001) // ComputeTS     Body[16:24]
			b.I64(500)           // MidPrice      Body[24:32]
			b.Pad(4)             // Reserved      Body[32:36]
		}).
		Bytes()
	f, sf := wire.Decode(raw, wire.MagicMid)
	f.Header.Sequence = 1
	e.Process(f, core.PortMktData, sf)
	e.Flush()

	// MID.QUALITY_FLAGS must have been emitted by Tier-1, not duplicated by checkMidpoint.
	qfFindings := findingsFor(ac, "MID.QUALITY_FLAGS")
	if len(qfFindings) == 0 {
		t.Error("MID.QUALITY_FLAGS: expected exactly one finding from Tier-1, got none")
	} else if len(qfFindings) > 1 {
		t.Errorf("MID.QUALITY_FLAGS: emitted %d times (checkMidpoint must not duplicate Tier-1)", len(qfFindings))
	}

	// Other Tier-1 rules must not have fired at all (message is otherwise valid).
	tier1Rules := []string{"MID.STRUCT_LEN_TYPE", "MID.METHOD_RANGE", "MID.TIMESTAMP_ORDERING"}
	for _, ruleID := range tier1Rules {
		if len(findingsFor(ac, ruleID)) > 0 {
			t.Errorf("%s: unexpected finding (message was otherwise valid)", ruleID)
		}
	}
}
