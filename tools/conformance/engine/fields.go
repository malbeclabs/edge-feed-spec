package engine

// fields.go — per-message field accessors at spec offsets.
//
// All offsets are relative to Message.Body (i.e. bytes after the 4-byte message
// header). The spec tables list offset-from-message-start; subtract 4 to get the
// Body offset used here.
//
// Notation: "spec offset N" means N bytes from the start of the message (header
// included); Body[N-4] is the same byte.

import (
	"encoding/binary"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// bodyU8 safely reads a uint8 from m.Body at the given body offset (0 = first
// byte after the 4-byte message header).
func bodyU8(m wire.Message, off int) uint8 {
	if off >= len(m.Body) {
		return 0
	}
	return m.Body[off]
}

func bodyU16LE(m wire.Message, off int) uint16 {
	if off+1 >= len(m.Body) {
		return 0
	}
	return binary.LittleEndian.Uint16(m.Body[off:])
}

func bodyU64LE(m wire.Message, off int) uint64 {
	if off+7 >= len(m.Body) {
		return 0
	}
	return binary.LittleEndian.Uint64(m.Body[off:])
}

// --- Heartbeat (0x01, 16 bytes total) ---
// spec offset 4 = Channel ID (u8); Body[0]
func heartbeatChannelID(m wire.Message) uint8 { return bodyU8(m, 0) }

// --- MBO delta shared fields (OrderAdd 0x10, OrderCancel 0x11, OrderExecute 0x12) ---
//
// These three delta types share the same layout for the two fields used by the
// per-instrument seq tracker:
//
//	spec offset  4 = Instrument ID (u32 LE); Body[0]
//	spec offset 12 = Per-Instrument Seq (u32 LE); Body[8]
func deltaInstrumentID(m wire.Message) uint32     { return bodyU32LE(m, 0) }
func deltaPerInstrumentSeq(m wire.Message) uint32 { return bodyU32LE(m, 8) }

// instrResetInstrumentID returns the Instrument ID from an InstrumentReset (0x14).
// spec offset 4 = Instrument ID (u32 LE); Body[0]
func instrResetInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }

// --- OrderAdd (0x10, 52 bytes total) ---
// spec offset  8 = Source ID (u16);          Body[4]
// spec offset 10 = Side (u8);                Body[6]
// spec offset 11 = Order Flags (u8);         Body[7]
// spec offset 16 = Order ID (u64);           Body[12]
// spec offset 24 = Enter Timestamp (u64);    Body[20]
// spec offset 32 = Price (i64);              Body[28]
// spec offset 40 = Quantity (u64);           Body[36]
func orderAddSourceID(m wire.Message) uint16       { return bodyU16LE(m, 4) }
func orderAddSide(m wire.Message) uint8            { return bodyU8(m, 6) }
func orderAddOrderFlags(m wire.Message) uint8      { return bodyU8(m, 7) }
func orderAddOrderID(m wire.Message) uint64        { return bodyU64LE(m, 12) }
func orderAddEnterTimestamp(m wire.Message) uint64 { return bodyU64LE(m, 20) }
func orderAddPrice(m wire.Message) int64           { return int64(bodyU64LE(m, 28)) }
func orderAddQuantity(m wire.Message) uint64       { return bodyU64LE(m, 36) }

// --- OrderCancel (0x11, 32 bytes total) ---
// spec offset  8 = Source ID (u16);    Body[4]
// spec offset 16 = Order ID (u64);     Body[12]
func orderCancelSourceID(m wire.Message) uint16 { return bodyU16LE(m, 4) }
func orderCancelOrderID(m wire.Message) uint64  { return bodyU64LE(m, 12) }

// --- OrderExecute (0x12, 56 bytes total) ---
// spec offset  8 = Source ID (u16);        Body[4]
// spec offset 10 = Aggressor Side (u8);    Body[6]
// spec offset 11 = Exec Flags (u8);        Body[7]
// spec offset 16 = Order ID (u64);         Body[12]
// spec offset 24 = Trade ID (u64);         Body[20]
// spec offset 40 = Exec Price (i64);       Body[36]
// spec offset 48 = Exec Quantity (u64);    Body[44]
func orderExecuteSourceID(m wire.Message) uint16     { return bodyU16LE(m, 4) }
func orderExecuteAggressorSide(m wire.Message) uint8 { return bodyU8(m, 6) }
func orderExecuteExecFlags(m wire.Message) uint8     { return bodyU8(m, 7) }
func orderExecuteOrderID(m wire.Message) uint64      { return bodyU64LE(m, 12) }
func orderExecuteTradeID(m wire.Message) uint64      { return bodyU64LE(m, 20) }
func orderExecuteExecPrice(m wire.Message) int64     { return int64(bodyU64LE(m, 36)) }
func orderExecuteExecQuantity(m wire.Message) uint64 { return bodyU64LE(m, 44) }

// --- InstrumentReset (0x14, 28 bytes total) ---
// spec offset 12 = New Anchor Seq (u64); Body[8]
func instrResetNewAnchorSeq(m wire.Message) uint64 { return bodyU64LE(m, 8) }

// --- BatchBoundary (0x13, 16 bytes total) ---
// spec offset 4 = Batch ID (u32 LE); Body[0]
func batchBoundaryBatchID(m wire.Message) uint32 { return bodyU32LE(m, 0) }

// --- SnapshotBegin (0x20, 36 bytes total) ---
// spec offset  4 = Instrument ID (u32 LE);          Body[0]
// spec offset  8 = Anchor Seq (u64 LE);             Body[4]
// spec offset 16 = Total Orders (u32 LE);           Body[12]
// spec offset 20 = Snapshot ID (u32 LE);            Body[16]
// spec offset 24 = Last Instrument Seq (u32 LE);    Body[20]
func snapshotBeginInstrumentID(m wire.Message) uint32      { return bodyU32LE(m, 0) }
func snapshotBeginAnchorSeq(m wire.Message) uint64         { return bodyU64LE(m, 4) }
func snapshotBeginTotalOrders(m wire.Message) uint32       { return bodyU32LE(m, 12) }
func snapshotBeginSnapshotID(m wire.Message) uint32        { return bodyU32LE(m, 16) }
func snapshotBeginLastInstrumentSeq(m wire.Message) uint32 { return bodyU32LE(m, 20) }

// --- SnapshotOrder (0x21, 44 bytes total) ---
// spec offset  4 = Snapshot ID (u32 LE);        Body[0]
// spec offset  8 = Order ID (u64 LE);           Body[4]
// spec offset 16 = Side (u8);                   Body[12]
// spec offset 17 = Order Flags (u8);            Body[13]
// spec offset 20 = Enter Timestamp (u64 LE);    Body[16]
// spec offset 28 = Price (i64 LE);              Body[24]
// spec offset 36 = Quantity (u64 LE);           Body[32]
func snapshotOrderSnapshotID(m wire.Message) uint32     { return bodyU32LE(m, 0) }
func snapshotOrderOrderID(m wire.Message) uint64        { return bodyU64LE(m, 4) }
func snapshotOrderSide(m wire.Message) uint8            { return bodyU8(m, 12) }
func snapshotOrderFlags(m wire.Message) uint8           { return bodyU8(m, 13) }
func snapshotOrderEnterTimestamp(m wire.Message) uint64 { return bodyU64LE(m, 16) }
func snapshotOrderPrice(m wire.Message) int64           { return int64(bodyU64LE(m, 24)) }
func snapshotOrderQuantity(m wire.Message) uint64       { return bodyU64LE(m, 32) }

// --- SnapshotEnd (0x22, 20 bytes total) ---
// spec offset  4 = Instrument ID (u32 LE);  Body[0]
// spec offset  8 = Anchor Seq (u64 LE);     Body[4]
// spec offset 16 = Snapshot ID (u32 LE);    Body[12]
func snapshotEndInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func snapshotEndAnchorSeq(m wire.Message) uint64    { return bodyU64LE(m, 4) }
func snapshotEndSnapshotID(m wire.Message) uint32   { return bodyU32LE(m, 12) }

// --- Quote / TOB (0x03, 60 bytes total) ---
// spec offset  4 = Instrument ID (u32); Body[0]
// spec offset  8 = Source ID (u16);     Body[4]
// spec offset 10 = Update Flags (u8);   Body[6]
// spec offset 20 = Bid Price (i64);     Body[16]
// spec offset 28 = Bid Quantity (u64);  Body[24]
// spec offset 36 = Ask Price (i64);     Body[32]
// spec offset 44 = Ask Quantity (u64);  Body[40]
// spec offset 52 = Bid Source Count (u16); Body[48]
// spec offset 54 = Ask Source Count (u16); Body[50]
func quoteInstrumentID(m wire.Message) uint32   { return bodyU32LE(m, 0) }
func quoteSourceID(m wire.Message) uint16       { return bodyU16LE(m, 4) }
func quoteUpdateFlags(m wire.Message) uint8     { return bodyU8(m, 6) }
func quoteBidPrice(m wire.Message) int64        { return int64(bodyU64LE(m, 16)) }
func quoteAskPrice(m wire.Message) int64        { return int64(bodyU64LE(m, 32)) }
func quoteBidSourceCount(m wire.Message) uint16 { return bodyU16LE(m, 48) }
func quoteAskSourceCount(m wire.Message) uint16 { return bodyU16LE(m, 50) }

// --- Trade (0x04, 52 bytes total) ---
// spec offset  4 = Instrument ID (u32); Body[0]
// spec offset  8 = Source ID (u16);     Body[4]
// spec offset 10 = Aggressor Side (u8);  Body[6]
// spec offset 11 = Trade Flags (u8);     Body[7]
// spec offset 36 = Trade ID (u64);       Body[32]
func tradeInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func tradeSourceID(m wire.Message) uint16     { return bodyU16LE(m, 4) }
func tradeAggressorSide(m wire.Message) uint8 { return bodyU8(m, 6) }
func tradeFlags(m wire.Message) uint8         { return bodyU8(m, 7) }
func tradeTradeID(m wire.Message) uint64      { return bodyU64LE(m, 32) }

// --- Midpoint (0x03, 40 bytes total, Midpoint feed) ---
// spec offset  4 = Instrument ID (u32);     Body[0]
// spec offset 10 = Method (u8);             Body[6]
// spec offset 11 = Quality Flags (u8);      Body[7]
// spec offset 12 = Book Timestamp (u64);    Body[8]
// spec offset 20 = Compute Timestamp (u64); Body[16]
// spec offset 28 = Mid Price (i64);         Body[24]
func midpointInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func midpointMethod(m wire.Message) uint8        { return bodyU8(m, 6) }
func midpointQualityFlags(m wire.Message) uint8  { return bodyU8(m, 7) }
func midpointBookTS(m wire.Message) uint64       { return bodyU64LE(m, 8) }
func midpointComputeTS(m wire.Message) uint64    { return bodyU64LE(m, 16) }
func midpointMidPrice(m wire.Message) int64      { return int64(bodyU64LE(m, 24)) }

// bodyU32LE safely reads a uint32 from m.Body at the given body offset.
func bodyU32LE(m wire.Message, off int) uint32 {
	if off+3 >= len(m.Body) {
		return 0
	}
	return binary.LittleEndian.Uint32(m.Body[off:])
}

// --- ManifestSummary (0x07, 24 bytes total) ---
// spec offset 5  = Valid (u8);              Body[1]
// spec offset 8  = Manifest Seq (u16 LE);   Body[4]
// spec offset 12 = Instrument Count (u32 LE); Body[8]
func manifestValid(m wire.Message) uint8     { return bodyU8(m, 1) }
func manifestSeqField(m wire.Message) uint16 { return bodyU16LE(m, 4) }
func manifestCount(m wire.Message) uint32    { return bodyU32LE(m, 8) }

// manifestFields extracts (valid, seq, count) from a ManifestSummary body.
func manifestFields(m wire.Message) (valid uint8, seq uint16, count uint32) {
	return manifestValid(m), manifestSeqField(m), manifestCount(m)
}

// --- InstrumentDefinition (0x02) — feed- and schema-dependent layout ---
//
// Every offset lives in instrdef.go, keyed by (feed, schema). Nothing here
// hardcodes one, because the whole point of that table is that a reader looking
// for "where is Manifest Seq" finds one answer.
//
// bodyU8At and bodyU16LEAt bounds-check, because a wrong layout must read as
// absent rather than as zero: a zero Manifest Seq is a legal value and would be
// graded as data.

// instrDefAllFields extracts (instrumentID, manifestSeq, defaultMethod,
// priceBound) from an InstrumentDefinition at the given feed and schema version.
//
// ok is false when the (feed, schema) pair has no layout, or when the message is
// shorter than its layout requires. The caller MUST skip the message on false and
// MUST NOT treat the zero values as read data.
//
// defaultMethod is only present on midpoint. priceBound is read for MBO and
// midpoint only: TOB never carries it, and MBP is deliberately excluded here
// because the switch this replaces never read Price Bound for MBP either.
// Whether MBP should start reporting Price Bound is a separate decision, not a
// side effect of adding schema support.
func instrDefAllFields(feed core.Feed, schema uint8, m wire.Message) (instrID uint32, manifestSeq uint16, defaultMethod, priceBound uint8, ok bool) {
	l, ok := instrDefLayoutFor(feed, schema)
	if !ok {
		return 0, 0, 0, 0, false
	}
	instrID, ok = bodyU32LEAt(m, l.InstrumentID)
	if !ok {
		return 0, 0, 0, 0, false
	}
	manifestSeq, ok = bodyU16LEAt(m, l.ManifestSeq)
	if !ok {
		return 0, 0, 0, 0, false
	}
	if l.DefaultMethod >= 0 {
		// ok discarded: safe because ManifestSeq sits at a higher body offset than
		// DefaultMethod in every layout row, so the ManifestSeq bounds check above
		// already proved the body reaches this offset. TestInstrDefLayoutManifestSeqIsLast
		// pins that ordering; if a future row breaks it, that test must fail.
		defaultMethod, _ = bodyU8At(m, l.DefaultMethod)
	}
	if (feed == core.FeedMBO || feed == core.FeedMidpoint) && l.PriceBound >= 0 {
		// ok discarded: same reasoning as DefaultMethod above — ManifestSeq is
		// always the higher offset, pinned by the same test.
		priceBound, _ = bodyU8At(m, l.PriceBound)
	}
	return instrID, manifestSeq, defaultMethod, priceBound, true
}

// bodyU8At, bodyU16LEAt and bodyU32LEAt read at a body offset, reporting whether
// the body is long enough. The unchecked bodyU8/bodyU16LE/bodyU32LE helpers above
// are for fixed-length messages whose length checkTier1 already gated.
func bodyU8At(m wire.Message, off int) (uint8, bool) {
	if off < 0 || off >= len(m.Body) {
		return 0, false
	}
	return m.Body[off], true
}

func bodyU16LEAt(m wire.Message, off int) (uint16, bool) {
	if off < 0 || off+2 > len(m.Body) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(m.Body[off:]), true
}

func bodyU32LEAt(m wire.Message, off int) (uint32, bool) {
	if off < 0 || off+4 > len(m.Body) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(m.Body[off:]), true
}
