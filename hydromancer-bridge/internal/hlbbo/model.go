// Package hlbbo defines the Hyperliquid / Hydromancer BBO wire types and the
// conversion from a DoubleZero Quote into that schema, and serves them over a
// websocket that emulates the Hyperliquid public WS endpoint so an existing
// Hydromancer client can connect unchanged.
package hlbbo

import (
	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

// Level is a Hyperliquid WsLevel: price, size, and order count, all at one side
// of the book. Encoded as null when that side has no resting liquidity.
type Level struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int    `json:"n"`
}

// Bbo is the Hyperliquid WsBbo data payload.
//
//	{"coin":"BTC","time":1754450974231,"bbo":[bid|null, ask|null]}
type Bbo struct {
	Coin string    `json:"coin"`
	Time uint64    `json:"time"` // milliseconds since epoch
	Bbo  [2]*Level `json:"bbo"`
}

// Envelope is the channel wrapper the Hyperliquid WS sends for data messages:
// {"channel":"bbo","data":{...}}.
type Envelope struct {
	Channel string `json:"channel"`
	Data    any    `json:"data"`
}

// Convert turns a DoubleZero Quote into a Hyperliquid Bbo using the instrument
// definition for symbol resolution and decimal scaling.
//
// coin defaults to the definition's Symbol; if Symbol is empty, Leg1 (base
// asset) is used. Source Timestamp (ns) is converted to milliseconds. A "gone"
// side becomes a null level.
func Convert(q wire.Quote, def wire.InstrumentDefinition) Bbo {
	coin := def.Symbol
	if coin == "" {
		coin = def.Leg1
	}

	b := Bbo{
		Coin: coin,
		Time: q.SourceTimestamp / 1_000_000, // ns -> ms
	}

	if !q.BidGone() {
		b.Bbo[0] = &Level{
			Px: wire.ScaleSigned(q.BidPrice, def.PriceExp),
			Sz: wire.ScaleUnsigned(q.BidQuantity, def.QtyExp),
			N:  int(q.BidSourceCount),
		}
	}
	if !q.AskGone() {
		b.Bbo[1] = &Level{
			Px: wire.ScaleSigned(q.AskPrice, def.PriceExp),
			Sz: wire.ScaleUnsigned(q.AskQuantity, def.QtyExp),
			N:  int(q.AskSourceCount),
		}
	}
	return b
}
