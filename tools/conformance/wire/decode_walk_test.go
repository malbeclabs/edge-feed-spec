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
	// Length Low = 0xA8 with extension nibble 0x1, and is only walkable by a
	// decoder that assembles both halves. Schema 2 declares the extension.
	raw := wb.Frame(wire.MagicMBO).Schema(wire.SchemaVersionLong).
		Msg(0x40, 424, func(b *wb.Body) { b.Pad(420) }).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).Bytes()
	if raw[wire.FrameHeaderLen+1] != 0xA8 || raw[wire.FrameHeaderLen+3] != 0x01 {
		t.Fatalf("length not split as 0xA8/0x01: % X", raw[wire.FrameHeaderLen:wire.FrameHeaderLen+4])
	}
	f, fs := wire.Decode(raw, wire.MagicMBO)
	// Schema 2 on a short-message feed is flagged (Info), but the walk must succeed.
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

func TestShortMessagesAreByteIdenticalToOldFraming(t *testing.T) {
	// The 0.2.0 encoder must produce byte 3 == 0 for any message of 255 bytes or
	// fewer, which is exactly what a 0.1.x encoder wrote as the high byte of its
	// u16 Flags. This is the whole basis of the widening's wire compatibility.
	raw := wb.Frame(wire.MagicMBO).
		MsgFlags(wire.TypeSnapshotOrder, 44, 0x01, func(b *wb.Body) { b.Pad(40) }).Bytes()
	hdr := raw[wire.FrameHeaderLen : wire.FrameHeaderLen+4]
	if hdr[1] != 44 || hdr[2] != 0x01 || hdr[3] != 0x00 {
		t.Fatalf("short message header not 0.1.x-compatible: % X", hdr)
	}
	f, fs := wire.Decode(raw, wire.MagicMBO)
	if len(fs) != 0 || len(f.Messages) != 1 || f.Messages[0].Flags != 0x01 {
		t.Fatalf("findings=%+v msgs=%+v", fs, f.Messages)
	}
}

func TestLengthExtensionUnderSchema1IsFlagged(t *testing.T) {
	// Declaring schema 1 promises byte 3 is zero. Using the extension anyway would
	// mis-walk on any 8-bit decoder that trusted the promise.
	raw := wb.Frame(wire.MagicMBO).Schema(wire.SchemaVersionBase).
		Msg(0x40, 424, func(b *wb.Body) { b.Pad(420) }).Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if !has(fs, "FRAME.SCHEMA_VERSION") {
		t.Fatalf("expected schema/length inconsistency, got %+v", fs)
	}
}
