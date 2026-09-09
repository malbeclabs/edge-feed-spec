package wire

import "time"

const (
	MagicTOB uint16 = 0x445A
	MagicMid uint16 = 0x4D44
	MagicMBO uint16 = 0x4444
	MagicMBP uint16 = 0x4442

	FrameHeaderLen = 24
	MsgHeaderLen   = 4
	MaxFrameLen    = 1232
)

// SupportedSchemas returns the frame Schema Version bytes this validator decodes
// for the given feed, identified by magic.
//
// Per VERSIONING.md the byte equals the spec's MAJOR version, so the set is
// per-feed. Midpoint kept its slimmed 64-byte InstrumentDefinition when the
// other five widened Symbol to char[64], so it is still at spec 1.x while its
// siblings are at 3.x. Keying on magic rather than core.Feed avoids an import
// cycle and is exact: magic identifies the feed.
//
// **This validator accepts more than one MAJOR version, and that is deliberate.**
// VERSIONING.md requires a decoder to reject a Schema Version it was not built
// for. A fleet validator grades every venue at once, and venues sit on different
// MAJOR versions, so a single-version binary forces a per-venue build pin —
// exactly the failure this set removes. The exception is scoped to this tool and
// recorded in VERSIONING.md. Production consumers keep the reject rule.
//
// Schema 2 is absent on purpose. It widened Symbol to char[64] (80 -> 128 bytes)
// and no publisher ever deployed it, so there is no capture to test a schema-2
// layout against. A layout nobody has seen on the wire is a guess, and a guess
// that silently decodes is worse than a rejection.
func SupportedSchemas(magic uint16) []uint8 {
	if magic == MagicMid {
		return []uint8{1}
	}
	return []uint8{1, 3}
}

// SchemaSupported reports whether this validator decodes ver for the feed
// identified by magic.
func SchemaSupported(magic uint16, ver uint8) bool {
	for _, v := range SupportedSchemas(magic) {
		if v == ver {
			return true
		}
	}
	return false
}

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
	// Market-by-price payloads. These take fresh IDs in 0x40-0x4F rather than
	// reusing a sibling's, because the cross-spec policy forbids reassigning an
	// ID to a different payload — SnapshotLevel is not SnapshotOrder.
	TypeLevelUpdate   = 0x40
	TypeBookClear     = 0x41
	TypeSnapshotLevel = 0x42
	// Shared with TOB and MBO, byte-identical, and carried by MBP.
	TypeLiquidation = 0x08
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
