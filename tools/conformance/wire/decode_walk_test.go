package wire_test

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

func TestWalkMessagesOK(t *testing.T) {
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeOrderCancel, 32, func(b *wb.Body) { b.Pad(28) }).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).Bytes()
	f, fs := wire.Decode(raw, wire.MagicMBO)
	if len(fs) != 0 || len(f.Messages) != 2 {
		t.Fatalf("findings=%+v msgs=%d", fs, len(f.Messages))
	}
	if f.Messages[0].Type != wire.TypeOrderCancel || f.Messages[0].Length != 32 {
		t.Fatalf("msg0 wrong: %+v", f.Messages[0])
	}
}

func TestWalkCountMismatch(t *testing.T) {
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
		ForgeCount(5).Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if !has(fs, "FRAME.MSG_COUNT_RANGE") {
		t.Fatalf("expected count mismatch, got %+v", fs)
	}
}

func TestDeclaredLengthExceedsReceived_IsTransport(t *testing.T) {
	// Well-formed 16-byte heartbeat frame (received 40 bytes) but the header claims 200.
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
		ForgeLength(200).Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if !hasTransport(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("expected transport truncation, got %+v", fs)
	}
}

func TestMessageOverrunsDeclaredFrameLength_IsPublisherInvalid(t *testing.T) {
	// One full 52-byte OrderAdd (received 76 bytes) but frame length forged to 40,
	// so the message's declared length runs past the declared frame length.
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) { b.Pad(48) }).
		ForgeLength(40).Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if hasTransport(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("should be publisher-invalid, not transport: %+v", fs)
	}
	if !has(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("expected publisher-invalid overrun, got %+v", fs)
	}
}

// NOTE: `has` and `hasTransport` are already defined in decode_test.go (Task 5),
// same external test package `wire_test` — do NOT redefine them here (duplicate
// declaration). Just use them.

// --- common framing 0.2.0: 12-bit Message Length ---

func TestWalk12BitMessageLength(t *testing.T) {
	// A 424-byte message (the market-by-price BookDepth size) encodes as
	// bytes 1-2 = A8 01 little-endian, and is only walkable by a
	// decoder that reads the whole 12-bit word rather than byte 1 alone.
	raw := wb.Frame(wire.MagicMBO).Schema(wire.SchemaVersionCF2).
		Msg(0x40, 424, func(b *wb.Body) { b.Pad(420) }).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).Bytes()
	if raw[wire.FrameHeaderLen+1] != 0xA8 || raw[wire.FrameHeaderLen+2] != 0x01 {
		t.Fatalf("length not encoded as A8 01: % X", raw[wire.FrameHeaderLen:wire.FrameHeaderLen+4])
	}
	f, fs := wire.Decode(raw, wire.MagicMBO)
	if len(f.Messages) != 2 {
		t.Fatalf("walk failed: msgs=%d findings=%+v", len(f.Messages), fs)
	}
	if f.Messages[0].Length != 424 || f.Messages[1].Type != wire.TypeHeartbeat {
		t.Fatalf("messages wrong: %+v", f.Messages)
	}
	if has(fs, "FRAME.LENGTH_CONSISTENCY") || has(fs, "MSG.LENGTH_PER_TYPE") {
		t.Fatalf("unexpected structural finding: %+v", fs)
	}
}

func TestFlagsLiveAtOffset3(t *testing.T) {
	// 0.2.0 puts Flags at offset 3, after the contiguous length word. A 0.1.x
	// decoder reading Flags as a u16 at offset 2 would see bit 0 clear, which is
	// exactly why Schema Version gates the two layouts.
	raw := wb.Frame(wire.MagicMBO).
		MsgFlags(wire.TypeSnapshotOrder, 44, 0x01, func(b *wb.Body) { b.Pad(40) }).Bytes()
	hdr := raw[wire.FrameHeaderLen : wire.FrameHeaderLen+4]
	if hdr[1] != 44 || hdr[2] != 0x00 || hdr[3] != 0x01 {
		t.Fatalf("header not 0.2.0 layout: % X", hdr)
	}
	f, fs := wire.Decode(raw, wire.MagicMBO)
	if len(fs) != 0 || len(f.Messages) != 1 || f.Messages[0].Flags != 0x01 {
		t.Fatalf("findings=%+v msgs=%+v", fs, f.Messages)
	}
}

func TestLegacySchemaVersionIsFlagged(t *testing.T) {
	// A version-1 frame carries the 0.1.x header layout, whose Flags sits at a
	// different offset. This decoder implements 0.2.0 and must not walk it silently.
	raw := wb.Frame(wire.MagicMBO).Schema(wire.SchemaVersionLegacy).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if !has(fs, "FRAME.SCHEMA_VERSION") {
		t.Fatalf("expected legacy-layout finding, got %+v", fs)
	}
}
