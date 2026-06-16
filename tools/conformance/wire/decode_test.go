package wire_test

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

func TestDecodeHeaderOK(t *testing.T) {
	raw := wb.Frame(wire.MagicMBO).Seq(7).ResetCount(2).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(3).U64(99) }).Bytes()
	f, fs := wire.Decode(raw, wire.MagicMBO)
	if len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
	if f.Header.Sequence != 7 || f.Header.ResetCount != 2 || f.Header.MsgCount != 1 {
		t.Fatalf("header decoded wrong: %+v", f.Header)
	}
}

func TestDecodeFrameLengthGreaterThanReceived_IsTransport(t *testing.T) {
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(3).U64(0) }).
		ForgeLength(200). // claims 200, datagram is 40
		Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if !hasTransport(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("expected transport-corruption length finding, got %+v", fs)
	}
}

func TestDecodeFrameLengthLessThanReceived_IsPublisherInvalid(t *testing.T) {
	raw := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(3).U64(0) }).
		ForgeLength(30). // claims 30, datagram is 40 (10 trailing bytes)
		Bytes()
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if hasTransport(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("trailing bytes are publisher-invalid, not transport: %+v", fs)
	}
	if !has(fs, "FRAME.LENGTH_CONSISTENCY") {
		t.Fatalf("expected publisher-invalid length finding, got %+v", fs)
	}
}

func has(fs []wire.StructFinding, id string) bool {
	for _, f := range fs {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func hasTransport(fs []wire.StructFinding, id string) bool {
	for _, f := range fs {
		if f.RuleID == id && f.Transport {
			return true
		}
	}
	return false
}
