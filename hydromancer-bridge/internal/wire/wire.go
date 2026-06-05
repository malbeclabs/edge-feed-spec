// Package wire implements the DoubleZero Top-of-Book & Trades Feed binary
// format: the 24-byte frame header, the 4-byte application message header, and
// the message types the bridge needs (Quote, InstrumentDefinition,
// ManifestSummary, Heartbeat).
//
// Everything is little-endian, matching the spec's design principle 1.
package wire

import (
	"encoding/binary"
	"errors"
)

// Magic is the frame delimiter, read as a little-endian u16 (spec: 0x445A, "DZ").
const Magic uint16 = 0x445A

// SchemaVersion is the protocol version this implementation speaks.
const SchemaVersion uint8 = 1

// Header sizes.
const (
	FrameHeaderSize = 24
	MsgHeaderSize   = 4
	MaxFrameSize    = 1232
)

// Message type IDs (spec "Message Types" table).
const (
	TypeHeartbeat            uint8 = 0x01
	TypeInstrumentDefinition uint8 = 0x02
	TypeQuote                uint8 = 0x03
	TypeTrade                uint8 = 0x04
	TypeEndOfSession         uint8 = 0x06
	TypeManifestSummary      uint8 = 0x07
)

// Fixed message lengths (including the 4-byte application message header).
const (
	LenHeartbeat            = 16
	LenInstrumentDefinition = 80
	LenQuote                = 60
	LenManifestSummary      = 24
	LenEndOfSession         = 12
)

// Quote update-flag bits (Quote.UpdateFlags, spec offset 10).
const (
	UpdBidUpdated uint8 = 1 << 0
	UpdAskUpdated uint8 = 1 << 1
	UpdBidGone    uint8 = 1 << 2
	UpdAskGone    uint8 = 1 << 3
)

var (
	ErrShortBuffer = errors.New("wire: buffer too short")
	ErrBadMagic    = errors.New("wire: bad frame magic")
)

// FrameHeader is the 24-byte header that prefixes every UDP datagram.
type FrameHeader struct {
	Magic         uint16
	SchemaVersion uint8
	ChannelID     uint8
	SequenceNum   uint64
	SendTimestamp uint64 // ns since epoch
	MsgCount      uint8
	ResetCount    uint8
	FrameLength   uint16
}

// DecodeFrameHeader parses the 24-byte frame header from b.
func DecodeFrameHeader(b []byte) (FrameHeader, error) {
	var h FrameHeader
	if len(b) < FrameHeaderSize {
		return h, ErrShortBuffer
	}
	h.Magic = binary.LittleEndian.Uint16(b[0:])
	if h.Magic != Magic {
		return h, ErrBadMagic
	}
	h.SchemaVersion = b[2]
	h.ChannelID = b[3]
	h.SequenceNum = binary.LittleEndian.Uint64(b[4:])
	h.SendTimestamp = binary.LittleEndian.Uint64(b[12:])
	h.MsgCount = b[20]
	h.ResetCount = b[21]
	h.FrameLength = binary.LittleEndian.Uint16(b[22:])
	return h, nil
}

// Encode writes the frame header into the first 24 bytes of b.
func (h FrameHeader) Encode(b []byte) {
	binary.LittleEndian.PutUint16(b[0:], Magic)
	b[2] = h.SchemaVersion
	b[3] = h.ChannelID
	binary.LittleEndian.PutUint64(b[4:], h.SequenceNum)
	binary.LittleEndian.PutUint64(b[12:], h.SendTimestamp)
	b[20] = h.MsgCount
	b[21] = h.ResetCount
	binary.LittleEndian.PutUint16(b[22:], h.FrameLength)
}

// MsgHeader is the 4-byte application message header.
type MsgHeader struct {
	Type   uint8
	Length uint8
	Flags  uint16
}

// DecodeMsgHeader parses the 4-byte application message header from b.
func DecodeMsgHeader(b []byte) (MsgHeader, error) {
	if len(b) < MsgHeaderSize {
		return MsgHeader{}, ErrShortBuffer
	}
	return MsgHeader{
		Type:   b[0],
		Length: b[1],
		Flags:  binary.LittleEndian.Uint16(b[2:]),
	}, nil
}

// Quote is the 0x03 message: a two-sided BBO update.
type Quote struct {
	InstrumentID    uint32
	SourceID        uint16
	UpdateFlags     uint8
	SourceTimestamp uint64 // ns
	BidPrice        int64
	BidQuantity     uint64
	AskPrice        int64
	AskQuantity     uint64
	BidSourceCount  uint16
	AskSourceCount  uint16
}

// DecodeQuote parses a Quote body. b must start at the message header.
func DecodeQuote(b []byte) (Quote, error) {
	var q Quote
	if len(b) < LenQuote {
		return q, ErrShortBuffer
	}
	q.InstrumentID = binary.LittleEndian.Uint32(b[4:])
	q.SourceID = binary.LittleEndian.Uint16(b[8:])
	q.UpdateFlags = b[10]
	q.SourceTimestamp = binary.LittleEndian.Uint64(b[12:])
	q.BidPrice = int64(binary.LittleEndian.Uint64(b[20:]))
	q.BidQuantity = binary.LittleEndian.Uint64(b[28:])
	q.AskPrice = int64(binary.LittleEndian.Uint64(b[36:]))
	q.AskQuantity = binary.LittleEndian.Uint64(b[44:])
	q.BidSourceCount = binary.LittleEndian.Uint16(b[52:])
	q.AskSourceCount = binary.LittleEndian.Uint16(b[54:])
	return q, nil
}

// Encode writes the full 60-byte Quote message (header + body) into b.
func (q Quote) Encode(b []byte) {
	b[0] = TypeQuote
	b[1] = LenQuote
	binary.LittleEndian.PutUint16(b[2:], 0) // flags
	binary.LittleEndian.PutUint32(b[4:], q.InstrumentID)
	binary.LittleEndian.PutUint16(b[8:], q.SourceID)
	b[10] = q.UpdateFlags
	b[11] = 0
	binary.LittleEndian.PutUint64(b[12:], q.SourceTimestamp)
	binary.LittleEndian.PutUint64(b[20:], uint64(q.BidPrice))
	binary.LittleEndian.PutUint64(b[28:], q.BidQuantity)
	binary.LittleEndian.PutUint64(b[36:], uint64(q.AskPrice))
	binary.LittleEndian.PutUint64(b[44:], q.AskQuantity)
	binary.LittleEndian.PutUint16(b[52:], q.BidSourceCount)
	binary.LittleEndian.PutUint16(b[54:], q.AskSourceCount)
	// bytes 56..60 reserved/padding (already zero)
}

// BidGone reports whether the bid side has no resting liquidity.
func (q Quote) BidGone() bool {
	return q.UpdateFlags&UpdBidGone != 0 || q.BidPrice == 0
}

// AskGone reports whether the ask side has no resting liquidity.
func (q Quote) AskGone() bool {
	return q.UpdateFlags&UpdAskGone != 0 || q.AskPrice == 0
}

// InstrumentDefinition is the 0x02 message: maps a numeric ID to metadata.
// Only the fields the bridge needs for conversion are decoded.
type InstrumentDefinition struct {
	InstrumentID uint32
	Symbol       string
	Leg1         string
	Leg2         string
	AssetClass   uint8
	PriceExp     int8
	QtyExp       int8
	MarketModel  uint8
	ManifestSeq  uint16
}

// DecodeInstrumentDefinition parses an InstrumentDefinition body. b must start
// at the message header.
func DecodeInstrumentDefinition(b []byte) (InstrumentDefinition, error) {
	var d InstrumentDefinition
	if len(b) < LenInstrumentDefinition {
		return d, ErrShortBuffer
	}
	d.InstrumentID = binary.LittleEndian.Uint32(b[4:])
	d.Symbol = cstr(b[8:24])
	d.Leg1 = cstr(b[24:32])
	d.Leg2 = cstr(b[32:40])
	d.AssetClass = b[40]
	d.PriceExp = int8(b[41])
	d.QtyExp = int8(b[42])
	d.MarketModel = b[43]
	d.ManifestSeq = binary.LittleEndian.Uint16(b[78:])
	return d, nil
}

// Encode writes the full 80-byte InstrumentDefinition (header + body) into b.
func (d InstrumentDefinition) Encode(b []byte) {
	b[0] = TypeInstrumentDefinition
	b[1] = LenInstrumentDefinition
	binary.LittleEndian.PutUint16(b[2:], 0)
	binary.LittleEndian.PutUint32(b[4:], d.InstrumentID)
	putCstr(b[8:24], d.Symbol)
	putCstr(b[24:32], d.Leg1)
	putCstr(b[32:40], d.Leg2)
	b[40] = d.AssetClass
	b[41] = byte(d.PriceExp)
	b[42] = byte(d.QtyExp)
	b[43] = d.MarketModel
	binary.LittleEndian.PutUint16(b[78:], d.ManifestSeq)
}

// ManifestSummary is the 0x07 message: a periodic summary of the active set.
type ManifestSummary struct {
	ChannelID       uint8
	Valid           uint8
	ManifestSeq     uint16
	InstrumentCount uint32
	Timestamp       uint64
}

// DecodeManifestSummary parses a ManifestSummary body.
func DecodeManifestSummary(b []byte) (ManifestSummary, error) {
	var m ManifestSummary
	if len(b) < LenManifestSummary {
		return m, ErrShortBuffer
	}
	m.ChannelID = b[4]
	m.Valid = b[5]
	m.ManifestSeq = binary.LittleEndian.Uint16(b[8:])
	m.InstrumentCount = binary.LittleEndian.Uint32(b[12:])
	m.Timestamp = binary.LittleEndian.Uint64(b[16:])
	return m, nil
}

// Encode writes the full 24-byte ManifestSummary (header + body) into b.
func (m ManifestSummary) Encode(b []byte) {
	b[0] = TypeManifestSummary
	b[1] = LenManifestSummary
	binary.LittleEndian.PutUint16(b[2:], 0)
	b[4] = m.ChannelID
	b[5] = m.Valid
	binary.LittleEndian.PutUint16(b[8:], m.ManifestSeq)
	binary.LittleEndian.PutUint32(b[12:], m.InstrumentCount)
	binary.LittleEndian.PutUint64(b[16:], m.Timestamp)
}

// cstr reads a null-padded, left-justified ASCII string.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// putCstr writes s left-justified and null-padded into dst, truncating if needed.
func putCstr(dst []byte, s string) {
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, s)
}
