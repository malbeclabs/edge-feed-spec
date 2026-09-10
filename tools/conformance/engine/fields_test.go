package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// instrDefMsg builds an InstrumentDefinition body of bodyLen bytes with
// instrumentID at body 0, and priceBound / manifestSeq written at the given body
// offsets, then decodes it back out through the real frame walk.
func instrDefMsg(t *testing.T, magic uint16, schema uint8, msgLen uint8,
	instrID uint32, priceBoundOff, manifestSeqOff int, priceBound uint8, manifestSeq uint16,
) wire.Message {
	t.Helper()
	bodyLen := int(msgLen) - wire.MsgHeaderLen
	body := make([]byte, bodyLen)
	body[0] = byte(instrID)
	body[1] = byte(instrID >> 8)
	body[2] = byte(instrID >> 16)
	body[3] = byte(instrID >> 24)
	body[priceBoundOff] = priceBound
	body[manifestSeqOff] = byte(manifestSeq)
	body[manifestSeqOff+1] = byte(manifestSeq >> 8)

	raw := wirebuild.Frame(magic).Schema(schema).Channel(0).Seq(1).
		Msg(wire.TypeInstrumentDef, msgLen, func(b *wirebuild.Body) {
			for _, x := range body {
				b.U8(x)
			}
		}).Bytes()

	f, fs := wire.Decode(raw, magic)
	for _, sf := range fs {
		if !sf.Transport {
			t.Fatalf("fixture frame is not clean: %s %s", sf.RuleID, sf.Detail)
		}
	}
	if len(f.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(f.Messages))
	}
	return f.Messages[0]
}

// FeedMBO here, not FeedTOB: TOB never reads Price Bound (see
// instrDefAllFields), so a FeedTOB fixture asserting a nonzero priceBound would
// fail against correct code. This test exists to cover the non-midpoint,
// priceBound-reading path at schema 3.
func TestInstrDefAllFieldsSchema3(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMBO, 3, 130, 0xAABBCCDD, 123, 124, 2, 0x1234)
	id, seq, _, bound, ok := instrDefAllFields(core.FeedMBO, 3, m)
	if !ok {
		t.Fatal("schema 3 must resolve")
	}
	if id != 0xAABBCCDD {
		t.Errorf("instrumentID = %#x, want 0xAABBCCDD", id)
	}
	if seq != 0x1234 {
		t.Errorf("manifestSeq = %#x, want 0x1234", seq)
	}
	if bound != 2 {
		t.Errorf("priceBound = %d, want 2", bound)
	}
}

// The regression this whole plan exists to prevent: a schema-1 message read with
// schema-3 offsets. Before the layout table, offsets 123/124 fell outside an
// 80-byte message's 76-byte body.
//
// FeedMBO here, not FeedTOB: see the comment on TestInstrDefAllFieldsSchema3.
func TestInstrDefAllFieldsSchema1(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMBO, 1, 80, 0x11223344, 73, 74, 1, 0x0777)
	id, seq, _, bound, ok := instrDefAllFields(core.FeedMBO, 1, m)
	if !ok {
		t.Fatal("schema 1 must resolve")
	}
	if id != 0x11223344 {
		t.Errorf("instrumentID = %#x, want 0x11223344", id)
	}
	if seq != 0x0777 {
		t.Errorf("manifestSeq = %#x, want 0x0777", seq)
	}
	if bound != 1 {
		t.Errorf("priceBound = %d, want 1", bound)
	}
}

// Reading past the body must report absence, never a zero that looks like data.
func TestInstrDefAllFieldsRejectsUnsupportedSchema(t *testing.T) {
	m := instrDefMsg(t, wire.MagicTOB, 1, 80, 1, 73, 74, 1, 1)
	if _, _, _, _, ok := instrDefAllFields(core.FeedTOB, 2, m); ok {
		t.Fatal("schema 2 has no layout and must not resolve")
	}
}

func TestInstrDefAllFieldsMidpoint(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMid, 1, 64, 7, 39, 56, 1, 9)
	id, seq, method, bound, ok := instrDefAllFields(core.FeedMidpoint, 1, m)
	if !ok {
		t.Fatal("midpoint schema 1 must resolve")
	}
	if id != 7 || seq != 9 || bound != 1 {
		t.Errorf("got id=%d seq=%d bound=%d, want 7/9/1", id, seq, bound)
	}
	_ = method // Default Method is exercised by the midpoint suite.
}

// FeedMBP must not read Price Bound: today's switch (which this table replaces)
// never read it for MBP, and this task must not change grading for live feeds.
// If this ever needs to change, that is a separate decision — see the comment on
// instrDefAllFields.
func TestInstrDefAllFieldsMBPExcludesPriceBound(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMBP, 3, 130, 1, 123, 124, 2, 9)
	_, _, _, bound, ok := instrDefAllFields(core.FeedMBP, 3, m)
	if !ok {
		t.Fatal("schema 3 must resolve")
	}
	if bound != 0 {
		t.Errorf("priceBound = %d, want 0 (MBP does not read Price Bound)", bound)
	}
}

// Default Method is a midpoint-only field: non-midpoint layouts carry
// DefaultMethod: -1, so instrDefAllFields never reads one for them. This pins
// the zero return directly, the other half of the exclusion tests above — a
// zero here must mean "not carried", not "read as zero by accident".
func TestInstrDefAllFieldsNonMidpointDefaultMethodIsZero(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMBO, 3, 130, 1, 123, 124, 2, 9)
	_, _, method, _, ok := instrDefAllFields(core.FeedMBO, 3, m)
	if !ok {
		t.Fatal("schema 3 must resolve")
	}
	if method != 0 {
		t.Errorf("defaultMethod = %d, want 0 (non-midpoint feeds do not carry Default Method)", method)
	}
}
