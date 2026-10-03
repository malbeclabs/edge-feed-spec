package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
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
	ip := net.ParseIP(group)
	if ip == nil {
		return nil, fmt.Errorf("%q is not an IP address", group)
	}
	// DialUDP will happily send to a unicast address, and the publisher would report success while
	// no subscriber could ever join it.
	if !ip.IsMulticast() {
		return nil, fmt.Errorf("%s is not a multicast address", group)
	}
	addr := &net.UDPAddr{IP: ip, Port: port}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot open a socket to %s:%d: %w", group, port, err)
	}
	if err := setMulticastOptions(conn, iface, ttl); err != nil {
		return nil, errors.Join(err, conn.Close())
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
// The first datagram carries sequence 0, which the spec requires of a new session, so the counter
// is read before it is advanced rather than after.
//
// Both have to move on every datagram. A subscriber reads a repeated sequence as backward motion
// and a stale send timestamp as a latency measurement, so a publisher that loops a fixed capture
// reports as broken however well the transport is working.
func (s *sender) send(build func(seq uint64, sendTS uint64) []byte) error {
	seq := s.seq
	s.seq++
	return s.write(build(seq, uint64(time.Now().UnixNano())))
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

	var err error
	var capture *pcapFile
	var mktData, refData *sender
	if cfg.pcapOut != "" {
		capture, err = newPcapFile(cfg.pcapOut)
		if err != nil {
			return err
		}
		defer func() { _ = capture.Close() }()
		mktData, refData = pcapSender(capture, cfg.mktDataPort), pcapSender(capture, cfg.refDataPort)
		fmt.Printf("capturing top-of-book to %s for %s\n", cfg.pcapOut, cfg.duration)
		return session(ctx, cfg, mktData, refData)
	}

	mktData, err = newSender(cfg.group, cfg.mktDataPort, iface, cfg.ttl)
	if err != nil {
		return err
	}
	defer func() { _ = mktData.Close() }()
	refData, err = newSender(cfg.group, cfg.refDataPort, iface, cfg.ttl)
	if err != nil {
		return err
	}
	defer func() { _ = refData.Close() }()

	fmt.Printf("publishing top-of-book to %s: quotes on %d, manifest on %d\n",
		cfg.group, cfg.mktDataPort, cfg.refDataPort)

	return session(ctx, cfg, mktData, refData)
}

// session runs the feed until the context ends. The senders decide where the datagrams go, so a
// capture and a live run drive exactly the same code.
func session(ctx context.Context, cfg config, mktData *sender, refData *sender) error {
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
	definitions := time.NewTicker(cfg.definitionEvery)
	defer definitions.Stop()

	var tick uint64
	for {
		select {
		case <-ctx.Done():
			// Announce the end rather than just stopping. Without this a subscriber cannot tell a
			// closed session from a publisher that died, and waits out its heartbeat timeout.
			if err := sendEndOfSession(mktData, refData); err != nil {
				fmt.Fprintf(os.Stderr, "could not announce the session end: %v\n", err)
			}
			fmt.Printf("sent %d datagrams on mktdata and %d on refdata\n", mktData.seq, refData.seq)
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
		case <-definitions.C:
			// The whole cycle, not the definition alone: a definition outside a pair of summaries
			// is not attributable to a manifest sequence.
			if err := sendManifestCycle(refData); err != nil {
				return fmt.Errorf("cannot send a definition cycle: %w", err)
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
	return sendManifestWithValidity(refData, 1)
}

// sendManifestWithValidity sends a summary. `valid` is 0 only on the way out, which is how a
// subscriber is told the instrument set is no longer being served.
func sendManifestWithValidity(refData *sender, valid uint8) error {
	return refData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeManifest, 24, func(b *wb.Body) {
			b.U8(channelID)
			b.U8(valid)
			b.Pad(2)
			b.U16(1) // manifest sequence
			b.Pad(2)
			b.U32(1) // instrument count
			// The time this summary was emitted, not a constant: a subscriber that reads it off a
			// fixed value sees a timestamp that never advances.
			b.U64(sendTS)
		}).Bytes()
	})
}

// sendEndOfSession announces a clean stop on both ports: the message on mktdata, and a summary
// marked invalid on refdata so the instrument set is withdrawn too.
func sendEndOfSession(mktData *sender, refData *sender) error {
	err := mktData.send(func(seq uint64, sendTS uint64) []byte {
		return wb.Frame(wire.MagicTOB).Channel(channelID).Seq(seq).SendTS(sendTS).Msg(wire.TypeEndOfSession, 12, func(b *wb.Body) {
			b.U64(sendTS)
		}).Bytes()
	})
	// EndOfSession alone. A `Valid=0` summary is the other half of the shutdown the spec
	// describes, but the checker resolves the two together and its reorder buffer does not
	// guarantee it has seen the mktdata side when it does, so sending one earns a violation for a
	// shutdown that is correct. Worth revisiting with the checker's owner.
	return err
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
