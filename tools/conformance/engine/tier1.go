package engine

// tier1.go — Tier-1 structural conformance checks.
//
// checkTier1 is called from Engine.Process after the decoder's StructFindings
// have been mapped.  It does NOT re-check what the decoder already flagged
// (FRAME.MAGIC_MISMATCH, FRAME.SCHEMA_VERSION, FRAME.MSG_COUNT_RANGE,
// FRAME.LENGTH_CONSISTENCY); those are already emitted by Process.
//
// Checks are split into per-message loops over f.Messages, each accessing
// fields via the helpers in fields.go.

import (
	"fmt"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// expectedMsgLen returns the canonical wire length (including the 4-byte
// header) for message types that have a fixed size in the current spec version.
// Returns 0 for types that are not defined in a given feed (caller must check).
func expectedMsgLen(feed core.Feed, typ uint8) uint8 {
	switch typ {
	case wire.TypeHeartbeat:
		return 16
	case wire.TypeInstrumentDef:
		if feed == core.FeedMidpoint {
			return 64
		}
		return 80
	case wire.TypeQuote: // 0x03: Quote (TOB) or Midpoint
		if feed == core.FeedTOB {
			return 60
		}
		if feed == core.FeedMidpoint {
			return 40
		}
		return 0 // MBO: 0x03 is reserved
	case wire.TypeTrade: // 0x04
		if feed == core.FeedMidpoint {
			return 0 // reserved in midpoint
		}
		return 52
	case wire.TypeEndOfSession:
		return 12
	case wire.TypeManifest:
		return 24
	case wire.TypeOrderAdd:
		return 52
	case wire.TypeOrderCancel:
		return 32
	case wire.TypeOrderExecute:
		return 56
	case wire.TypeBatchBoundary:
		return 16
	case wire.TypeInstrReset:
		return 28
	case wire.TypeSnapshotBegin:
		return 36
	case wire.TypeSnapshotOrder:
		return 44
	case wire.TypeSnapshotEnd:
		return 20
	}
	return 0
}

// knownTypes returns the set of type IDs that are defined (non-reserved) in
// this feed's spec.
func knownTypes(feed core.Feed) map[uint8]bool {
	switch feed {
	case core.FeedTOB:
		return map[uint8]bool{
			wire.TypeHeartbeat:     true,
			wire.TypeInstrumentDef: true,
			wire.TypeQuote:         true, // 0x03 = Quote
			wire.TypeTrade:         true,
			wire.TypeEndOfSession:  true,
			wire.TypeManifest:      true,
		}
	case core.FeedMidpoint:
		return map[uint8]bool{
			wire.TypeHeartbeat:     true,
			wire.TypeInstrumentDef: true,
			wire.TypeMidpoint:      true, // 0x03 = Midpoint
			wire.TypeEndOfSession:  true,
			wire.TypeManifest:      true,
		}
	case core.FeedMBO:
		return map[uint8]bool{
			wire.TypeHeartbeat:     true,
			wire.TypeInstrumentDef: true,
			// 0x03, 0x05 reserved
			wire.TypeTrade:         true,
			wire.TypeEndOfSession:  true,
			wire.TypeManifest:      true,
			wire.TypeOrderAdd:      true,
			wire.TypeOrderCancel:   true,
			wire.TypeOrderExecute:  true,
			wire.TypeBatchBoundary: true,
			wire.TypeInstrReset:    true,
			wire.TypeSnapshotBegin: true,
			wire.TypeSnapshotOrder: true,
			wire.TypeSnapshotEnd:   true,
		}
	}
	return nil
}

// portAllowed returns true if the message type is permitted on the given port
// for the given feed.
func portAllowed(feed core.Feed, typ uint8, port core.Port) bool {
	switch feed {
	case core.FeedTOB:
		// Two-port model: mktdata and refdata.
		switch port {
		case core.PortMktData:
			return typ == wire.TypeHeartbeat ||
				typ == wire.TypeQuote ||
				typ == wire.TypeTrade ||
				typ == wire.TypeEndOfSession
		case core.PortRefData:
			return typ == wire.TypeInstrumentDef ||
				typ == wire.TypeManifest
		}
	case core.FeedMidpoint:
		// Two-port model: mktdata and refdata.
		switch port {
		case core.PortMktData:
			return typ == wire.TypeHeartbeat ||
				typ == wire.TypeMidpoint ||
				typ == wire.TypeEndOfSession
		case core.PortRefData:
			return typ == wire.TypeInstrumentDef ||
				typ == wire.TypeManifest
		}
	case core.FeedMBO:
		// Three-port model: mktdata, refdata, snapshot.
		switch port {
		case core.PortMktData:
			return typ == wire.TypeHeartbeat ||
				typ == wire.TypeTrade ||
				typ == wire.TypeEndOfSession ||
				typ == wire.TypeOrderAdd ||
				typ == wire.TypeOrderCancel ||
				typ == wire.TypeOrderExecute ||
				typ == wire.TypeBatchBoundary ||
				typ == wire.TypeInstrReset
		case core.PortRefData:
			return typ == wire.TypeInstrumentDef ||
				typ == wire.TypeManifest
		case core.PortSnapshot:
			return typ == wire.TypeSnapshotBegin ||
				typ == wire.TypeSnapshotOrder ||
				typ == wire.TypeSnapshotEnd
		}
	}
	return false
}

// checkTier1 runs all Tier-1 structural checks that are NOT already handled by
// the decoder (StructFindings).  Called from Engine.Process.
func (e *Engine) checkTier1(f *wire.Frame, port core.Port) {
	known := knownTypes(e.cfg.Feed)
	ch := f.Header.ChannelID
	seq := f.Header.Sequence

	for _, m := range f.Messages {
		// MSG.RESERVED_TYPE_0X03_0X05 (MBO only): types 0x03 and 0x05 are explicitly
		// reserved in MBO. Check this BEFORE the generic unknown-type path, so a reserved
		// type is flagged as a reserved-type violation rather than swallowed as "unknown".
		if e.cfg.Feed == core.FeedMBO && (m.Type == 0x03 || m.Type == 0x05) {
			e.Emit("MSG.RESERVED_TYPE_0X03_0X05", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("reserved type 0x%02X in MBO frame", m.Type))
			continue
		}

		// MSG.UNKNOWN_TYPE_SKIPPED: type not in this feed's defined set (genuinely
		// unassigned/forward-compat). Skipped via Message Length, reported as info.
		if !known[m.Type] {
			e.Emit("MSG.UNKNOWN_TYPE_SKIPPED", core.Pass, port, seq, ch, 0,
				fmt.Sprintf("type 0x%02X skipped (unknown in feed %s)", m.Type, e.cfg.Feed))
			continue // skip further per-type checks on unknown messages
		}

		// MSG.LENGTH_PER_TYPE: message length must match the canonical fixed size.
		if exp := expectedMsgLen(e.cfg.Feed, m.Type); exp != 0 && m.Length != exp {
			e.Emit("MSG.LENGTH_PER_TYPE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("type 0x%02X: length %d, expected %d", m.Type, m.Length, exp))
		}

		// MSG.WRONG_PORT_PLACEMENT: known type on the wrong port.
		if !portAllowed(e.cfg.Feed, m.Type, port) {
			e.Emit("MSG.WRONG_PORT_PLACEMENT", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("type 0x%02X not permitted on %s port", m.Type, port))
		}

		// Per-type field checks.
		e.checkTier1Message(f, m, port, ch, seq)
	}
}

// checkTier1Message runs per-type field-level Tier-1 checks.
//
// Body-reading checks are gated on the message being canonical length: when the
// length is wrong, MSG.LENGTH_PER_TYPE already fired in checkTier1, and the field
// accessors would read zero-padded/garbage bytes and emit cascaded false findings.
// The length-based STRUCT_LEN_TYPE rules (Quote/Midpoint) are the exception — they
// fire precisely on a wrong length — and are handled inside the TOB/Mid sub-checks.
func (e *Engine) checkTier1Message(f *wire.Frame, m wire.Message, port core.Port, ch uint8, seq uint64) {
	exp := expectedMsgLen(e.cfg.Feed, m.Type)
	lengthOK := exp == 0 || m.Length == exp

	if lengthOK {
		switch m.Type {

		case wire.TypeHeartbeat:
			// HEARTBEAT.CHANNEL_ID_MATCH: heartbeat Channel ID must equal frame Channel ID.
			hbCh := heartbeatChannelID(m)
			if hbCh != f.Header.ChannelID {
				e.Emit("HEARTBEAT.CHANNEL_ID_MATCH", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("heartbeat channel_id %d != frame channel_id %d", hbCh, f.Header.ChannelID))
			}

		case wire.TypeOrderAdd:
			// FIELD.SIDE_ENUM: side must be 0 or 1.
			side := orderAddSide(m)
			if side > 1 {
				e.Emit("FIELD.SIDE_ENUM", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("OrderAdd side %d not in {0,1}", side))
			}
			// FIELD.QTY_POSITIVE: quantity must be > 0.
			qty := orderAddQuantity(m)
			if qty == 0 {
				e.Emit("FIELD.QTY_POSITIVE", core.Violation, port, seq, ch, 0, "OrderAdd quantity is 0")
			}
			// RESERVED.FIELD_BITS_ZERO: Order Flags bits 5–7 must be zero.
			flags := orderAddOrderFlags(m)
			if flags&0xE0 != 0 {
				e.Emit("RESERVED.FIELD_BITS_ZERO", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("OrderAdd order_flags reserved bits 5-7 non-zero: 0x%02X", flags))
			}

		case wire.TypeOrderExecute:
			// FIELD.AGGRESSOR_SIDE_ENUM: aggressor side must be 0, 1, or 2.
			agg := orderExecuteAggressorSide(m)
			if agg > 2 {
				e.Emit("FIELD.AGGRESSOR_SIDE_ENUM", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("OrderExecute aggressor_side %d not in {0,1,2}", agg))
			}
			// RESERVED.FIELD_BITS_ZERO: Exec Flags bits 2–7 must be zero.
			ef := orderExecuteExecFlags(m)
			if ef&0xFC != 0 {
				e.Emit("RESERVED.FIELD_BITS_ZERO", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("OrderExecute exec_flags reserved bits 2-7 non-zero: 0x%02X", ef))
			}

		case wire.TypeTrade:
			// FIELD.AGGRESSOR_SIDE_ENUM also covers the MBO Trade message (the TOB feed's
			// Trade is validated separately by TOB.TRADE.FIELDS in checkTier1TOB).
			if e.cfg.Feed == core.FeedMBO {
				agg := tradeAggressorSide(m)
				if agg > 2 {
					e.Emit("FIELD.AGGRESSOR_SIDE_ENUM", core.Violation, port, seq, ch, 0,
						fmt.Sprintf("Trade aggressor_side %d not in {0,1,2}", agg))
				}
			}

		case wire.TypeInstrReset:
			// RESET.ANCHOR_SEQ_IS_CURRENT_FRAME: new_anchor_seq must equal the
			// mktdata frame's Sequence Number (the reset takes effect immediately).
			anchorSeq := instrResetNewAnchorSeq(m)
			if anchorSeq != seq {
				e.Emit("RESET.ANCHOR_SEQ_IS_CURRENT_FRAME", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("InstrumentReset new_anchor_seq %d != frame seq %d", anchorSeq, seq))
			}

		case wire.TypeSnapshotOrder:
			// SNAP.ORDER_STRUCT_VALID: Side in {0,1}, reserved bits zero, qty > 0.
			side := snapshotOrderSide(m)
			if side > 1 {
				e.Emit("SNAP.ORDER_STRUCT_VALID", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("SnapshotOrder side %d not in {0,1}", side))
			}
			flags := snapshotOrderFlags(m)
			if flags&0xE0 != 0 {
				e.Emit("SNAP.ORDER_STRUCT_VALID", core.Violation, port, seq, ch, 0,
					fmt.Sprintf("SnapshotOrder order_flags reserved bits 5-7 non-zero: 0x%02X", flags))
			}
			qty := snapshotOrderQuantity(m)
			if qty == 0 {
				e.Emit("SNAP.ORDER_STRUCT_VALID", core.Violation, port, seq, ch, 0, "SnapshotOrder quantity is 0")
			}
		}
	}

	// TOB-specific checks (the STRUCT_LEN_TYPE rule fires even on a bad length).
	if e.cfg.Feed == core.FeedTOB {
		e.checkTier1TOB(m, port, ch, seq, lengthOK)
	}

	// Midpoint-specific checks (likewise).
	if e.cfg.Feed == core.FeedMidpoint {
		e.checkTier1Mid(m, port, ch, seq, lengthOK)
	}
}

// checkTier1TOB runs TOB-only Tier-1 checks (Quote and Trade). lengthOK is false
// when the message length is non-canonical; in that case the STRUCT_LEN_TYPE rule
// still fires but the body-reading semantic checks are skipped to avoid cascaded
// false findings from zero-padded reads.
func (e *Engine) checkTier1TOB(m wire.Message, port core.Port, ch uint8, seq uint64, lengthOK bool) {
	switch m.Type {
	case wire.TypeQuote: // 0x03 in TOB feed

		// TOB.QUOTE.STRUCT_LEN_TYPE: a Quote must be exactly 60 bytes.
		if m.Length != 60 {
			e.Emit("TOB.QUOTE.STRUCT_LEN_TYPE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("Quote length %d != 60", m.Length))
		}
		if !lengthOK {
			return // body would be zero-padded — skip semantic field checks
		}

		upd := quoteUpdateFlags(m)
		bidGone := upd&0x04 != 0
		askGone := upd&0x08 != 0
		bidUpd := upd&0x01 != 0
		askUpd := upd&0x02 != 0
		bidPrice := quoteBidPrice(m)
		askPrice := quoteAskPrice(m)
		bidQty := quoteBidQty(m)
		askQty := quoteAskQty(m)

		// TOB.QUOTE.GONE_VS_ZERO_PRICE: the spec requires only gone => price 0
		// (bit 2 bid-gone => bid_price 0; bit 3 ask-gone => ask_price 0). The reverse
		// (price 0 => gone) is NOT mandated by the spec, so it is not checked here.
		if bidGone && bidPrice != 0 {
			e.Emit("TOB.QUOTE.GONE_VS_ZERO_PRICE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("bid_gone set but bid_price=%d (must be 0)", bidPrice))
		}
		if askGone && askPrice != 0 {
			e.Emit("TOB.QUOTE.GONE_VS_ZERO_PRICE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("ask_gone set but ask_price=%d (must be 0)", askPrice))
		}

		// TOB.QUOTE.CROSSED_LOCKED: when both sides are present (neither gone), the
		// book must not be crossed/locked (bid >= ask). "Present" is the not-gone
		// flags, NOT price > 0 (a present side may carry a non-positive raw price).
		if !bidGone && !askGone && bidPrice >= askPrice {
			e.Emit("TOB.QUOTE.CROSSED_LOCKED", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("bid_price %d >= ask_price %d (crossed/locked)", bidPrice, askPrice))
		}

		// TOB.QUOTE.UPDATE_FLAGS_COHERENCE: at least one side must be flagged as
		// updated in the update flags (bits 0-3); a zero update_flags with no side
		// marked updated or gone is incoherent.
		if upd&0x0F == 0 {
			e.Emit("TOB.QUOTE.UPDATE_FLAGS_COHERENCE", core.Violation, port, seq, ch, 0,
				"update_flags bits 0-3 all zero: no side updated or gone")
		}
		// A bid_gone flag without bid_updated is incoherent (gone implies updated).
		if bidGone && !bidUpd {
			e.Emit("TOB.QUOTE.UPDATE_FLAGS_COHERENCE", core.Violation, port, seq, ch, 0,
				"bid_gone set but bid_updated not set")
		}
		if askGone && !askUpd {
			e.Emit("TOB.QUOTE.UPDATE_FLAGS_COHERENCE", core.Violation, port, seq, ch, 0,
				"ask_gone set but ask_updated not set")
		}
		// If a side is gone, qty must be 0 (no volume at a gone level).
		if bidGone && bidQty != 0 {
			e.Emit("TOB.QUOTE.UPDATE_FLAGS_COHERENCE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("bid_gone set but bid_qty=%d (must be 0)", bidQty))
		}
		if askGone && askQty != 0 {
			e.Emit("TOB.QUOTE.UPDATE_FLAGS_COHERENCE", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("ask_gone set but ask_qty=%d (must be 0)", askQty))
		}

		// TOB.QUOTE.SOURCE_ID_REGISTRY: source_id == 0 is always invalid (reserved
		// by spec).  Range check via SourceRegistry only when non-nil.
		srcID := quoteSourceID(m)
		if srcID == 0 {
			e.Emit("TOB.QUOTE.SOURCE_ID_REGISTRY", core.Violation, port, seq, ch, 0,
				"Quote source_id is 0 (reserved)")
		} else if e.cfg.SourceRegistry != nil && !e.cfg.SourceRegistry.Allowed(srcID) {
			e.Emit("TOB.QUOTE.SOURCE_ID_REGISTRY", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("Quote source_id %d not in registry", srcID))
		}

		// TOB.QUOTE.SOURCE_COUNT: bid/ask source counts are informational; a value of
		// 0 when the side is live (not gone) is noteworthy (info).
		bidCnt := quoteBidSourceCount(m)
		askCnt := quoteAskSourceCount(m)
		if !bidGone && bidPrice != 0 && bidCnt == 0 {
			e.Emit("TOB.QUOTE.SOURCE_COUNT", core.Pass, port, seq, ch, 0,
				"bid_source_count is 0 for a live bid (unavailable)")
		}
		if !askGone && askPrice != 0 && askCnt == 0 {
			e.Emit("TOB.QUOTE.SOURCE_COUNT", core.Pass, port, seq, ch, 0,
				"ask_source_count is 0 for a live ask (unavailable)")
		}

	case wire.TypeTrade: // 0x04
		if !lengthOK {
			return // length already flagged by MSG.LENGTH_PER_TYPE; skip body reads
		}
		// TOB.TRADE.FIELDS: Aggressor Side in {0,1,2}; Trade Flags use only bits 0-2
		// (block/sweep/cross) so bits 3-7 must be 0; Source ID must be non-zero and,
		// when a registry is configured, within it (0 is reserved for all price messages).
		agg := tradeAggressorSide(m)
		if agg > 2 {
			e.Emit("TOB.TRADE.FIELDS", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("Trade aggressor_side %d not in {0,1,2}", agg))
		}
		if tf := tradeFlags(m); tf&0xF8 != 0 {
			e.Emit("TOB.TRADE.FIELDS", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("Trade flags reserved bits 3-7 non-zero: 0x%02X", tf))
		}
		src := tradeSourceID(m)
		if src == 0 {
			e.Emit("TOB.TRADE.FIELDS", core.Violation, port, seq, ch, 0, "Trade source_id is 0 (reserved)")
		} else if e.cfg.SourceRegistry != nil && !e.cfg.SourceRegistry.Allowed(src) {
			e.Emit("TOB.TRADE.FIELDS", core.Violation, port, seq, ch, 0,
				fmt.Sprintf("Trade source_id %d not in registry", src))
		}
	}
}

// checkTier1Mid runs Midpoint-only Tier-1 checks. lengthOK is false on a
// non-canonical length; STRUCT_LEN_TYPE still fires but body reads are skipped.
func (e *Engine) checkTier1Mid(m wire.Message, port core.Port, ch uint8, seq uint64, lengthOK bool) {
	if m.Type != wire.TypeMidpoint { // 0x03 in Midpoint feed
		return
	}

	// MID.STRUCT_LEN_TYPE: canonical length is 40.
	if m.Length != 40 {
		e.Emit("MID.STRUCT_LEN_TYPE", core.Violation, port, seq, ch, 0,
			fmt.Sprintf("Midpoint length %d != 40", m.Length))
	}
	if !lengthOK {
		return // body would be zero-padded — skip semantic field checks
	}

	method := midpointMethod(m)
	// MID.METHOD_RANGE: defined values are 0–4 and 255; values 5–254 are
	// technically unknown but the spec says treat as Custom (255).  Emit info
	// for unknown non-zero, non-custom values.
	if method > 4 && method != 255 {
		e.Emit("MID.METHOD_RANGE", core.Pass, port, seq, ch, 0,
			fmt.Sprintf("Midpoint method %d outside defined range (treating as custom)", method))
	}

	// MID.QUALITY_FLAGS: bits 4–7 must be zero (reserved).
	qf := midpointQualityFlags(m)
	if qf&0xF0 != 0 {
		e.Emit("MID.QUALITY_FLAGS", core.Violation, port, seq, ch, 0,
			fmt.Sprintf("Midpoint quality_flags reserved bits 4-7 non-zero: 0x%02X", qf))
	}

	// MID.TIMESTAMP_ORDERING: compute_ts must be >= book_ts.
	bookTS := midpointBookTS(m)
	computeTS := midpointComputeTS(m)
	if computeTS < bookTS {
		e.Emit("MID.TIMESTAMP_ORDERING", core.Violation, port, seq, ch, 0,
			fmt.Sprintf("compute_ts %d < book_ts %d", computeTS, bookTS))
	}
}
