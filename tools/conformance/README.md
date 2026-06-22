# dz-conformance

A conformance subscriber for edge-feed publishers. Subscribes to one feed channel (TOB, Midpoint, or MBO) — live multicast or pcap replay — validates the stream against 82 explicit conformance rules drawn from the [edge-feed-spec](../../) (this repo), and returns a CI-friendly exit code.

Unlike a production consumer — which is tolerant of publisher quirks (skipping unknown types, ignoring reserved bits, recovering silently from loss) — this tool is **strict by design**: it flags every structural, sequence, and semantic violation the spec defines. `core/registry.go` is the in-code source of truth for the full rule set.

## What it does

```
multicast group (or .pcap)
  ├── mktdata port ──► frame decoder (strict, intolerant)
  ├── refdata port ──► frame decoder
  └── snapshot port ──► frame decoder (MBO only)
           │
           ▼
      engine (82-rule validator)
           │
    ┌──────┴──────┐
    │             │
  slog        Prometheus
  findings     metrics
    │             │
    └──────┬──────┘
           │
      JSON report
      exit code (0=pass / 1=violation / 2=error)
```

## Two-tier conformance model

Rules are partitioned into two tiers based on when they can fire:

**Tier 1 — structural (loss-immune).** Decidable from a single intact datagram: magic, schema version, frame length consistency, message count vs walk, per-type message length, port placement, enum ranges, reserved bits. These cannot produce false positives from packet loss.

**Tier 2 — stateful / relational (verifiability-gated).** Require cross-message state: per-instrument sequence density, referential integrity, quantity conservation, the snapshot↔delta book oracle, refdata coverage. Before firing a Tier-2 rule the engine confirms that packet loss (or cold-start, reorder, bound-subset capture, or an in-flight transition) cannot explain the anomaly. If it can, the result is `Unverifiable` rather than a violation.

This split is what makes lossless pcap replay yield near-100% verifiable coverage — the tool is strong in CI without producing false alarms on live feeds with normal multicast loss.

## MBO snapshot oracle (headline capability)

For the MBO feed the engine reconstructs the order book independently from both the delta stream and the periodic snapshot stream, then diffs them at every snapshot (`SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT`). This catches a publisher whose internal book has silently diverged from the deltas it emitted — exactly the class of bug invisible to structural or sequence checks alone. Loss is never treated as publisher non-conformance: a per-instrument gap forces the affected instrument to `Unverifiable`, not `Violation`.

## Rule catalog

The full 82-rule catalog — rule ID, severity, tier, applicable feeds — is defined in `core/registry.go` (the in-code source of truth), with one-line per-rule summaries in `core/ruledoc.go`. `core/registry_test.go` and `core/ruledoc_test.go` guarantee the set stays complete and documented.

## Quick start

### Live capture

```bash
go build -o dz-conformance .

# MBO feed — three ports
./dz-conformance \
  --feed mbo \
  --group 239.10.10.10 \
  --mktdata-port 7003 \
  --refdata-port 7004 \
  --snapshot-port 7005 \
  --interface doublezero1 \
  --metrics-addr 127.0.0.1:9100 \
  --json-report /tmp/conformance-report.json

# Top-of-Book feed — two ports
./dz-conformance \
  --feed tob \
  --group 239.10.10.10 \
  --mktdata-port 7001 \
  --refdata-port 7002 \
  --interface doublezero1

# Midpoint feed
./dz-conformance \
  --feed midpoint \
  --group 239.10.10.10 \
  --mktdata-port 7011 \
  --refdata-port 7012 \
  --interface doublezero1
```

Runs until SIGINT or SIGTERM.

### pcap replay (CI)

```bash
./dz-conformance \
  --feed mbo \
  --pcap capture.pcap \
  --mktdata-port 7003 \
  --refdata-port 7004 \
  --snapshot-port 7005 \
  --json-report report.json
echo "exit: $?"
```

With `--pcap`, the tool replays the capture in order and exits when the file is exhausted. On a lossless capture this gives near-100% rule coverage.

## CLI flags

| Flag | Default | Description |
|------|---------|-------------|
| `--feed` | `mbo` | Feed to validate: `tob`, `midpoint`, or `mbo` |
| `--group` | | Multicast group address (required for live capture; omit with `--pcap`) |
| `--mktdata-port` | `0` (off) | UDP port for market-data frames |
| `--refdata-port` | `0` (off) | UDP port for reference-data frames |
| `--snapshot-port` | `0` (off) | UDP port for snapshot frames (MBO only) |
| `--interface` | system default | Network interface for multicast join (e.g. `doublezero1`) |
| `--pcap` | | Replay a `.pcap` file instead of live capture |
| `--metrics-addr` | (off) | Serve Prometheus metrics on this address (e.g. `127.0.0.1:9100`). Bind to a non-public interface. |
| `--source-registry` | embedded | Path to a source-registry JSON override (default: embedded pinned registry) |
| `--strict` | `false` | Treat `should`-violations as exit-code failures (in addition to `must`) |
| `--oracle-confirm-cycles` | `2` | Oracle cycles before promoting `Suspected` → `Violation` |
| `--reorder-window` | `8` | Per-port frame reorder buffer size (frames) |
| `--expect-manifest-cadence` | `0` (off) | Expected manifest cadence (e.g. `1s`); enables `REFDATA.MANIFEST_CADENCE` |
| `--expect-definition-cycle` | `0` (off) | Expected definition-cycle duration; enables `REFDATA.DEFINITION_CYCLE_COVERAGE` |
| `--expect-heartbeat` | `0` (off) | Expected heartbeat interval; enables `HEARTBEAT.CADENCE` |
| `--expect-snapshot-cycle` | `0` (off) | Expected snapshot cycle duration (accepted for forward compatibility; wired in Phase 3) |
| `--json-report` | | Write a JSON findings report to this path |
| `-v` | `false` | Verbose logging (lowers the stderr threshold to INFO, surfacing `Unverifiable` findings; default is WARN) |
| `--log-throttle` | `1s` | Minimum wall-clock interval between identical `(rule, status)` log lines. `0` disables throttling. Affects log lines only — metrics and the exit code always count every finding. |
| `--version` | | Print version and exit |

**Port note.** At least one port flag must be non-zero or the tool exits with code 2. Rule behavior when a port is omitted is rule-specific — some rules are effectively unreachable with no traffic on that port, while others may still fire from related activity on a bound port.

**Interface note.** On a multi-NIC host, the default IGMP join may use the wrong interface. Pass `--interface doublezero1` (or whatever the GRE tunnel interface is named) to join on the correct one.

**Cadence/cycle flags.** The spec's suggested cadence values (1 s manifest, 30 s definition cycle, 15 s heartbeat) are recommendations, not requirements. These flags let you enforce the value agreed with your publisher. Rules without their corresponding flag set are downgraded to `info` or skipped — they never fire on an assumed hard-coded value.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | No violations (or only `should` violations without `--strict`) |
| `1` | At least one `must` violation; or at least one `should` violation with `--strict` |
| `2` | Startup/runtime error (bad flags, I/O failure, could not write JSON report) |

Oracle mismatches on the first occurrence are scored as `mismatch_suspected` in `snapshot_audits_total` and do **not** emit a finding or fail CI. After `--oracle-confirm-cycles` consecutive mismatches with the same signature, a confirmed `Violation` finding is emitted.

## Output

### Structured log

Every finding is a structured `slog` line to stderr. Log level is driven by **status** (graded by severity only for a `Violation`), not by severity alone:

| Level | Findings |
|-------|----------|
| `ERROR` | `must`-severity `Violation` (confirmed serious non-conformance) |
| `WARN`  | `should`-severity `Violation`, `Suspected` |
| `INFO`  | `Unverifiable`, `info`-severity `Violation` |
| `DEBUG` | `Pass`, `NA` (observability / not-applicable) |

The process starts at `WARN`, so the steady-state `Unverifiable` stream — which can be tens of thousands of findings per minute on a mid-stream join (loss/cold-start could not be ruled out) — is silent by default and surfaces only in the `dz_conformance_unverifiable_total` metric. Pass `-v` to lower the threshold to `INFO` and see `Unverifiable` lines (e.g. when debugging a pcap).

A `must`-rule that yields an `Unverifiable` finding is *not* an error and does not log at `ERROR`.

```
time=... level=ERROR msg=finding
  rule_id=SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT severity=must status=violation
  feed=mbo instrument_id=42 seq=0 detail="book diverged: 3 orders differ"
```

**Log throttling.** For long-running use, identical `(rule, status)` log lines are rate-limited to one per `--log-throttle` interval (default `1s`). When lines are dropped, the next emitted line for that key carries a `suppressed=N` attribute reporting how many were elided. Throttling touches **only** the log sink: the `Aggregator` (exit code) and Prometheus metrics observe every finding regardless, so counts and alerting are never affected. Set `--log-throttle=0` to log every finding (e.g. for deterministic pcap-replay debugging).

### JSON report

Pass `--json-report path` to write a machine-readable summary at the end of the run. The report is a JSON object with a `rules` array; each entry has a `rule_id` and a `counts` map of status name → count (e.g. `{"rule_id": "FRAME.MAGIC_MISMATCH", "counts": {"violation": 1}}`).

### Prometheus metrics

Pass `--metrics-addr` to expose metrics at `/metrics`. Liveness probe at `/healthz`.

## Metrics

All metrics are prefixed `dz_conformance_`.

| Name | Type | Labels | Meaning |
|------|------|--------|---------|
| `violations_total` | counter | `feed`, `rule_id`, `severity` | Confirmed conformance violations |
| `unverifiable_total` | counter | `feed`, `rule_id`, `reason` | Checks that could not be verified (`reason` is `unspecified` in current engine code; the field is reserved for future per-cause breakdown) |
| `checks_total` | counter | `feed`, `rule_id`, `result` | All checks by result |
| `transport_loss_total` | counter | `port` | Transport packet-loss events by port (`mktdata`, `refdata`, `snapshot`) |
| `transport_corruption_total` | counter | `port`, `reason` | Transport corruption events |
| `snapshot_audits_total` | counter | `result` | Snapshot oracle outcomes (`match`, `mismatch_suspected`, `mismatch_confirmed`, `unverifiable`) |
| `instruments_state` | gauge | `status` | Instrument count per state (registered; not yet populated by current engine code) |
| `build_info` | gauge | `version`, `commit` | Always 1; carries build labels |
| `rule_info` | gauge | `rule_id`, `severity`, `summary`, `spec_url` | Always 1; static per-rule metadata for the active feed. One series per applicable rule (set once at startup). Join by `rule_id` to label the counters with a human summary and a link to the spec section. |
| `uptime_seconds` | gauge | — | Seconds since process start |

Cardinality is bounded: `rule_id` is a fixed enum (from `core/registry.go`), `port` has 3 values, `reason` is a small enum. `rule_info` adds one series per feed-applicable rule with fixed `summary`/`spec_url` label values (sourced from the registry's `RuleDoc`); these bounded strings live **only** on `rule_info`, never on the high-rate counters.

**Note:** both `build_info` labels are wired from build vars — set them with `-ldflags "-X main.version=… -X main.commit=…"`. They default to `version="dev"`, `commit="none"`.

## Building

```bash
go build -o dz-conformance .
```

With version info:

```bash
go build -ldflags "-X main.version=0.1.0" -o dz-conformance .
```

## Testing

```bash
go test ./...
```

Most tests use synthetic wire-format bytes (no network). `golden_test.go` builds conformant pcaps programmatically, runs them through `Run()`, and asserts exit code 0 with zero must-violations. A separate fixture drift guard (`TestGoldenPcapFixtures`) compares the generated pcap bytes against committed `testdata/*.pcap` files byte-for-byte; set `TESTDATA_UPDATE=1` to regenerate fixtures after an intentional protocol change. `input/multicast_test.go` contains a live UDP loopback test that skips automatically in short mode or environments where multicast on loopback is unavailable.

## CI usage

Run against a reference capture in your publisher's release pipeline:

```bash
./dz-conformance \
  --feed mbo \
  --pcap publisher-release.pcap \
  --mktdata-port 7003 \
  --refdata-port 7004 \
  --snapshot-port 7005 \
  --strict \
  --json-report conformance-report.json
# exits 0 on clean, 1 on any violation
```

## Running as a long-running service

The tool has two operating modes:

- **`--pcap` replay = one-shot / CI.** Reads the file to EOF, writes the report, and exits with the [exit code](#exit-codes) above. This is the batch gate shown under [CI usage](#ci-usage).
- **Live multicast = long-running daemon.** The read loop runs until `SIGINT`/`SIGTERM` (or a fatal read error), serving Prometheus metrics the whole time. This is the primary production deployment — a persistent per-feed monitor, e.g. on the host that already records the feed.

For a 24/7 deployment:

- **One instance per feed.** `--feed` is singular (each feed has its own frame magic and rule subset). Run separate processes for `tob`, `midpoint`, and `mbo`, each with its own `--metrics-addr`.
- **Supervise it.** A read error exits the process with code `2`; there is no internal reconnect (e.g. on an interface flap). Run under `systemd` or Docker with a restart policy such as `restart: unless-stopped`.
- **Alert on metrics, not the exit code.** The `0/1/2` exit code is a batch artifact and is meaningless for a live instance (which only exits on signal/error). Live alerting watches `dz_conformance_violations_total` (and the `should`-graded `unverifiable`/`suspected` counters) via Prometheus.
- **Tame log volume.** Status-based levels keep the default (`WARN`) stderr stream to confirmed violations plus `Suspected` (oracle candidates) — the `Unverifiable` firehose is suppressed unless `-v` is passed; `--log-throttle` (default `1s`) bounds repeats. See [Structured log](#structured-log).
- **Restarts reset deep verifiability, not structural checks.** Because a mid-stream join doesn't bootstrap a trusted book (see [Known limitations](#known-limitations)), every restart re-enters cold-start: the stateful referential/oracle checks stay `Unverifiable` until a fresh `Reset Count` era. **Tier-1 structural rules are restart-immune** — magic, schema, frame-length, message-count, enum/port placement fire on every frame regardless — so even a freshly (re)started or flapping instance still catches all structural non-conformance immediately; only the deep MBO/oracle coverage needs a stable, from-session-start process.

Memory is bounded for indefinite operation: the per-`(era,seq)` dedup map is FIFO-evicted via a fixed `2×--reorder-window` ring (it does **not** grow with the sequence space), books/trackers are keyed by the live instrument set and pruned on manifest bumps / cleared on era resets, and metric cardinality is fixed — each finding series is keyed by `feed × rule_id` plus one small bounded enum (`severity`, `result`, or `reason`), never the free-form `detail`.

> A self-contained Docker + Prometheus + Grafana monitoring stack ships alongside this tool (`docker-compose.yml`, `grafana/`, `prometheus/`) — added in a follow-up layer.

## Known limitations

The tool is correct (no false `Violation`s) and complete for its primary use case: validating a **single channel** captured from the publisher's **session start**. Two boundaries are deliberately conservative — both fail toward `Unverifiable`, never toward a false violation:

- **Single channel per port.** Per-port sequencing/dedup/reorder state is keyed by port, not by `(port, channel_id)`. The edge-feed-spec allows sharding the instrument set across multiple channels; running a *multi-channel* capture where two channel IDs share one UDP port could mis-attribute sequence numbers across channels. Multi-channel support is future work; for a single channel the tool is exact.
- **Mid-stream join doesn't reconstruct a trusted book.** The referential-integrity and snapshot↔delta oracle checks only run once an instrument's delta book is *trusted*, which today requires observing its delta stream from `Per-Instrument Seq = 1` (i.e. from session start / `Reset Count` boundary). A cold-start or post-`InstrumentReset` recovery snapshot is currently used only to detect divergence, not to *bootstrap* the live book — so an instrument joined mid-stream stays `Unverifiable` for those checks until a fresh era. Capture the publisher from startup to exercise the full oracle. Bootstrapping a trusted book from a clean snapshot is a planned enhancement.

Minor: the `dz_conformance_instruments_state` gauge is registered but not yet populated, and most `Unverifiable` findings currently carry `reason="unspecified"` (the bounded-reason taxonomy is wired only at the cross-port downgrade sites). Neither affects violation detection or the CI exit code.
