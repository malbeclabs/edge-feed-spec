// Command simulator publishes a synthetic DoubleZero top-of-book feed over UDP
// multicast so the bridge can be exercised end-to-end without a real feed. It
// emits InstrumentDefinition + ManifestSummary on the reference-data port and a
// stream of random-walk Quote messages on the market-data port.
package main

import (
	"flag"
	"log"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

type instrument struct {
	def   wire.InstrumentDefinition
	price int64 // current mid in scaled price units
}

func main() {
	group := flag.String("group", "239.255.0.1", "multicast group")
	mktPort := flag.Int("mkt", 5000, "market-data port")
	refPort := flag.Int("ref", 5001, "reference-data port")
	iface := flag.String("iface", "", "outbound multicast interface name (optional)")
	rate := flag.Int("rate", 20, "quotes per second per instrument")
	syms := flag.String("instruments", "BTC,ETH,SOL", "comma-separated coin symbols")
	flag.Parse()

	mkt, err := dial(*group, *mktPort, *iface)
	if err != nil {
		log.Fatalf("dial mkt: %v", err)
	}
	ref, err := dial(*group, *refPort, *iface)
	if err != nil {
		log.Fatalf("dial ref: %v", err)
	}

	instruments := buildInstruments(strings.Split(*syms, ","))

	pub := &publisher{mkt: mkt, ref: ref, instruments: instruments}
	go pub.refdataLoop()
	pub.marketDataLoop(*rate)
}

func buildInstruments(syms []string) []*instrument {
	// Plausible starting mids in scaled units (PriceExp = -2 => cents).
	seed := map[string]int64{"BTC": 11337700, "ETH": 350000, "SOL": 15000}
	out := make([]*instrument, 0, len(syms))
	for i, s := range syms {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		px := seed[s]
		if px == 0 {
			px = 10000
		}
		out = append(out, &instrument{
			def: wire.InstrumentDefinition{
				InstrumentID: uint32(i + 1),
				Symbol:       s,
				Leg1:         s,
				Leg2:         "USD",
				AssetClass:   1, // crypto spot
				PriceExp:     -2,
				QtyExp:       -4,
				MarketModel:  1, // CLOB
				ManifestSeq:  1,
			},
			price: px,
		})
	}
	return out
}

type publisher struct {
	mkt, ref    *net.UDPConn
	instruments []*instrument
	mktSeq      uint64
	refSeq      uint64
	resetCnt    uint8
}

// refdataLoop emits a ManifestSummary every second and a full pass of
// InstrumentDefinitions every few seconds (compressed from the spec's 30s for a
// snappier demo).
func (p *publisher) refdataLoop() {
	manifest := time.NewTicker(1 * time.Second)
	defs := time.NewTicker(5 * time.Second)
	p.sendDefs()
	p.sendManifest()
	for {
		select {
		case <-manifest.C:
			p.sendManifest()
		case <-defs.C:
			p.sendDefs()
		}
	}
}

func (p *publisher) sendDefs() {
	for _, in := range p.instruments {
		var frame [wire.FrameHeaderSize + wire.LenInstrumentDefinition]byte
		in.def.Encode(frame[wire.FrameHeaderSize:])
		p.writeFrame(p.ref, &p.refSeq, frame[:], 1)
	}
}

func (p *publisher) sendManifest() {
	m := wire.ManifestSummary{
		Valid:           1,
		ManifestSeq:     1,
		InstrumentCount: uint32(len(p.instruments)),
		Timestamp:       uint64(time.Now().UnixNano()),
	}
	var frame [wire.FrameHeaderSize + wire.LenManifestSummary]byte
	m.Encode(frame[wire.FrameHeaderSize:])
	p.writeFrame(p.ref, &p.refSeq, frame[:], 1)
}

// marketDataLoop publishes a random-walk quote for each instrument at the given
// per-instrument rate.
func (p *publisher) marketDataLoop(rate int) {
	if rate < 1 {
		rate = 1
	}
	interval := time.Second / time.Duration(rate)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	log.Printf("publishing %d instruments at %d quotes/sec each", len(p.instruments), rate)
	for range tick.C {
		for _, in := range p.instruments {
			p.sendQuote(in)
		}
	}
}

func (p *publisher) sendQuote(in *instrument) {
	// Random walk the mid by up to +/-0.05%.
	step := int64(float64(in.price) * (rand.Float64() - 0.5) * 0.001)
	in.price += step
	if in.price < 1 {
		in.price = 1
	}
	// Half-tick spread, one tick = 1 scaled unit here.
	spread := in.price / 5000
	if spread < 1 {
		spread = 1
	}
	now := uint64(time.Now().UnixNano())
	q := wire.Quote{
		InstrumentID:    in.def.InstrumentID,
		SourceID:        1, // Hyperliquid
		UpdateFlags:     wire.UpdBidUpdated | wire.UpdAskUpdated,
		SourceTimestamp: now,
		BidPrice:        in.price - spread,
		BidQuantity:     uint64(1000 + rand.Intn(50000)), // scaled qty (QtyExp -4)
		AskPrice:        in.price + spread,
		AskQuantity:     uint64(1000 + rand.Intn(50000)),
		BidSourceCount:  uint16(1 + rand.Intn(20)),
		AskSourceCount:  uint16(1 + rand.Intn(20)),
	}
	var frame [wire.FrameHeaderSize + wire.LenQuote]byte
	q.Encode(frame[wire.FrameHeaderSize:])
	p.writeFrame(p.mkt, &p.mktSeq, frame[:], 1)
}

// writeFrame fills in the frame header and sends the datagram.
func (p *publisher) writeFrame(conn *net.UDPConn, seq *uint64, frame []byte, msgCount uint8) {
	h := wire.FrameHeader{
		SchemaVersion: wire.SchemaVersion,
		ChannelID:     0,
		SequenceNum:   *seq,
		SendTimestamp: uint64(time.Now().UnixNano()),
		MsgCount:      msgCount,
		ResetCount:    p.resetCnt,
		FrameLength:   uint16(len(frame)),
	}
	h.Encode(frame)
	*seq++
	if _, err := conn.Write(frame); err != nil {
		log.Printf("write frame: %v", err)
	}
}

func dial(group string, port int, ifaceName string) (*net.UDPConn, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(group), Port: port}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	if ifaceName != "" {
		// Best-effort: bind the multicast egress interface if requested.
		if ni, err := net.InterfaceByName(ifaceName); err == nil {
			_ = ni // DialUDP picks the route; interface binding is OS-dependent.
		}
	}
	return conn, nil
}
