package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// sender holds one destination port and its own sequence counter.
//
// A channel instance is keyed on (source IP address, Channel ID, destination port), so the two
// ports are two instances and each owns its own sequence series. Sharing one counter between them
// would look to a subscriber like two publishers alternating on one series, which reads as
// backward motion and latches its verifiability window.
type sender struct {
	// write takes the finished datagram. A socket in the binary and a recorder in the tests, so
	// the sequence counter below is the one the tests drive rather than a copy of it. Live
	// multicast is not available on every machine a test runs on.
	write func([]byte) error
	close func() error
	seq   uint64
}

func newSender(group string, port int, iface *net.Interface, ttl int) (*sender, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(group), Port: port}
	if addr.IP == nil {
		return nil, fmt.Errorf("%q is not an IP address", group)
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot open a socket to %s:%d: %w", group, port, err)
	}
	if err := setMulticastOptions(conn, iface, ttl); err != nil {
		conn.Close()
		return nil, err
	}
	return &sender{
		write: func(datagram []byte) error {
			_, err := conn.Write(datagram)
			return err
		},
		close: conn.Close,
	}, nil
}

// send stamps the next sequence number and the send time, then transmits.
//
// The header chain is repeated at each call site rather than factored into a helper, because
// `wirebuild.Frame` returns an unexported type that no signature here can name.
//
// Both have to move on every datagram. A subscriber reads a repeated sequence as backward motion
// and a stale send timestamp as a latency measurement, so a publisher that loops a fixed capture
// reports as broken however well the transport is working.
func (s *sender) send(build func(seq uint64, sendTS uint64) []byte) error {
	s.seq++
	return s.write(build(s.seq, uint64(time.Now().UnixNano())))
}

func (s *sender) Close() error { return s.close() }

func publish(ctx context.Context, cfg config) error {
	var iface *net.Interface
	if cfg.iface != "" {
		var err error
		iface, err = net.InterfaceByName(cfg.iface)
		if err != nil {
			return fmt.Errorf("cannot use interface %q: %w", cfg.iface, err)
		}
	}

	mktData, err := newSender(cfg.group, cfg.mktDataPort, iface, cfg.ttl)
	if err != nil {
		return err
	}
	defer mktData.Close()
	refData, err := newSender(cfg.group, cfg.refDataPort, iface, cfg.ttl)
	if err != nil {
		return err
	}
	defer refData.Close()

	fmt.Printf("publishing top-of-book to %s: quotes on %d, manifest on %d\n",
		cfg.group, cfg.mktDataPort, cfg.refDataPort)

	// The bootstrap cycle first, because a subscriber cannot grade a quote for an instrument it
	// has no definition for: it reports the quote as unverifiable rather than conformant.
	if err := sendManifestCycle(refData); err != nil {
		return err
	}

	quotes := time.NewTicker(cfg.quoteEvery)
	defer quotes.Stop()
	manifests := time.NewTicker(cfg.manifestEvery)
	defer manifests.Stop()
	heartbeats := time.NewTicker(cfg.heartbeatEvery)
	defer heartbeats.Stop()

	var tick uint64
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("sent %d quote datagrams and %d on refdata\n", mktData.seq, refData.seq)
			return ctx.Err()
		case <-quotes.C:
			tick++
			if err := sendQuote(mktData, tick); err != nil {
				return fmt.Errorf("cannot send a quote: %w", err)
			}
		case <-manifests.C:
			if err := sendManifest(refData); err != nil {
				return fmt.Errorf("cannot send a manifest: %w", err)
			}
		case <-heartbeats.C:
			if err := sendHeartbeat(mktData); err != nil {
				return fmt.Errorf("cannot send a heartbeat: %w", err)
			}
		}
	}
}

// sendManifestCycle sends manifest, definition, manifest.
//
// The definition has to sit between two summaries: a subscriber treats the pair as the boundary of
// one cycle, and a definition outside one is not attributable to a manifest sequence.
func sendManifestCycle(refData *sender) error {
	if err := sendManifest(refData); err != nil {
		return err
	}
	if err := sendInstrumentDefinition(refData); err != nil {
		return err
	}
	return sendManifest(refData)
}

func sendManifest(refData *sender) error {
	return refData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeManifest, 24, func(b *wb.Body) {
			b.U8(channelID)
			b.U8(1) // valid
			b.Pad(2)
			b.U16(1) // manifest sequence
			b.Pad(2)
			b.U32(1) // instrument count
			b.U64(100_000)
		}).Bytes()
	})
}

func sendInstrumentDefinition(refData *sender) error {
	return refData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeInstrumentDef, 130, func(b *wb.Body) {
			b.U32(instrumentID)
			b.U16(sourceID)
			b.Pad(118)
			b.U16(1) // the manifest sequence this definition belongs to
		}).Bytes()
	})
}

// sendQuote sends one two-sided quote. The prices walk so a subscriber sees movement rather than a
// constant, which is what makes a stuck publisher visible.
func sendQuote(mktData *sender, tick uint64) error {
	bid := int64(100 + tick%10)
	return mktData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeQuote, 60, func(b *wb.Body) {
			b.U32(instrumentID)
			b.U16(sourceID)
			b.U8(0x03) // both sides updated
			b.U8(0)
			b.U64(uint64(time.Now().UnixNano()))
			b.I64(bid)
			b.U64(10)
			b.I64(bid + 100) // ask above bid, so the book is never crossed
			b.U64(10)
			b.U16(1)
			b.U16(1)
			b.Pad(4)
		}).Bytes()
	})
}

func sendHeartbeat(mktData *sender) error {
	return mktData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) {
			b.U8(channelID)
			b.Pad(3)
			b.U64(uint64(time.Now().UnixNano()))
		}).Bytes()
	})
}
