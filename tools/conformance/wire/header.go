package wire

import "time"

const (
	MagicTOB uint16 = 0x445A
	MagicMid uint16 = 0x4D44
	MagicMBO uint16 = 0x4444

	FrameHeaderLen = 24
	MsgHeaderLen   = 4
	MaxFrameLen    = 1232
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

type Message struct {
	Type   uint8
	Length uint8
	Flags  uint16
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
