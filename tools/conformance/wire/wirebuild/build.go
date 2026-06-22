package wirebuild

import (
	"encoding/binary"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

type Body struct{ b []byte }

func (x *Body) U8(v uint8) *Body   { x.b = append(x.b, v); return x }
func (x *Body) U16(v uint16) *Body { x.b = binary.LittleEndian.AppendUint16(x.b, v); return x }
func (x *Body) U32(v uint32) *Body { x.b = binary.LittleEndian.AppendUint32(x.b, v); return x }
func (x *Body) U64(v uint64) *Body { x.b = binary.LittleEndian.AppendUint64(x.b, v); return x }
func (x *Body) I64(v int64) *Body  { return x.U64(uint64(v)) }
func (x *Body) Pad(n int) *Body    { x.b = append(x.b, make([]byte, n)...); return x }
func (x *Body) Char(s string, n int) *Body {
	buf := make([]byte, n)
	copy(buf, s)
	x.b = append(x.b, buf...)
	return x
}

type msg struct {
	typ         uint8
	declaredLen uint8 // what to write in the header (override to forge MSG.LENGTH_PER_TYPE)
	flags       uint16
	body        []byte
}

type frame struct {
	magic          uint16
	schemaVer      uint8
	channelID      uint8
	seq            uint64
	sendTS         uint64
	resetCount     uint8
	msgs           []msg
	overrideCount  *uint8  // forge FRAME.MSG_COUNT_RANGE
	overrideLength *uint16 // forge FRAME.LENGTH_CONSISTENCY
}

func Frame(magic uint16) *frame { return &frame{magic: magic, schemaVer: 1} }

func (f *frame) Schema(v uint8) *frame       { f.schemaVer = v; return f }
func (f *frame) Channel(c uint8) *frame      { f.channelID = c; return f }
func (f *frame) Seq(s uint64) *frame         { f.seq = s; return f }
func (f *frame) SendTS(t uint64) *frame      { f.sendTS = t; return f }
func (f *frame) ResetCount(r uint8) *frame   { f.resetCount = r; return f }
func (f *frame) ForgeCount(c uint8) *frame   { f.overrideCount = &c; return f }
func (f *frame) ForgeLength(l uint16) *frame { f.overrideLength = &l; return f }

// Msg appends a message. declaredLen is the value written in the 1-byte length
// header; pass the correct size for conformant frames, a wrong value to test
// MSG.LENGTH_PER_TYPE. body builds the bytes AFTER the 4-byte message header.
func (f *frame) Msg(typ uint8, declaredLen uint8, build func(*Body)) *frame {
	b := &Body{}
	if build != nil {
		build(b)
	}
	f.msgs = append(f.msgs, msg{typ: typ, declaredLen: declaredLen, body: b.b})
	return f
}
func (f *frame) MsgFlags(typ uint8, declaredLen uint8, flags uint16, build func(*Body)) *frame {
	f.Msg(typ, declaredLen, build)
	f.msgs[len(f.msgs)-1].flags = flags
	return f
}

func (f *frame) Bytes() []byte {
	out := make([]byte, wire.FrameHeaderLen)
	binary.LittleEndian.PutUint16(out[0:], f.magic)
	out[2] = f.schemaVer
	out[3] = f.channelID
	binary.LittleEndian.PutUint64(out[4:], f.seq)
	binary.LittleEndian.PutUint64(out[12:], f.sendTS)
	count := uint8(len(f.msgs))
	if f.overrideCount != nil {
		count = *f.overrideCount
	}
	out[20] = count
	out[21] = f.resetCount
	for _, m := range f.msgs {
		hdr := []byte{m.typ, m.declaredLen, 0, 0}
		binary.LittleEndian.PutUint16(hdr[2:], m.flags)
		out = append(out, hdr...)
		out = append(out, m.body...)
	}
	length := uint16(len(out))
	if f.overrideLength != nil {
		length = *f.overrideLength
	}
	binary.LittleEndian.PutUint16(out[22:], length)
	return out
}
