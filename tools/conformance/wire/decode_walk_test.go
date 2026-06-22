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
