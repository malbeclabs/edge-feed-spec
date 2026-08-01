package engine

import "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"

// Market-by-price field accessors. Offsets are the spec's, minus the 4-byte
// application header that `wire.Message.Body` already strips.

// --- LevelUpdate (0x40, 48 bytes total) ---
// spec  4 Instrument ID | 8 Source ID | 10 Side | 11 Action
//
//	12 Per-Instrument Seq | 16 Price | 24 Quantity | 32 Timestamp
//	40 Order Count | 42 Level Index | 44 Update Reason | 45 Level Flags
func levelUpdateInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func levelUpdateSourceID(m wire.Message) uint16     { return bodyU16LE(m, 4) }
func levelUpdateSide(m wire.Message) uint8          { return bodyU8(m, 6) }
func levelUpdateAction(m wire.Message) uint8        { return bodyU8(m, 7) }
func levelUpdatePerInstrSeq(m wire.Message) uint32  { return bodyU32LE(m, 8) }
func levelUpdatePrice(m wire.Message) int64         { return int64(bodyU64LE(m, 12)) }
func levelUpdateQuantity(m wire.Message) uint64     { return bodyU64LE(m, 20) }
func levelUpdateOrderCount(m wire.Message) uint16   { return bodyU16LE(m, 36) }
func levelUpdateLevelIndex(m wire.Message) uint16   { return bodyU16LE(m, 38) }
func levelUpdateReason(m wire.Message) uint8        { return bodyU8(m, 40) }
func levelUpdateLevelFlags(m wire.Message) uint8    { return bodyU8(m, 41) }

// --- BookClear (0x41, 36 bytes total) ---
// spec  4 Instrument ID | 8 Source ID | 10 Clear Side | 11 Scope
//
//	12 Per-Instrument Seq | 16 From Price | 24 Timestamp | 32 Clear Reason
func bookClearInstrumentID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func bookClearClearSide(m wire.Message) uint8     { return bodyU8(m, 6) }
func bookClearScope(m wire.Message) uint8         { return bodyU8(m, 7) }
func bookClearPerInstrSeq(m wire.Message) uint32  { return bodyU32LE(m, 8) }
func bookClearFromPrice(m wire.Message) int64     { return int64(bodyU64LE(m, 12)) }
func bookClearReason(m wire.Message) uint8        { return bodyU8(m, 28) }

// --- SnapshotBegin (0x20, 40 bytes on this feed) ---
// Bytes 0-35 are market-by-order's layout; `Depth Bound` is appended at 36.
// `Total Orders` reads as `Total Levels` — same field, same offset.
func snapshotBeginDepthBound(m wire.Message) uint32 { return bodyU32LE(m, 32) }

// --- SnapshotLevel (0x42, 32 bytes total) ---
// spec  4 Snapshot ID | 8 Price | 16 Quantity | 24 Order Count | 26 Side
//
//	27 Level Flags
func snapshotLevelSnapshotID(m wire.Message) uint32 { return bodyU32LE(m, 0) }
func snapshotLevelPrice(m wire.Message) int64       { return int64(bodyU64LE(m, 4)) }
func snapshotLevelQuantity(m wire.Message) uint64   { return bodyU64LE(m, 12) }
func snapshotLevelOrderCount(m wire.Message) uint16 { return bodyU16LE(m, 20) }
func snapshotLevelSide(m wire.Message) uint8        { return bodyU8(m, 22) }
func snapshotLevelFlags(m wire.Message) uint8       { return bodyU8(m, 23) }

// Action values (0x40). The table starts at Unknown, not at New — a publisher
// numbering New/Change/Delete from zero puts every removal on the wire as a
// Change carrying Quantity = 0, which the spec forbids outright.
const (
	mbpActionUnknown = 0
	mbpActionNew     = 1
	mbpActionChange  = 2
	mbpActionDelete  = 3
	mbpActionOther   = 255
)

// Clear Side / Scope values (0x41).
const (
	mbpClearSideBid  = 0
	mbpClearSideAsk  = 1
	mbpClearSideBoth = 2

	mbpScopeWholeSide = 0
	mbpScopeFromPrice = 1
)
