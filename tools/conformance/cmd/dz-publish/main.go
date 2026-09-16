// Command dz-publish transmits a conformant Top-of-Book feed to a multicast group, indefinitely.
//
// It exists so a subscriber has something to consume where no venue publisher is available: an
// end-to-end path test, a dz-conformance smoke run, or a demo. It is not a venue feed and carries
// no venue data. The instrument is one synthetic ID and the prices oscillate.
//
// The datagrams are built with the same wirebuild helpers the conformance golden captures use, so
// the two cannot drift: if the spec's layout changes, both move together.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// The synthetic instrument. Source ID 1 is inside the conformant [1,1023] range and names no
// venue: `sources/spec.md` is the registry, and nothing here claims an entry in it.
const (
	channelID    = uint8(1)
	instrumentID = uint32(700)
	sourceID     = uint16(1)
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "dz-publish: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	group          string
	mktDataPort    int
	refDataPort    int
	iface          string
	ttl            int
	quoteEvery     time.Duration
	manifestEvery  time.Duration
	heartbeatEvery time.Duration
	duration       time.Duration
}

func run() error {
	var cfg config
	fs := flag.NewFlagSet("dz-publish", flag.ExitOnError)
	fs.StringVar(&cfg.group, "group", "", "multicast group to send to (required)")
	fs.IntVar(&cfg.mktDataPort, "mktdata-port", 0, "destination port for quotes and heartbeats (required)")
	fs.IntVar(&cfg.refDataPort, "refdata-port", 0, "destination port for the manifest cycle (required)")
	fs.StringVar(&cfg.iface, "interface", "", "outbound interface name; the route table decides when empty")
	fs.IntVar(&cfg.ttl, "ttl", 8, "multicast TTL; the default crosses a few hops and leaves the estate")
	// The spec's suggested cadences, which the checker's flags grade against. They are
	// recommendations rather than requirements, so they are flags here too.
	fs.DurationVar(&cfg.quoteEvery, "quote-interval", 100*time.Millisecond, "time between quotes")
	fs.DurationVar(&cfg.manifestEvery, "manifest-interval", time.Second, "time between manifest summaries")
	fs.DurationVar(&cfg.heartbeatEvery, "heartbeat-interval", 15*time.Second, "time between heartbeats")
	fs.DurationVar(&cfg.duration, "duration", 0, "stop after this long; runs until interrupted when zero")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if cfg.group == "" || cfg.mktDataPort == 0 || cfg.refDataPort == 0 {
		fs.Usage()
		return errors.New("--group, --mktdata-port and --refdata-port are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	return publish(ctx, cfg)
}
