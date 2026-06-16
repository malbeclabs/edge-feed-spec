package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/input"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// magicFor returns the expected frame magic for the given feed.
func magicFor(feed core.Feed) uint16 {
	switch feed {
	case core.FeedTOB:
		return wire.MagicTOB
	case core.FeedMidpoint:
		return wire.MagicMid
	default: // FeedMBO
		return wire.MagicMBO
	}
}

// buildPortMap constructs the UDP-port → core.Port map from the provided opts.
// Ports that are zero are not added to the map.
func buildPortMap(opts RunOpts) map[int]core.Port {
	pm := make(map[int]core.Port)
	if opts.MktDataPort != 0 {
		pm[opts.MktDataPort] = core.PortMktData
	}
	if opts.RefDataPort != 0 {
		pm[opts.RefDataPort] = core.PortRefData
	}
	if opts.SnapshotPort != 0 {
		pm[opts.SnapshotPort] = core.PortSnapshot
	}
	return pm
}

// buildSource constructs either a PcapSource (replay) or a MulticastSource (live).
// It returns an error if no ports are configured (which would produce an empty capture).
func buildSource(opts RunOpts) (input.Source, error) {
	pm := buildPortMap(opts)
	if len(pm) == 0 {
		return nil, fmt.Errorf("no ports configured: set at least one of --mktdata-port, --refdata-port, --snapshot-port")
	}
	if opts.PcapPath != "" {
		return input.NewPcapSource(opts.PcapPath, pm)
	}
	// Live multicast capture: require --group.
	if opts.Group == "" {
		return nil, fmt.Errorf("--group is required for live capture (or use --pcap for replay)")
	}
	// Invert the port map to logical→UDP.
	logicalPorts := make(map[core.Port]int, len(pm))
	for udpPort, logPort := range pm {
		logicalPorts[logPort] = udpPort
	}
	cfg := input.MulticastConfig{
		Group:     net.ParseIP(opts.Group),
		Ports:     logicalPorts,
		Interface: opts.Interface,
	}
	return input.NewMulticastSource(cfg)
}

// Run wires the full pipeline and returns an OS exit code (0 = pass, 1 = violation).
func Run(opts RunOpts) int {
	magic := magicFor(opts.Cfg.Feed)

	// --- reporters ---
	agg := &report.Aggregator{}
	logLevel := slog.LevelWarn
	if opts.Verbose {
		logLevel = slog.LevelInfo
	}
	logHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	logSink := report.NewSlogSink(slog.New(logHandler), opts.LogThrottle)

	var rep report.Reporter
	var promReporter *report.Prom
	if opts.MetricsAddr != "" {
		reg := prometheus.NewRegistry()
		promReporter = report.NewProm(reg, version, "", opts.Cfg.Feed)
		rep = report.Multi{agg, logSink, promReporter}

		// Serve metrics in the background; errors are non-fatal.
		mux := http.NewServeMux()
		mux.Handle("/metrics", promReporter.Handler())
		mux.Handle("/healthz", promReporter.Healthz())
		srv := &http.Server{Addr: opts.MetricsAddr, Handler: mux}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "metrics server: %v\n", err)
			}
		}()
	} else {
		rep = report.Multi{agg, logSink}
	}

	// --- source ---
	src, err := buildSource(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dz-conformance: open source: %v\n", err)
		return 2
	}
	defer src.Close()

	// --- source registry ---
	if opts.SourceRegistry != "" {
		reg, err := engine.LoadSourceRegistry(opts.SourceRegistry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dz-conformance: source registry: %v\n", err)
			return 2
		}
		opts.Cfg.SourceRegistry = reg
	} else {
		opts.Cfg.SourceRegistry = engine.DefaultSourceRegistry()
	}

	// --- engine ---
	eng := engine.New(opts.Cfg, rep)

	// --- signal handling for live captures ---
	done := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
		case <-done:
		}
		src.Close() //nolint:errcheck // best-effort on signal
	}()

	// --- main loop ---
	var readErr error
	for {
		dg, ok, err := src.Next()
		if err != nil {
			fmt.Fprintf(os.Stderr, "dz-conformance: read: %v\n", err)
			readErr = err
			break
		}
		if !ok {
			break
		}
		frame, sf := wire.Decode(dg.Raw, magic)
		eng.Process(frame, dg.Port, sf)
	}
	close(done)
	signal.Stop(sigs)

	// --- end-of-run (always, even after a read error) ---
	eng.Flush()
	eng.EndRun()

	// --- JSON report ---
	var reportErr error
	if opts.JSONReport != "" {
		if err := report.JSONReport(agg, opts.JSONReport); err != nil {
			fmt.Fprintf(os.Stderr, "dz-conformance: json report: %v\n", err)
			reportErr = err
		}
	}

	if readErr != nil || reportErr != nil {
		return 2
	}
	return agg.ExitCode(opts.Cfg.Strict)
}
