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

	// MsgLenExtMask selects the low nibble of message-header byte 3, which
	// carries bits 8–11 of the 12-bit Message Length (common framing 0.2.0).
	// The high nibble is reserved and MUST be zero on emission.
	MsgLenExtMask         = 0x0F
	MsgLenExtReservedMask = 0xF0

	// SchemaVersionBase is the schema version of a channel whose messages all
	// fit the 8-bit Message Length of common framing 0.1.x. SchemaVersionLong
	// declares that messages on the channel may exceed 255 bytes and so use the
	// 12-bit length extension. Every feed this tool decodes has only short
	// message types and therefore declares SchemaVersionBase.
	SchemaVersionBase = 1
	SchemaVersionLong = 2
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

// Message is one application message. Length is the 12-bit Message Length of
// common framing 0.2.0, assembled from byte 1 (low 8 bits) and the low nibble of
// byte 3 (bits 8–11); Flags is the u8 at byte 2. Under 0.1.x framing byte 3 was
// the high byte of a u16 Flags whose bits 1–15 were reserved and ignored, so for
// every message of 255 bytes or fewer the two readings agree byte-for-byte.
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
