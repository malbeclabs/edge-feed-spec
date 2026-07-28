package wire

import "time"

const (
	MagicTOB uint16 = 0x445A
	MagicMid uint16 = 0x4D44
	MagicMBO uint16 = 0x4444

	FrameHeaderLen = 24
	MsgHeaderLen   = 4
	MaxFrameLen    = 1232

	// MaxMsgLen is the largest message a frame can hold: one message filling a
	// maximum-size frame. The 12-bit Message Length field of common framing
	// 0.2.0 admits 4095, so the frame is the binding limit, not the field.
	MaxMsgLen = MaxFrameLen - FrameHeaderLen

	// MsgLenMask selects the low 12 bits of the little-endian u16 at message-header
	// offset 1, which carry the whole Message Length under common framing 0.2.0.
	// The top 4 bits are reserved and MUST be zero on emission.
	MsgLenMask         = 0x0FFF
	MsgLenReservedMask = 0xF000

	// SchemaVersionLegacy is common framing 0.1.x: a u8 Message Length at offset 1
	// and a u16 Flags at offset 2. SchemaVersionCF2 is common framing 0.2.0: a
	// contiguous 12-bit Message Length at offset 1 and a u8 Flags at offset 3. The
	// layouts are not interchangeable, so a decoder walks only the version it
	// implements. This decoder implements 0.2.0.
	SchemaVersionLegacy = 1
	SchemaVersionCF2    = 2
)

// Message type IDs (shared + per-feed).
const (
	TypeHeartbeat     = 0x01
	TypeInstrumentDef = 0x02
	TypeQuote         = 0x03 // TOB
	TypeMidpoint      = 0x03 // Midpoint (same id, different feed)
	TypeTrade         = 0x04
	// 0x05 is reserved/unused across all edge-feed-spec feeds (see MSG.RESERVED_TYPE_0X03_0X05).
	TypeEndOfSession  = 0x06
	TypeManifest      = 0x07
	TypeOrderAdd      = 0x10
	TypeOrderCancel   = 0x11
	TypeOrderExecute  = 0x12
	TypeBatchBoundary = 0x13
	TypeInstrReset    = 0x14
	TypeSnapshotBegin = 0x20
	TypeSnapshotOrder = 0x21
	TypeSnapshotEnd   = 0x22
)

type FrameHeader struct {
	Magic         uint16
	SchemaVersion uint8
	ChannelID     uint8
	Sequence      uint64
	SendTS        uint64
	MsgCount      uint8
	ResetCount    uint8
	FrameLength   uint16
}

// Message is one application message. Length is the contiguous 12-bit Message
// Length of common framing 0.2.0 — the low 12 bits of the little-endian u16 at
// offset 1 — and Flags is the u8 at offset 3. Under 0.1.x framing the length was
// a u8 at offset 1 and Flags a u16 at offset 2, so the two layouts differ
// wherever Flags is non-zero: they are gated by frame Schema Version, not mixed.
type Message struct {
	Type   uint8
	Length uint16
	Flags  uint8
	Body   []byte // bytes after the 4-byte message header, bounded by Length
	Offset int    // byte offset of this message within the frame
}

type Frame struct {
	Header   FrameHeader
	Messages []Message
	Raw      []byte
	RecvTS   time.Time
}

// StructFinding is a Tier-1 structural deviation surfaced by the decoder.
// (engine maps RuleID→core.RuleMeta; wire stays dep-free of core by using strings.)
type StructFinding struct {
	RuleID    string
	Offset    int
	Detail    string
	Transport bool // true = transport corruption bucket, not a publisher conformance violation
}
