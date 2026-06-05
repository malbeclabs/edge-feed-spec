package wire

import "testing"

func TestQuoteRoundTrip(t *testing.T) {
	in := Quote{
		InstrumentID:    42,
		SourceID:        1,
		UpdateFlags:     UpdBidUpdated | UpdAskUpdated,
		SourceTimestamp: 1_700_000_000_000_000_000,
		BidPrice:        11337700,
		BidQuantity:     76699,
		AskPrice:        11339700,
		AskQuantity:     1154,
		BidSourceCount:  17,
		AskSourceCount:  3,
	}
	var b [LenQuote]byte
	in.Encode(b[:])

	mh, err := DecodeMsgHeader(b[:])
	if err != nil || mh.Type != TypeQuote || mh.Length != LenQuote {
		t.Fatalf("header = %+v err=%v", mh, err)
	}
	out, err := DecodeQuote(b[:])
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestInstrumentDefinitionRoundTrip(t *testing.T) {
	in := InstrumentDefinition{
		InstrumentID: 7, Symbol: "BTC", Leg1: "BTC", Leg2: "USD",
		AssetClass: 1, PriceExp: -2, QtyExp: -4, MarketModel: 1, ManifestSeq: 5,
	}
	var b [LenInstrumentDefinition]byte
	in.Encode(b[:])
	out, err := DecodeInstrumentDefinition(b[:])
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestFrameHeaderRoundTrip(t *testing.T) {
	in := FrameHeader{
		SchemaVersion: SchemaVersion, ChannelID: 2, SequenceNum: 99,
		SendTimestamp: 123456789, MsgCount: 3, ResetCount: 1, FrameLength: 84,
	}
	var b [FrameHeaderSize]byte
	in.Encode(b[:])
	out, err := DecodeFrameHeader(b[:])
	if err != nil {
		t.Fatal(err)
	}
	in.Magic = Magic // set by Encode
	if out != in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}
