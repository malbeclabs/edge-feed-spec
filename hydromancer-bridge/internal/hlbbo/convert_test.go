package hlbbo

import (
	"encoding/json"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

func TestConvert(t *testing.T) {
	def := wire.InstrumentDefinition{
		InstrumentID: 1, Symbol: "BTC", Leg1: "BTC", PriceExp: -2, QtyExp: -4,
	}
	q := wire.Quote{
		InstrumentID:    1,
		UpdateFlags:     wire.UpdBidUpdated | wire.UpdAskUpdated,
		SourceTimestamp: 1_754_450_974_231_000_000, // ns
		BidPrice:        11337700,
		BidQuantity:     76699,
		AskPrice:        11339700,
		AskQuantity:     1154,
		BidSourceCount:  17,
		AskSourceCount:  3,
	}
	b := Convert(q, def)

	if b.Coin != "BTC" {
		t.Errorf("coin = %q", b.Coin)
	}
	if b.Time != 1_754_450_974_231 {
		t.Errorf("time = %d, want ms", b.Time)
	}
	if b.Bbo[0] == nil || b.Bbo[0].Px != "113377" || b.Bbo[0].Sz != "7.6699" || b.Bbo[0].N != 17 {
		t.Errorf("bid = %+v", b.Bbo[0])
	}
	if b.Bbo[1] == nil || b.Bbo[1].Px != "113397" || b.Bbo[1].N != 3 {
		t.Errorf("ask = %+v", b.Bbo[1])
	}
}

func TestConvertGoneSide(t *testing.T) {
	def := wire.InstrumentDefinition{Symbol: "ETH", PriceExp: -2, QtyExp: -4}
	q := wire.Quote{
		UpdateFlags: wire.UpdBidGone,
		BidPrice:    0,
		AskPrice:    350000,
		AskQuantity: 5000,
	}
	b := Convert(q, def)
	if b.Bbo[0] != nil {
		t.Errorf("bid should be null, got %+v", b.Bbo[0])
	}
	if b.Bbo[1] == nil {
		t.Fatal("ask should be present")
	}

	// The gone side must serialise as JSON null.
	out, _ := json.Marshal(Envelope{Channel: "bbo", Data: b})
	if got := string(out); got != `{"channel":"bbo","data":{"coin":"ETH","time":0,"bbo":[null,{"px":"3500","sz":"0.5","n":0}]}}` {
		t.Errorf("json = %s", got)
	}
}
