package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
)

// version and commit are set at build time via -ldflags
// (-X main.version=… -X main.commit=…). They label the build_info metric.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	fs := flag.NewFlagSet("dz-conformance", flag.ExitOnError)

	// Feed selection
	feedStr := fs.String("feed", "mbo", "feed to monitor: tob, midpoint, or mbo")

	// Network binding
	group := fs.String("group", "", "multicast group address (required for live capture)")
	mktdataPort := fs.Int("mktdata-port", 0, "UDP port for market-data feed")
	refdataPort := fs.Int("refdata-port", 0, "UDP port for reference-data feed")
	snapshotPort := fs.Int("snapshot-port", 0, "UDP port for snapshot feed (MBO only)")
	iface := fs.String("interface", "", "network interface for live multicast capture")

	// Replay
	pcapPath := fs.String("pcap", "", "replay a .pcap file instead of live capture")

	// Observability
	metricsAddr := fs.String("metrics-addr", "", "serve Prometheus metrics on this addr (off by default; bind non-public, e.g. 127.0.0.1:9090)")
	sourceRegistry := fs.String("source-registry", "", "path to a source-registry JSON override (default: embedded pinned registry)")

	// Engine behaviour
	strict := fs.Bool("strict", false, "treat should-violations as exit-code failures")
	oracleConfirmCycles := fs.Int("oracle-confirm-cycles", 2, "oracle cycles before Suspected→Violation")
	reorderWindow := fs.Int("reorder-window", 8, "per-port frame reorder buffer size (frames)")

	// Conditional rule expectations (zero = downgrade; non-zero = enforce)
	expectManifestCadence := fs.Duration("expect-manifest-cadence", 0, "expected manifest cadence (e.g. 1s); enables REFDATA.MANIFEST_CADENCE")
	expectDefinitionCycle := fs.Duration("expect-definition-cycle", 0, "expected definition-cycle duration; enables REFDATA.DEFINITION_CYCLE_COVERAGE")
	expectHeartbeat := fs.Duration("expect-heartbeat", 0, "expected heartbeat interval; enables HEARTBEAT.CADENCE")
	// expect-snapshot-cycle: snap-cycle rule wired in Phase 3; accepted here for forward compatibility.
	expectSnapshotCycle := fs.Duration("expect-snapshot-cycle", 0, "expected snapshot cycle duration (wired in Phase 3)")

	// Output
	jsonReport := fs.String("json-report", "", "write JSON findings report to this path")
	verbose := fs.Bool("v", false, "verbose logging (surface Unverifiable findings at INFO)")
	logThrottle := fs.Duration("log-throttle", time.Second, "min interval between identical (rule,status) log lines for long-running use; 0 disables (metrics always count every finding)")
	showVersion := fs.Bool("version", false, "print version and exit")

	_ = fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	var feed core.Feed
	switch *feedStr {
	case "tob":
		feed = core.FeedTOB
	case "midpoint":
		feed = core.FeedMidpoint
	case "mbo":
		feed = core.FeedMBO
	default:
		fmt.Fprintf(os.Stderr, "dz-conformance: unknown feed %q; must be tob, midpoint, or mbo\n", *feedStr)
		os.Exit(2)
	}

	cfg := engine.Config{
		Feed:                  feed,
		Strict:                *strict,
		OracleConfirmCycles:   *oracleConfirmCycles,
		ReorderWindow:         *reorderWindow,
		ExpectManifestCadence: *expectManifestCadence,
		ExpectDefinitionCycle: *expectDefinitionCycle,
		ExpectHeartbeat:       *expectHeartbeat,
		ExpectSnapshotCycle:   *expectSnapshotCycle,
		SourceRegistry:        nil, // set in Run() from --source-registry or DefaultSourceRegistry
	}

	opts := RunOpts{
		Cfg:            cfg,
		Group:          *group,
		MktDataPort:    *mktdataPort,
		RefDataPort:    *refdataPort,
		SnapshotPort:   *snapshotPort,
		Interface:      *iface,
		PcapPath:       *pcapPath,
		MetricsAddr:    *metricsAddr,
		SourceRegistry: *sourceRegistry,
		JSONReport:     *jsonReport,
		Verbose:        *verbose,
		LogThrottle:    *logThrottle,
	}

	code := Run(opts)
	os.Exit(code)
}

// RunOpts collects all CLI-derived options that Run needs.
type RunOpts struct {
	Cfg          engine.Config
	Group        string
	MktDataPort  int
	RefDataPort  int
	SnapshotPort int
	Interface    string
	PcapPath     string
	MetricsAddr  string
	// SourceRegistry is the path to a source-registry JSON override; empty uses the embedded default.
	SourceRegistry string
	JSONReport     string
	Verbose        bool
	// LogThrottle is the minimum interval between identical (rule,status) log
	// lines; 0 disables throttling. Does not affect metrics or exit code.
	LogThrottle time.Duration
}
