package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// fires decodes raw on the given feed/magic/port, runs it through the engine, and
// reports whether ruleID appears among the findings with Status==Violation (or, for
// Info-severity rules, with any status — Info rules are observability signals).
func fires(t *testing.T, feed core.Feed, magic uint16, raw []byte, port core.Port, ruleID string) bool {
	t.Helper()
	f, sf := wire.Decode(raw, magic)
	ac := &allCapture{}
	e := New(Config{Feed: feed, SourceRegistry: stubRegistry{}}, ac)
	e.Process(f, port, sf)
	e.Flush() // drain reorder buffer before inspecting findings
	meta, ok := core.Lookup(ruleID)
	if !ok {
		t.Fatalf("unknown rule %s", ruleID)
	}
	for _, fn := range ac.findings {
		if fn.RuleID != ruleID {
			continue
		}
		if meta.Severity == core.Info {
			// Info rules: any finding (Pass counts as "fired" for the observable
			// side-effect; for the "bad" vector we just want the rule to appear).
			return true
		}
		if fn.Status == core.Violation {
			return true
		}
	}
	return false
}

// allCapture collects every finding.
type allCapture struct {
	findings []core.Finding
}

func (a *allCapture) Record(f core.Finding)                 { a.findings = append(a.findings, f) }
func (a *allCapture) TransportLoss(core.Port)               {}
func (a *allCapture) TransportCorruption(core.Port, string) {}
func (a *allCapture) SnapshotAudit(string)                  {}
func (a *allCapture) SetInstrumentState(string, int)        {}

// stubRegistry allows source IDs 1–1023 (production range), denies 0 and
// anything >= 1024 that is not in the private range check.
type stubRegistry struct{}

func (stubRegistry) Allowed(id uint16) bool { return id >= 1 && id <= 1023 }

// --- body builder helpers ---

// orderAddBody builds a conformant-by-default OrderAdd body.
// side: 0=bid,1=ask; orderFlags: 0 = conformant; qty > 0 for conformant.
func orderAddBody(side, orderFlags uint8, qty uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // InstrumentID (body off 0)
		b.U16(1)             // SourceID (body off 4)
		b.U8(side)           // Side (body off 6)
		b.U8(orderFlags)     // OrderFlags (body off 7)
		b.U32(1)             // PerInstrumentSeq (body off 8)
		b.U64(42)            // OrderID (body off 12)
		b.U64(1_000_000_000) // EnterTimestamp (body off 20)
		b.I64(100)           // Price (body off 28)
		b.U64(qty)           // Quantity (body off 36)
		b.Pad(4)             // Reserved (body off 44) → total body 48 bytes → msg 52
	}
}

// orderExecuteBody builds a conformant-by-default OrderExecute body.
func orderExecuteBody(aggressorSide, execFlags uint8) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // InstrumentID (body off 0)
		b.U16(1)             // SourceID (body off 4)
		b.U8(aggressorSide)  // AggressorSide (body off 6)
		b.U8(execFlags)      // ExecFlags (body off 7)
		b.U32(1)             // PerInstrumentSeq (body off 8)
		b.U64(42)            // OrderID (body off 12)
		b.U64(0)             // TradeID (body off 20)
		b.U64(1_000_000_000) // Timestamp (body off 28)
		b.I64(100)           // ExecPrice (body off 36)
		b.U64(10)            // ExecQuantity (body off 44)
		// total body 52 bytes → msg 56
	}
}

// instrResetBody builds an InstrumentReset body.
// newAnchorSeq should equal the frame's seq for a conformant message.
func instrResetBody(newAnchorSeq uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // InstrumentID (body off 0)
		b.U8(0)              // Reason (body off 4)
		b.Pad(3)             // Reserved (body off 5)
		b.U64(newAnchorSeq)  // NewAnchorSeq (body off 8)
		b.U64(1_000_000_000) // Timestamp (body off 16)
		// total body 24 bytes → msg 28
	}
}

// snapshotOrderBody builds a conformant-by-default SnapshotOrder body.
func snapshotOrderBody(side, orderFlags uint8, qty uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // SnapshotID (body off 0)
		b.U64(42)            // OrderID (body off 4)
		b.U8(side)           // Side (body off 12)
		b.U8(orderFlags)     // OrderFlags (body off 13)
		b.Pad(2)             // Reserved (body off 14)
		b.U64(1_000_000_000) // EnterTimestamp (body off 16)
		b.I64(100)           // Price (body off 24)
		b.U64(qty)           // Quantity (body off 32)
		// total body 40 bytes → msg 44
	}
}

// heartbeatBody builds a Heartbeat body.
func heartbeatBody(channelID uint8) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U8(channelID)      // ChannelID (body off 0)
		b.Pad(3)             // Reserved (body off 1)
		b.U64(1_000_000_000) // Timestamp (body off 4)
		// total body 12 bytes → msg 16
	}
}

// quoteBody builds a conformant TOB Quote body.
// updateFlags: bit0=bidUpd, bit1=askUpd, bit2=bidGone, bit3=askGone
// bidGone/askGone flags => use 0 price and 0 qty for those sides.
func quoteBody(sourceID uint16, updateFlags uint8, bidPrice, askPrice int64, bidQty, askQty uint64, bidSrc, askSrc uint16) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // InstrumentID (body off 0)
		b.U16(sourceID)      // SourceID (body off 4)
		b.U8(updateFlags)    // UpdateFlags (body off 6)
		b.U8(0)              // Reserved (body off 7)
		b.U64(1_000_000_000) // SourceTimestamp (body off 8)
		b.I64(bidPrice)      // BidPrice (body off 16)
		b.U64(bidQty)        // BidQty (body off 24)
		b.I64(askPrice)      // AskPrice (body off 32)
		b.U64(askQty)        // AskQty (body off 40)
		b.U16(bidSrc)        // BidSourceCount (body off 48)
		b.U16(askSrc)        // AskSourceCount (body off 50)
		b.Pad(4)             // Reserved (body off 52) → total body 56 → msg 60
	}
}

// tradeBody builds a conformant Trade body.
func tradeBody(aggressorSide uint8, price int64, qty uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)             // InstrumentID
		b.U16(1)             // SourceID
		b.U8(aggressorSide)  // AggressorSide
		b.U8(0)              // TradeFlags
		b.U64(1_000_000_000) // SourceTimestamp
		b.I64(price)         // TradePrice
		b.U64(qty)           // TradeQuantity
		b.U64(0)             // TradeID
		b.U64(0)             // CumulativeVolume
		// total body 48 bytes → msg 52
	}
}

// midpointBody builds a conformant Midpoint body.
func midpointBody(method, qualityFlags uint8, bookTS, computeTS uint64, midPrice int64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(1)           // InstrumentID
		b.U16(1)           // SourceID
		b.U8(method)       // Method
		b.U8(qualityFlags) // QualityFlags
		b.U64(bookTS)      // BookTimestamp
		b.U64(computeTS)   // ComputeTimestamp
		b.I64(midPrice)    // MidPrice
		b.Pad(4)           // Reserved → total body 36 → msg 40
	}
}

// tier1Cases is separated out so TestEveryTier1RuleHasCase can reference it.
func tier1Cases() []struct {
	rule  string
	feed  core.Feed
	magic uint16
	port  core.Port
	bad   []byte
	good  []byte
} {
	// Conformant frame seq for InstrumentReset tests.
	const frameSeq = uint64(10)

	return []struct {
		rule  string
		feed  core.Feed
		magic uint16
		port  core.Port
		bad   []byte
		good  []byte
	}{
		// ---- Decoder-envelope rules: verify they propagate through Process/Emit ----
		{
			rule:  "FRAME.MAGIC_MISMATCH",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// Send an MBO magic but tell the decoder to expect TOB.
			bad:  wb.Frame(wire.MagicMBO).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "FRAME.SCHEMA_VERSION",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			bad:   wb.Frame(wire.MagicTOB).Schema(2).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
			good:  wb.Frame(wire.MagicTOB).Schema(1).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "FRAME.MSG_COUNT_RANGE",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			bad:   wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).ForgeCount(0).Bytes(),
			good:  wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "FRAME.LENGTH_CONSISTENCY",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// Forge the FrameLength to be too large.
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).ForgeLength(9999).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		// ---- MSG rules ----
		{
			rule:  "MSG.LENGTH_PER_TYPE",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// Declare wrong length for OrderAdd (should be 52, use 40).
			bad:  wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 40, orderAddBody(0, 0, 10)).Bytes(),
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0, 10)).Bytes(),
		},
		{
			rule:  "MSG.WRONG_PORT_PLACEMENT",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData, // fires checks on mktdata port
			// SnapshotBegin belongs on snapshot port, not mktdata.
			bad: wb.Frame(wire.MagicMBO).Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) { b.Pad(32) }).Bytes(),
			// Heartbeat is valid on mktdata.
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "MSG.UNKNOWN_TYPE_SKIPPED",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// Type 0xFF is unknown in TOB.
			bad:  wb.Frame(wire.MagicTOB).Msg(0xFF, 8, func(b *wb.Body) { b.Pad(4) }).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "MSG.RESERVED_TYPE_0X03_0X05",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// Type 0x03 is reserved in MBO.
			bad:  wb.Frame(wire.MagicMBO).Msg(0x03, 8, func(b *wb.Body) { b.Pad(4) }).Bytes(),
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "MSG.RESERVED_TYPE_0X03_0X05",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// Type 0x05 is also reserved in MBO (the reserved check must cover both,
			// and must take precedence over MSG.UNKNOWN_TYPE_SKIPPED).
			bad:  wb.Frame(wire.MagicMBO).Msg(0x05, 8, func(b *wb.Body) { b.Pad(4) }).Bytes(),
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
		},
		{
			rule:  "MSG.WRONG_PORT_PLACEMENT",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortSnapshot, // run checks on the snapshot port
			// Heartbeat belongs on mktdata, not snapshot → wrong-port on a 2nd port.
			bad: wb.Frame(wire.MagicMBO).Msg(wire.TypeHeartbeat, 16, heartbeatBody(0)).Bytes(),
			// SnapshotBegin is valid on the snapshot port.
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) { b.Pad(32) }).Bytes(),
		},
		// ---- Reserved bits ----
		{
			rule:  "RESERVED.FIELD_BITS_ZERO",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// OrderAdd with bits 5-7 of order_flags set (0xE0).
			bad:  wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0xE0, 10)).Bytes(),
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0, 10)).Bytes(),
		},
		// ---- Heartbeat ----
		{
			rule:  "HEARTBEAT.CHANNEL_ID_MATCH",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// Frame channel=0, heartbeat channel=5 → mismatch.
			bad:  wb.Frame(wire.MagicMBO).Channel(0).Msg(wire.TypeHeartbeat, 16, heartbeatBody(5)).Bytes(),
			good: wb.Frame(wire.MagicMBO).Channel(3).Msg(wire.TypeHeartbeat, 16, heartbeatBody(3)).Bytes(),
		},
		// ---- Field enums (MBO) ----
		{
			rule:  "FIELD.SIDE_ENUM",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			bad:   wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(2 /*bad side*/, 0, 10)).Bytes(),
			good:  wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0, 10)).Bytes(),
		},
		{
			rule:  "FIELD.AGGRESSOR_SIDE_ENUM",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			bad:   wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderExecute, 56, orderExecuteBody(5 /*bad agg side*/, 0)).Bytes(),
			good:  wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderExecute, 56, orderExecuteBody(1, 0)).Bytes(),
		},
		{
			rule:  "FIELD.QTY_POSITIVE",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			bad:   wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0, 0 /*qty=0*/)).Bytes(),
			good:  wb.Frame(wire.MagicMBO).Msg(wire.TypeOrderAdd, 52, orderAddBody(0, 0, 10)).Bytes(),
		},
		// ---- InstrumentReset anchor ----
		{
			rule:  "RESET.ANCHOR_SEQ_IS_CURRENT_FRAME",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortMktData,
			// Frame seq=10, anchor=99 → violation.
			bad:  wb.Frame(wire.MagicMBO).Seq(frameSeq).Msg(wire.TypeInstrReset, 28, instrResetBody(99)).Bytes(),
			good: wb.Frame(wire.MagicMBO).Seq(frameSeq).Msg(wire.TypeInstrReset, 28, instrResetBody(frameSeq)).Bytes(),
		},
		// ---- Snapshot order ----
		{
			rule:  "SNAP.ORDER_STRUCT_VALID",
			feed:  core.FeedMBO,
			magic: wire.MagicMBO,
			port:  core.PortSnapshot,
			// Bad: side=2 (invalid), qty=0 handled separately; use side=2 here.
			bad:  wb.Frame(wire.MagicMBO).Msg(wire.TypeSnapshotOrder, 44, snapshotOrderBody(2 /*bad side*/, 0, 10)).Bytes(),
			good: wb.Frame(wire.MagicMBO).Msg(wire.TypeSnapshotOrder, 44, snapshotOrderBody(0, 0, 10)).Bytes(),
		},
		// ---- TOB Quote rules ----
		{
			rule:  "TOB.QUOTE.STRUCT_LEN_TYPE",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// Wrong length for Quote (should be 60).
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 40, func(b *wb.Body) { b.Pad(36) }).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 1, 1)).Bytes(),
		},
		{
			rule:  "TOB.QUOTE.GONE_VS_ZERO_PRICE",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// bid_gone set (bit2) but bid_price != 0 → violation.
			// updateFlags=0x07 (bid_upd|ask_upd|bid_gone), but bid_price=100 (should be 0).
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x07, 100 /*bid non-zero*/, 101, 0, 10, 1, 1)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x07, 0 /*bid=0 for gone*/, 101, 0, 10, 1, 1)).Bytes(),
		},
		{
			rule:  "TOB.QUOTE.CROSSED_LOCKED",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// bid >= ask (crossed): bid=200, ask=100.
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 200, 100, 10, 10, 1, 1)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 200, 10, 10, 1, 1)).Bytes(),
		},
		{
			rule:  "TOB.QUOTE.UPDATE_FLAGS_COHERENCE",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// update_flags=0 → no side marked updated or gone.
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x00, 100, 101, 10, 10, 1, 1)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 1, 1)).Bytes(),
		},
		{
			rule:  "TOB.QUOTE.SOURCE_ID_REGISTRY",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// source_id=0 is always invalid.
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(0 /*invalid*/, 0x03, 100, 101, 10, 10, 1, 1)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 1, 1)).Bytes(),
		},
		{
			rule:  "TOB.QUOTE.SOURCE_COUNT",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// bid source_count=0 on a live bid → info finding (Pass status).
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 0 /*bidSrc=0*/, 1)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeQuote, 60, quoteBody(1, 0x03, 100, 101, 10, 10, 1, 1)).Bytes(),
		},
		// ---- TOB Trade ----
		{
			rule:  "TOB.TRADE.FIELDS",
			feed:  core.FeedTOB,
			magic: wire.MagicTOB,
			port:  core.PortMktData,
			// aggressor_side=5 → out of range.
			bad:  wb.Frame(wire.MagicTOB).Msg(wire.TypeTrade, 52, tradeBody(5 /*bad agg*/, 100, 10)).Bytes(),
			good: wb.Frame(wire.MagicTOB).Msg(wire.TypeTrade, 52, tradeBody(1, 100, 10)).Bytes(),
		},
		// ---- Midpoint rules ----
		{
			rule:  "MID.STRUCT_LEN_TYPE",
			feed:  core.FeedMidpoint,
			magic: wire.MagicMid,
			port:  core.PortMktData,
			// Wrong length for Midpoint (should be 40).
			bad:  wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 20, func(b *wb.Body) { b.Pad(16) }).Bytes(),
			good: wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0, 1000, 2000, 500)).Bytes(),
		},
		{
			rule:  "MID.METHOD_RANGE",
			feed:  core.FeedMidpoint,
			magic: wire.MagicMid,
			port:  core.PortMktData,
			// method=50 is out of defined range (5–254 are unknown non-custom).
			bad:  wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(50 /*unknown method*/, 0, 1000, 2000, 500)).Bytes(),
			good: wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0, 1000, 2000, 500)).Bytes(),
		},
		{
			rule:  "MID.QUALITY_FLAGS",
			feed:  core.FeedMidpoint,
			magic: wire.MagicMid,
			port:  core.PortMktData,
			// quality_flags bits 4-7 set (0xF0).
			bad:  wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0xF0 /*reserved bits set*/, 1000, 2000, 500)).Bytes(),
			good: wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0x03 /*only defined bits*/, 1000, 2000, 500)).Bytes(),
		},
		{
			rule:  "MID.TIMESTAMP_ORDERING",
			feed:  core.FeedMidpoint,
			magic: wire.MagicMid,
			port:  core.PortMktData,
			// compute_ts < book_ts → violation.
			bad:  wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0, 2000 /*book*/, 1000 /*compute < book*/, 500)).Bytes(),
			good: wb.Frame(wire.MagicMid).Msg(wire.TypeMidpoint, 40, midpointBody(1, 0, 1000, 2000, 500)).Bytes(),
		},
	}
}

// TestTier1Rules is the main table-driven test: each case must fire on `bad`
// and remain silent on `good`.
func TestTier1Rules(t *testing.T) {
	for _, c := range tier1Cases() {
		t.Run(c.rule, func(t *testing.T) {
			if !fires(t, c.feed, c.magic, c.bad, c.port, c.rule) {
				t.Errorf("%s did not fire on violating frame", c.rule)
			}
			if fires(t, c.feed, c.magic, c.good, c.port, c.rule) {
				t.Errorf("%s fired on conformant frame", c.rule)
			}
		})
	}
}

// TestEveryTier1RuleHasCase asserts that every Tier-1 rule in the registry has
// at least one test case in the table above.
func TestEveryTier1RuleHasCase(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range tier1Cases() {
		covered[c.rule] = true
	}
	for _, r := range core.Rules {
		if r.Tier == 1 && !covered[r.ID] {
			t.Errorf("Tier-1 rule %s has no test case", r.ID)
		}
	}
}
