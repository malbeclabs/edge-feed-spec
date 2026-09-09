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

func TestDecodeMagicMismatch_NoWalkCascade(t *testing.T) {
	// A datagram whose magic doesn't match the bound feed is not a frame of
	// this feed; Decode must report only FRAME.MAGIC_MISMATCH and not cascade
	// derived findings (schema/length/walk) off untrusted bytes.
	raw := make([]byte, 60) // all-zero: magic 0x0000 ≠ MBO, and every other field is junk
	_, fs := wire.Decode(raw, wire.MagicMBO)
	if len(fs) != 1 || fs[0].RuleID != "FRAME.MAGIC_MISMATCH" {
		t.Fatalf("want exactly one FRAME.MAGIC_MISMATCH finding (no cascade), got %+v", fs)
	}
}

func TestSchemaSupported(t *testing.T) {
	cases := []struct {
		name  string
		magic uint16
		ver   uint8
		want  bool
	}{
		{"tob schema 3 is current", wire.MagicTOB, 3, true},
		{"tob schema 1 is the hyperliquid publisher", wire.MagicTOB, 1, true},
		{"tob schema 2 was never deployed", wire.MagicTOB, 2, false},
		{"tob schema 0 is not a version", wire.MagicTOB, 0, false},
		{"tob schema 4 does not exist yet", wire.MagicTOB, 4, false},
		{"mbo schema 1", wire.MagicMBO, 1, true},
		{"mbo schema 3", wire.MagicMBO, 3, true},
		{"mbp schema 3", wire.MagicMBP, 3, true},
		{"midpoint kept its 64-byte variant at 1", wire.MagicMid, 1, true},
		{"midpoint never widened to 3", wire.MagicMid, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wire.SchemaSupported(tc.magic, tc.ver); got != tc.want {
				t.Fatalf("SchemaSupported(0x%04X, %d) = %v, want %v", tc.magic, tc.ver, got, tc.want)
			}
		})
	}
}

func TestDecodeAcceptsSchema1TOB(t *testing.T) {
	raw := wb.Frame(wire.MagicTOB).Schema(1).Channel(0).Seq(1).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(11) }).
		Bytes()

	_, fs := wire.Decode(raw, wire.MagicTOB)
	for _, f := range fs {
		if f.RuleID == "FRAME.SCHEMA_VERSION" {
			t.Fatalf("schema 1 must not raise FRAME.SCHEMA_VERSION: %s", f.Detail)
		}
	}
}

func TestDecodeRejectsSchema2TOB(t *testing.T) {
	raw := wb.Frame(wire.MagicTOB).Schema(2).Channel(0).Seq(1).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(0).Pad(11) }).
		Bytes()

	_, fs := wire.Decode(raw, wire.MagicTOB)
	var found bool
	for _, f := range fs {
		if f.RuleID == "FRAME.SCHEMA_VERSION" {
			found = true
			if f.Transport {
				t.Fatal("an unsupported schema is a publisher fault, not transport corruption")
			}
		}
	}
	if !found {
		t.Fatal("schema 2 must raise FRAME.SCHEMA_VERSION")
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
