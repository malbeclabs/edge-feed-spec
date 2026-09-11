package engine

import "github.com/malbeclabs/edge-feed-spec/tools/conformance/core"

// instrDefLayout holds the body offsets of the InstrumentDefinition (0x02) fields
// this validator reads, for one (feed, schema version) pair. Offsets are relative
// to the message body, so they are the spec offset minus the 4-byte message
// header. An absent field is -1.
//
// The InstrumentDefinition layout is the only part of the wire format that moves
// between the schema versions this tool accepts, which is why one table covers
// multi-schema support for the whole engine.
//
// **The table says where a field is, not whether a feed reads it.** A row gives
// the offsets for its (feed, schema) pair; which of those fields a given feed
// actually consumes is the caller's decision, in instrDefAllFields. So a valid
// offset here is not a promise that anything reads it — PriceBound is 123 for
// every non-midpoint feed, and only MBO consumes it.
type instrDefLayout struct {
	MsgLen        uint8 // canonical Message Length, header included
	InstrumentID  int
	ManifestSeq   int
	PriceBound    int
	DefaultMethod int
}

// Body offsets for the three layouts. Read spec offsets from the tables named in
// each comment and subtract 4.
//
// **Schema 1's layout is not in the current spec files.** The 80-byte
// InstrumentDefinition was retired by the 2.0.0 and 3.0.0 MAJOR releases, and
// top-of-book/spec.md now documents only the 130-byte form. These offsets
// are transcribed from tag `top-of-book/v1.0.0`, section "0x02
// InstrumentDefinition (80 bytes)". Verify against that tag, not against HEAD.
//
// Per VERSIONING.md the two MAJOR steps were: 2.0.0 widened Symbol from char[16]
// to char[64] (80 -> 128 bytes), and 3.0.0 inserted Source ID (u16) after
// Instrument ID (128 -> 130). No publisher deployed schema 2, so the live gap
// between 1 and 3 is the sum of both steps and every field after Symbol sits 50
// bytes later. instrdef_test.go pins that delta.
var (
	// top-of-book/v1.0.0 "0x02 InstrumentDefinition (80 bytes)":
	// Instrument ID spec 4, Price Bound spec 77, Manifest Seq spec 78.
	instrDefSchema1 = instrDefLayout{MsgLen: 80, InstrumentID: 0, ManifestSeq: 74, PriceBound: 73, DefaultMethod: -1}

	// top-of-book/spec.md and market-by-order/spec.md at schema 3:
	// Instrument ID spec 4, Price Bound spec 127, Manifest Seq spec 128.
	instrDefSchema3 = instrDefLayout{MsgLen: 130, InstrumentID: 0, ManifestSeq: 124, PriceBound: 123, DefaultMethod: -1}

	// midpoint/spec.md, still at 1.0.x with the slimmed 64-byte variant:
	// Instrument ID spec 4, Default Method spec 42, Price Bound spec 43, Manifest Seq spec 60.
	instrDefMidpoint = instrDefLayout{MsgLen: 64, InstrumentID: 0, ManifestSeq: 56, PriceBound: 39, DefaultMethod: 38}
)

// instrDefLayoutFor returns the InstrumentDefinition layout for a feed at a schema
// version, and whether the pair is supported. An unsupported pair returns false;
// the caller must not fall back to a default layout, because decoding 130-byte
// offsets out of an 80-byte message reads garbage rather than failing.
func instrDefLayoutFor(feed core.Feed, schema uint8) (instrDefLayout, bool) {
	if feed == core.FeedMidpoint {
		if schema == 1 {
			return instrDefMidpoint, true
		}
		return instrDefLayout{}, false
	}
	switch schema {
	case 1:
		return instrDefSchema1, true
	case 3:
		return instrDefSchema3, true
	default:
		return instrDefLayout{}, false
	}
}
