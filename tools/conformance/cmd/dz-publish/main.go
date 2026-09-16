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

// The synthetic instrument.
//
// `sourceID` is in the private range `sources/spec.md` reserves for internal testing, where
// subscribers must assume no meaning. An assigned ID would name a real matching engine: ID 1 is
// Hyperliquid, so traffic carrying it claims to describe that venue's activity.
const (
	channelID    = uint8(1)
	instrumentID = uint32(700)
	sourceID     = uint16(32768)
)

func main() {
	// A timed stop and an interrupt are both how this is meant to end, so neither is an error.
	// --duration expiring surfaces as DeadlineExceeded rather than Canceled.
	if err := run(); err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
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
	// A subscriber that joins after the bootstrap cycle has no definition for the instrument the
	// quotes name, so it cannot grade them until the next cycle comes round.
	definitionEvery time.Duration
	duration        time.Duration
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
	fs.DurationVar(&cfg.definitionEvery, "definition-interval", 30*time.Second, "time between instrument definition cycles, so a subscriber joining late becomes ready")
	fs.DurationVar(&cfg.duration, "duration", 0, "stop after this long; runs until interrupted when zero")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if cfg.group == "" || cfg.mktDataPort == 0 || cfg.refDataPort == 0 {
		fs.Usage()
		return errors.New("--group, --mktdata-port and --refdata-port are required")
	}
	// The two ports are what tells the two channel instances apart, so one port is not a
	// degenerate case of the model, it is outside it.
	if cfg.mktDataPort == cfg.refDataPort {
		return fmt.Errorf("--mktdata-port and --refdata-port are both %d; they key two channel instances and cannot be equal", cfg.mktDataPort)
	}
	if cfg.duration < 0 {
		return fmt.Errorf("--duration %s is negative; zero is the value that runs until interrupted", cfg.duration)
	}
	// time.NewTicker panics below zero, and a panic is a worse way to learn this than a message.
	for name, interval := range map[string]time.Duration{
		"--quote-interval":      cfg.quoteEvery,
		"--manifest-interval":   cfg.manifestEvery,
		"--heartbeat-interval":  cfg.heartbeatEvery,
		"--definition-interval": cfg.definitionEvery,
	} {
		if interval <= 0 {
			return fmt.Errorf("%s is %s; every interval has to be positive", name, interval)
		}
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
