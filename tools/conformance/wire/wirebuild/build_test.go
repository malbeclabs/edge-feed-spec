package wirebuild

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

func TestBuildFrameRoundsTrip(t *testing.T) {
	raw := Frame(wire.MagicMBO).Seq(5).ResetCount(1).
		Msg(wire.TypeHeartbeat, 16, func(b *Body) { b.U8(0).Pad(3).U64(123) }).
		Bytes()
	if len(raw) != 24+16 {
		t.Fatalf("len=%d want %d", len(raw), 24+16)
	}
	// magic little-endian (MBO 0x4444 → bytes 44 44)
	if raw[0] != 0x44 || raw[1] != 0x44 {
		t.Fatalf("magic bytes wrong: %x %x", raw[0], raw[1])
	}
	// frame length field at offset 22 (little-endian uint16: 40 = 0x28 → 28 00)
	if raw[22] != 40 || raw[23] != 0 {
		t.Fatalf("frame length wrong: %x %x", raw[22], raw[23])
	}
	// seq=5 at offset 4 (little-endian uint64: 05 00 00 00 00 00 00 00)
	if raw[4] != 5 || raw[5] != 0 {
		t.Fatalf("seq bytes wrong: %x %x", raw[4], raw[5])
	}
	// msg_count=1 at offset 20, reset_count=1 at offset 21
	if raw[20] != 1 {
		t.Fatalf("msg_count wrong: %d", raw[20])
	}
	if raw[21] != 1 {
		t.Fatalf("reset_count wrong: %d", raw[21])
	}
	// message header at offset 24: type=0x01, length=16, flags=0x0000
	if raw[24] != wire.TypeHeartbeat || raw[25] != 16 || raw[26] != 0 || raw[27] != 0 {
		t.Fatalf("msg header wrong: %x %x %x %x", raw[24], raw[25], raw[26], raw[27])
	}
	// body U64(123) at offset 28+4=32 (little-endian: 7b 00 00 00 00 00 00 00)
	if raw[32] != 123 || raw[33] != 0 {
		t.Fatalf("body U64 wrong: %x %x", raw[32], raw[33])
	}
}

// TestBuildFrameAsymmetricMagicLE verifies that little-endian encoding is correct
// for a non-palindrome magic value: MagicTOB = 0x445A → bytes 5A 44.
func TestBuildFrameAsymmetricMagicLE(t *testing.T) {
	raw := Frame(wire.MagicTOB).Bytes()
	if raw[0] != 0x5A || raw[1] != 0x44 {
		t.Fatalf("TOB magic wrong: got %x %x, want 5a 44", raw[0], raw[1])
	}
}
