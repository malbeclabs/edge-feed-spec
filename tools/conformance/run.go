package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/input"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// magicFor returns the expected frame magic for the given feed.
func magicFor(feed core.Feed) uint16 { return engine.MagicFor(feed) }

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

// reportStarvedRules accounts for the rules this invocation's port flags make
// unreachable, at startup, before any traffic.
//
// **Why the CLI and not the engine.** Every other rule reports its own denominator
// from the stream (see engine/denominator.go), but a snapshot-driven rule with no
// `--snapshot-port` has no stream to report from: the code that would emit is never
// entered. Zero opportunities produce zero findings, and zero findings are exactly
// what a clean feed produces. The failure that motivated this is a two-step, and
// neither step looks wrong on its own:
//
//  1. Operator runs without `--snapshot-port`, gets exit 1 and a real violation.
//  2. They fix the publisher, re-run the same command, get exit 0 and an empty
//     report — and read it as a pass, while every snapshot rule was starved.
//
// So the fact is reported from the only place that knows it, which is the flags.
// NA is the status the catalog already defines for "rule not applicable (e.g.
// required port unbound)", and routing it through the reporter rather than a bespoke
// field means it lands in all three sinks at once: stderr, the JSON report's per-rule
// counts, and checks_total{result="na"}.
func reportStarvedRules(rep report.Reporter, opts RunOpts) {
	if opts.SnapshotPort != 0 {
		return
	}
	starved := core.SnapshotDrivenRules(opts.Cfg.Feed)
	if len(starved) == 0 {
		return // this feed has no snapshot-port rules; nothing is lost
	}

	// Loud, and on stderr regardless of --verbose: the whole point is that it must
	// reach an operator who is reading an otherwise-empty report as a pass.
	//
	// The wording names the trap rather than overstating it. The exit code is
	// deliberately unchanged — a two-port run is legitimate, and failing it would break
	// anyone deliberately doing one — so what matters is that exit 0 must not be read as
	// "these rules passed". They did not run.
	fmt.Fprintf(os.Stderr,
		"dz-conformance: WARNING --snapshot-port is not set, so %d %s rule(s) have no frames "+
			"to evaluate and are reported as na, NOT as passes. The exit code does not cover "+
			"them: %v\n",
		len(starved), opts.Cfg.Feed, starved)

	now := time.Now()
	for _, id := range starved {
		meta, ok := core.Lookup(id)
		if !ok {
			continue
		}
		rep.Record(core.Finding{
			RuleID:   id,
			Severity: meta.Severity,
			Status:   core.NA,
			Feed:     opts.Cfg.Feed,
			Port:     core.PortSnapshot,
			Detail:   "--snapshot-port is not set, so this rule has no frames to evaluate",
			At:       now,
		})
	}
}

// Run wires the full pipeline and returns an OS exit code (0 = pass, 1 = violation,
// 2 = the run errored; see the read-error note on report.Meta).
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
		promReporter = report.NewProm(reg, opts.Version, opts.Commit, opts.Cfg.Feed)
		rep = report.Multi{agg, logSink, promReporter}

		// Bind the metrics listener synchronously and fail fast. The metrics
		// endpoint is the live alerting surface, so a bind failure (e.g. the port
		// already in use) must surface as a startup error rather than be swallowed
		// into a silent run with no metrics.
		mux := http.NewServeMux()
		mux.Handle("/metrics", promReporter.Handler())
		mux.Handle("/healthz", promReporter.Healthz())
		ln, err := net.Listen("tcp", opts.MetricsAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dz-conformance: cannot bind --metrics-addr %s: %v\n", opts.MetricsAddr, err)
			return 2
		}
		srv := &http.Server{Handler: mux}
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
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
	defer func() { _ = src.Close() }()

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

	// Report, before a single frame is read, the rules this invocation cannot reach.
	reportStarvedRules(rep, opts)

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
		meta := report.Meta{Version: opts.Version, Commit: opts.Commit, Strict: opts.Cfg.Strict, ReadErr: readErr}
		if err := report.JSONReport(agg, opts.JSONReport, meta); err != nil {
			fmt.Fprintf(os.Stderr, "dz-conformance: json report: %v\n", err)
			reportErr = err
		}
	}

	if readErr != nil || reportErr != nil {
		return 2
	}
	return agg.ExitCode(opts.Cfg.Strict)
}
