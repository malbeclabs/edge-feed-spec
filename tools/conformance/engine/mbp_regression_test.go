package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// Reproduces what a real publisher put on its refdata port: `ManifestSummary`
// and `InstrumentDefinition` built with the snapshot header constructor, so
// application-header Flags bit 0 is SET on a port the spec requires it cleared
// on (malbeclabs/kalshi#62).
//
// This is the case that motivated registering the market-by-price feed here at
// all. The defect survived review, a byte-exact golden test and a round-trip
// test, because none of them compares against the port a message arrived on —
// and it was found by reading the spec beside the code rather than by a tool.
func TestRefdataSnapshotFlagRegression(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   uint8
		len   uint8
		flags uint16
		want  bool
	}{
		{"manifest as-shipped", wire.TypeManifest, 24, 0x0001, true},
		{"instrument def as-shipped", wire.TypeInstrumentDef, 80, 0x0001, true},
		{"manifest with bit 0 cleared", wire.TypeManifest, 24, 0x0000, false},
		{"instrument def with bit 0 cleared", wire.TypeInstrumentDef, 80, 0x0000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := wb.Frame(wire.MagicMBP).
				MsgFlags(tc.typ, tc.len, tc.flags, func(b *wb.Body) { b.Pad(int(tc.len) - 4) }).
				Bytes()
			got := fires(t, core.FeedMBP, wire.MagicMBP, raw, core.PortRefData,
				"MSG.SNAPSHOT_FLAG_MATCHES_PORT")
			if got != tc.want {
				t.Fatalf("violation reported = %v, want %v", got, tc.want)
			}
		})
	}
}

// The mirror case: a snapshot-port message with bit 0 *cleared* is equally wrong,
// and checking by port rather than by type is what catches both directions.
func TestSnapshotPortRequiresTheFlagSet(t *testing.T) {
	raw := wb.Frame(wire.MagicMBP).
		MsgFlags(wire.TypeSnapshotLevel, 32, 0x0000, func(b *wb.Body) { b.Pad(28) }).
		Bytes()
	if !fires(t, core.FeedMBP, wire.MagicMBP, raw, core.PortSnapshot, "MSG.SNAPSHOT_FLAG_MATCHES_PORT") {
		t.Fatal("a snapshot-port message with bit 0 clear must be reported")
	}
}

// `SnapshotBegin` is a prefix-superset: 36 bytes in market-by-order, 40 here with
// `Depth Bound` appended. A shared length table would have accepted the sibling's
// size and silently passed a truncated message.
func TestSnapshotBeginLengthIsFeedSpecific(t *testing.T) {
	if got := expectedMsgLen(core.FeedMBP, wire.TypeSnapshotBegin); got != 40 {
		t.Fatalf("MBP SnapshotBegin length = %d, want 40", got)
	}
	if got := expectedMsgLen(core.FeedMBO, wire.TypeSnapshotBegin); got != 36 {
		t.Fatalf("MBO SnapshotBegin length = %d, want 36", got)
	}
}

// `SnapshotLevel` (0x42) must not be confused with market-by-order's
// `SnapshotOrder` (0x21): the cross-spec policy forbids reassigning an ID to a
// different payload, so each feed knows only its own.
func TestSnapshotPayloadsDoNotCrossFeeds(t *testing.T) {
	if knownTypes(core.FeedMBP)[wire.TypeSnapshotOrder] {
		t.Error("MBP must not accept market-by-order's SnapshotOrder (0x21)")
	}
	if knownTypes(core.FeedMBO)[wire.TypeSnapshotLevel] {
		t.Error("MBO must not accept market-by-price's SnapshotLevel (0x42)")
	}
	if !knownTypes(core.FeedMBP)[wire.TypeSnapshotLevel] {
		t.Error("MBP must accept its own SnapshotLevel")
	}
}
