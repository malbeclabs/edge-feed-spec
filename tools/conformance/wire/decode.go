package wire

import (
	"encoding/binary"
	"fmt"
)

// Decode strictly parses one datagram. expectMagic is the bound feed's magic.
// It returns the best-effort Frame and any Tier-1 structural findings. Findings
// with Transport=true are transport corruption (truncation), not publisher faults.
func Decode(raw []byte, expectMagic uint16) (*Frame, []StructFinding) {
	var fs []StructFinding
	f := &Frame{Raw: raw}
	if len(raw) < FrameHeaderLen {
		return f, append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", 0,
			fmt.Sprintf("datagram %d < header %d", len(raw), FrameHeaderLen), true})
	}
	h := &f.Header
	h.Magic = binary.LittleEndian.Uint16(raw[0:])
	h.SchemaVersion = raw[2]
	h.ChannelID = raw[3]
	h.Sequence = binary.LittleEndian.Uint64(raw[4:])
	h.SendTS = binary.LittleEndian.Uint64(raw[12:])
	h.MsgCount = raw[20]
	h.ResetCount = raw[21]
	h.FrameLength = binary.LittleEndian.Uint16(raw[22:])

	if h.Magic != expectMagic {
		fs = append(fs, StructFinding{"FRAME.MAGIC_MISMATCH", 0,
			fmt.Sprintf("magic 0x%04X, expected 0x%04X", h.Magic, expectMagic), false})
		// A wrong magic means this datagram is not a frame of this feed (misroute
		// or corruption). The remaining header fields and message bytes can't be
		// trusted, so return now rather than emit a cascade of derived findings
		// from walking non-frame bytes.
		return f, fs
	}
	if h.SchemaVersion != 1 {
		fs = append(fs, StructFinding{"FRAME.SCHEMA_VERSION", 2,
			fmt.Sprintf("schema version %d", h.SchemaVersion), false})
	}
	// Frame length: publisher-invalid (self-inconsistent / out of range) vs transport truncation.
	switch {
	case h.FrameLength < FrameHeaderLen || h.FrameLength > MaxFrameLen:
		fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", 22,
			fmt.Sprintf("frame length %d out of [%d,%d]", h.FrameLength, FrameHeaderLen, MaxFrameLen), false})
	case int(h.FrameLength) > len(raw):
		fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", 22,
			fmt.Sprintf("declared %d > received %d (truncation)", h.FrameLength, len(raw)), true})
	case int(h.FrameLength) < len(raw):
		// One datagram = one frame; trailing bytes beyond the declared length are a
		// publisher framing bug (not transport — nothing on the wire adds bytes).
		fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", 22,
			fmt.Sprintf("declared %d < received %d (trailing bytes)", h.FrameLength, len(raw)), false})
	}
	// Message walk appends to fs and fills f.Messages.
	fs = append(fs, f.walkMessages()...)
	return f, fs
}

func (f *Frame) walkMessages() []StructFinding {
	var fs []StructFinding
	raw := f.Raw
	frameLen := int(f.Header.FrameLength)
	// validDecl is true when the declared FrameLength is in the legal range [FrameHeaderLen,
	// MaxFrameLen]. An out-of-range declaration is publisher-invalid; the header check in Decode
	// already flagged it, and the walk must not treat clamping as transport in that case.
	validDecl := frameLen >= FrameHeaderLen && frameLen <= MaxFrameLen
	// The authoritative end is min(declared frame length, received bytes); overruns past
	// received bytes are transport truncation (already flagged in Decode if declared>received).
	end := frameLen
	if end > len(raw) {
		end = len(raw)
	}
	// truncated is true only when the declared length is valid but exceeds the received
	// datagram — the walker was stopped by the receive buffer, not a publisher error.
	truncated := validDecl && end < frameLen
	off := FrameHeaderLen
	for off < end {
		if off+MsgHeaderLen > end {
			// A sub-header trailing fragment WITHIN the declared frame length is publisher
			// self-inconsistency; if `end` was clamped by a valid-but-short datagram it is
			// transport truncation.
			fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", off,
				"trailing bytes shorter than message header", truncated})
			break
		}
		mlen := raw[off+1]
		if mlen < MsgHeaderLen {
			fs = append(fs, StructFinding{"MSG.LENGTH_PER_TYPE", off,
				fmt.Sprintf("message length %d < header %d", mlen, MsgHeaderLen), false})
			break
		}
		if off+int(mlen) > frameLen {
			fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", off,
				"message overruns declared frame length", false})
			break
		}
		if off+int(mlen) > len(raw) {
			// Transport truncation only when the declared FrameLength is itself valid; if the
			// declared length is out of range, the publisher is the cause, not transport.
			fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", off,
				"message overruns received datagram (truncation)", validDecl})
			break
		}
		m := Message{
			Type:   raw[off],
			Length: mlen,
			Flags:  binary.LittleEndian.Uint16(raw[off+2:]),
			Body:   raw[off+MsgHeaderLen : off+int(mlen)],
			Offset: off,
		}
		f.Messages = append(f.Messages, m)
		off += int(mlen)
	}
	// MsgCount==0 is always a publisher error: it is read from the fully decoded header and
	// the spec mandates at least one message per frame; transport truncation does not affect it.
	if f.Header.MsgCount == 0 {
		fs = append(fs, StructFinding{"FRAME.MSG_COUNT_RANGE", 20, "message count 0", false})
	}
	// Count-vs-walked and walk-must-reach-FrameLength are only verifiable when the walk was
	// not cut short by transport truncation or a walk-local transport finding.
	if !anyTransport(fs) && !truncated {
		if f.Header.MsgCount != 0 && int(f.Header.MsgCount) != len(f.Messages) {
			fs = append(fs, StructFinding{"FRAME.MSG_COUNT_RANGE", 20,
				fmt.Sprintf("count %d, walked %d", f.Header.MsgCount, len(f.Messages)), false})
		}
		if validDecl && off != frameLen {
			fs = append(fs, StructFinding{"FRAME.LENGTH_CONSISTENCY", off,
				fmt.Sprintf("walk ended at %d, frame length %d", off, frameLen), false})
		}
	}
	return fs
}

func anyTransport(fs []StructFinding) bool {
	for _, x := range fs {
		if x.Transport {
			return true
		}
	}
	return false
}
