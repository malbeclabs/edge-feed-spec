package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

func TestInstrDefLayoutFor(t *testing.T) {
	cases := []struct {
		name          string
		feed          core.Feed
		schema        uint8
		ok            bool
		msgLen        uint8
		manifestSeq   int
		priceBound    int
		defaultMethod int
	}{
		// Schema 3: the current five-feed layout.
		{"tob schema 3", core.FeedTOB, 3, true, 130, 124, 123, -1},
		{"mbo schema 3", core.FeedMBO, 3, true, 130, 124, 123, -1},
		{"mbp schema 3", core.FeedMBP, 3, true, 130, 124, 123, -1},
		// Schema 1: the 80-byte layout, recovered from tag top-of-book/v1.0.0.
		{"tob schema 1", core.FeedTOB, 1, true, 80, 74, 73, -1},
		{"mbo schema 1", core.FeedMBO, 1, true, 80, 74, 73, -1},
		// Midpoint never left its own 64-byte variant.
		{"midpoint schema 1", core.FeedMidpoint, 1, true, 64, 56, 39, 38},
		// Not supported.
		{"tob schema 2 was never deployed", core.FeedTOB, 2, false, 0, 0, 0, 0},
		{"midpoint schema 3 does not exist", core.FeedMidpoint, 3, false, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := instrDefLayoutFor(tc.feed, tc.schema)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.MsgLen != tc.msgLen {
				t.Errorf("MsgLen = %d, want %d", got.MsgLen, tc.msgLen)
			}
			if got.ManifestSeq != tc.manifestSeq {
				t.Errorf("ManifestSeq = %d, want %d", got.ManifestSeq, tc.manifestSeq)
			}
			if got.PriceBound != tc.priceBound {
				t.Errorf("PriceBound = %d, want %d", got.PriceBound, tc.priceBound)
			}
			if got.DefaultMethod != tc.defaultMethod {
				t.Errorf("DefaultMethod = %d, want %d", got.DefaultMethod, tc.defaultMethod)
			}
		})
	}
}

// The two layouts differ by exactly the two MAJOR changes VERSIONING.md records:
// schema 2 widened Symbol from char[16] to char[64] (+48) and schema 3 inserted
// Source ID after Instrument ID (+2). Every field after Symbol therefore sits 50
// bytes later in schema 3 than in schema 1. This pins that arithmetic so a
// hand-edited offset cannot drift without failing here.
func TestSchema1And3DifferByFiftyBytesAfterSymbol(t *testing.T) {
	s1, ok1 := instrDefLayoutFor(core.FeedTOB, 1)
	s3, ok3 := instrDefLayoutFor(core.FeedTOB, 3)
	if !ok1 || !ok3 {
		t.Fatal("both TOB layouts must resolve")
	}
	if d := s3.ManifestSeq - s1.ManifestSeq; d != 50 {
		t.Errorf("ManifestSeq delta = %d, want 50", d)
	}
	if d := s3.PriceBound - s1.PriceBound; d != 50 {
		t.Errorf("PriceBound delta = %d, want 50", d)
	}
	if d := int(s3.MsgLen) - int(s1.MsgLen); d != 50 {
		t.Errorf("MsgLen delta = %d, want 50", d)
	}
	if s1.InstrumentID != s3.InstrumentID {
		t.Error("Instrument ID precedes both insertions and must not move")
	}
}

// The layout table and wire's accepted set must agree. A frame wire accepts but
// the layout table cannot resolve would reach the field accessors with no offsets,
// and a layout for a version wire rejects is dead code.
func TestLayoutTableAgreesWithWire(t *testing.T) {
	for _, feed := range []core.Feed{core.FeedTOB, core.FeedMidpoint, core.FeedMBO, core.FeedMBP} {
		magic := MagicFor(feed)
		for ver := uint8(0); ver < 8; ver++ {
			_, layoutOK := instrDefLayoutFor(feed, ver)
			wireOK := wire.SchemaSupported(magic, ver)
			if layoutOK != wireOK {
				t.Errorf("feed %s schema %d: layout=%v wire=%v", feed, ver, layoutOK, wireOK)
			}
		}
	}
}
