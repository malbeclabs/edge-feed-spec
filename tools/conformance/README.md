# dz-conformance

A conformance subscriber for edge-feed publishers. Subscribes to one feed (TOB, Midpoint, MBO, or MBP) — live multicast or pcap replay — validates the feed against 88 explicit conformance rules drawn from the [edge-feed-spec](../../) (this repo), and returns a CI-friendly exit code.

Unlike a production consumer — which is tolerant of publisher quirks (skipping unknown types, ignoring reserved bits, recovering silently from loss) — this tool is **strict by design**: it flags every structural, sequence, and semantic violation the spec defines. `core/registry.go` is the in-code source of truth for the full rule set.

## What it does

```
multicast group (or .pcap)
  ├── mktdata port ──► frame decoder (strict, intolerant)
  ├── refdata port ──► frame decoder
  └── snapshot port ──► frame decoder (MBO only)
           │
           ▼
      engine (88-rule validator)
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

## Sequencing keys on the channel instance

A **channel instance** is one path's view of one channel, keyed `(source IP address, Channel ID, destination port)` — the unit that owns a sequence series and a `Reset Count` ([GLOSSARY.md](../../GLOSSARY.md), and *Redundant Channel Instances* in [market-by-price/spec.md](../../market-by-price/spec.md)). An operator MAY run two publishers serving the **same** channel to the same group and the same port for redundancy; each advances its own sequence space and its own `Reset Count`, and they are told apart by transport, because `Source ID` names the matching engine and is equal on both. So the checker keys its datagram-level state — sequence, dedup ring, reorder buffer, send-timestamp baseline, verifiability window, heartbeat baseline — per instance, and never per channel.

Keying any less finely merges the two series, and the merge is loud in one direction and silent in the other. Loud: each alternation reads as backward motion (`FRAME.SEQ_RESET_GAP`, a `must`) or as a forward gap, which climbs `transport_loss_total` and latches the verifiability window — cleared only on a publisher reset — so every gated rule reports `unverifiable` for the process lifetime while `checks_total{result="pass"}` keeps advancing. Silent: one publisher's heartbeats cover the other's total outage, so `HEARTBEAT.CADENCE` never fires. `engine/channel_instance_test.go` pins both directions, on both axes (two sources on one channel, and two channels on one port).

Three consequences worth stating:

- **A source address that has not been seen before opens a new series, silently.** It is not a gap, not backward motion, and not a reset: no violation, no `transport_loss_total`, no tainted window. This matters because a publisher's address is not stable — a tunnel address is a lease that can be reassigned under a live host — and a reassignment must not page.
- **Both inputs carry the source at the same fidelity.** Live capture takes it from the socket and pcap replay from the IP header, so an offline replay keys instances exactly as the live instance does. A capture whose link type carries no network layer yields the zero address, and every datagram in it then reads as one instance.
- **The tracker map is bounded** at `maxChannelInstances` (`engine/engine.go`), evicting the least-recently-seen instance. The source address is part of the key and nothing authorises it — an any-source join accepts datagrams from any sender — so the key space is not ours to trust. The cap is far above any real deployment; anything still buffered for an evicted instance is counted as transport loss rather than dropped quietly, and if the instance returns it starts a fresh series.

## Coverage vs. silence

A Tier-2 rule whose **execution** is conditional runs only when its preconditions line up, and that creates a failure mode worse than a false positive: a rule that quietly stops running reports the same thing as a rule that checked everything and found nothing — nothing. A clean report then means "no violations found", not "no violations exist", and there is no way to tell which from the output.

So every such rule accounts for each opportunity it gets, with exactly one finding:

| Result | Meaning |
|--------|---------|
| `pass` | the check ran and the property held |
| `violation` | the check ran and the publisher broke it |
| `unverifiable` | the check could not run; `reason` names what stopped it |
| `na` | the check does not apply to this opportunity at all |

`checks_total{rule_id}` is therefore the rule's **denominator** and its `result="pass"` series the coverage actually achieved. `unverifiable_total{rule_id,reason}` breaks the shortfall down by cause, which is what separates a healthy run from a broken feed: 33 `cold_start` skips mean instruments still adopting their first baseline, while 33 `loss` skips mean a feed dropping frames.

Read them together. On the bundled market-by-price capture the reconstruction oracle reports:

```json
{
  "rule_id": "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT",
  "counts": { "pass": 5, "unverifiable": 33 },
  "unverifiable_by_reason": { "cold_start": 29, "pending": 4 }
}
```

Five of the capture's 38 completed snapshot groups were compared and matched; 29 were each an instrument's first group, adopted as its baseline with nothing yet to compare against, and 4 carried a `Last Instrument Seq` the delta port had not yet reached. The capture spans about one snapshot cycle, so one comparison per instrument that got a second group is the ceiling those bytes allow — a fact the previous output could not express, because it reported the same silence a run comparing zero groups would have.

`core.ConditionalExec` marks the rules that owe a denominator, `engine/denominator.go` states the invariant, and `TestConditionalRulesReportADenominator` fails a run where one of them goes quiet. Rules gated on an `--expect-*` flag are a separate axis: they report nothing when the flag is unset, and `rule_info` says so statically before a frame arrives.

### Rules an unbound port starves

The invariant above cannot close one hole from inside the engine. A refdata-gated rule with no `--refdata-port` still reports — it is driven by mktdata messages and merely *gated* on reference data, so each message it cannot judge becomes an `unverifiable`/`cold_start`. But a **snapshot-driven** rule with no `--snapshot-port` has no driver at all: the code that would emit is never entered, so there are zero opportunities, zero findings, and zero findings is what a clean feed looks like.

That produced a two-step trap in which neither step looks wrong:

1. Operator runs an MBP feed without `--snapshot-port`, gets exit 1 and a real violation.
2. They fix the publisher, re-run the same command, get **exit 0 and an empty report** — and read it as a pass, while every `MBP.SNAP.*` rule was starved.

So the CLI now reports it from the only place that knows, the flags: a warning on stderr at bind time (regardless of `-v`) and an `na` finding per unreachable rule, which lands in the JSON report and in `checks_total{result="na"}`.

```
dz-conformance: WARNING --snapshot-port is not set, so 2 mbp rule(s) have no frames to
evaluate and are reported as na, NOT as passes. The exit code does not cover them:
[MBP.SNAP.GROUP_STRUCTURE MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT]
```

**The exit code is deliberately unchanged.** A two-port run is legitimate and failing it would break anyone doing one on purpose; what matters is that exit 0 is not read as "those rules passed". `core.SnapshotDrivenRules` is the set, and `TestStarvedRulesAreExactlyTheSnapshotDrivenOnes` proves each member disappears when the port is dropped and reappears when it is restored — so the `na` is never a false claim.

## MBO snapshot oracle (headline capability)

For the MBO feed the engine reconstructs the order book independently from both the delta stream and the periodic snapshot stream, then diffs them at every snapshot (`SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT`). This catches a publisher whose internal book has silently diverged from the deltas it emitted — exactly the class of bug invisible to structural or sequence checks alone. Loss is never treated as publisher non-conformance: a per-instrument gap forces the affected instrument to `Unverifiable`, not `Violation`.

## Rule catalog

The full 88-rule catalog — rule ID, severity, tier, applicable feeds — is defined in `core/registry.go` (the in-code source of truth), with one-line per-rule summaries in `core/ruledoc.go`. `core/registry_test.go` and `core/ruledoc_test.go` guarantee the set stays complete and documented.

## Quick start

### Try it now (bundled captures)

No feed or network required — the repo ships small conformant captures under `testdata/`, on fixed ports `18001` (mktdata), `18002` (refdata), `18003` (snapshot):

```bash
go build -o dz-conformance .

# Conformant MBO capture → exits 0
./dz-conformance --feed mbo --pcap testdata/conformant_mbo.pcap \
  --mktdata-port 18001 --refdata-port 18002 --snapshot-port 18003
echo "exit: $?"   # 0

# See it catch a violation: decode MBO bytes as TOB → magic mismatch, exits 1
./dz-conformance --feed tob --pcap testdata/conformant_mbo.pcap \
  --mktdata-port 18001 --refdata-port 18002
echo "exit: $?"   # 1
```

`conformant_tob.pcap` and `conformant_midpoint.pcap` are also provided (mktdata + refdata only).

`nonconformant_mbp.pcap` is different in kind: a real capture from a live publisher on venue data, on ports `31000`/`41000`/`51000`, taken **before** its defects were fixed. It carries oversized frames and the snapshot flag set on refdata, so it exits 1 by design — it is a regression fixture for the market-by-price rules, not a conformant sample. It is also two orders of magnitude larger than the hand-built captures, which is the cost of covering a feed whose snapshot stream is most of its bytes. That cost bought something: three defects in the consumer survived the synthetic tests and were found only by running against this data.

It was captured before the market-by-price spec went to `2.0.0`, so it carries `Schema Version = 1` and now also reports `FRAME.SCHEMA_VERSION` on every frame. That is correct — it is a `1.x` capture being judged against the current `3.x` rules — and it does not diminish the fixture's value: the original defects still fire, and the snapshot oracle still reaches the same pass counts. Read past the schema rows when using it.

```bash
# Exits 1: the capture's known defects are reported.
./dz-conformance --feed mbp --pcap testdata/nonconformant_mbp.pcap \
  --mktdata-port 31000 --refdata-port 41000 --snapshot-port 51000
```

Add `--json-report /tmp/report.json` for a per-rule status dump, or `-v` to surface `Unverifiable`/info findings.

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

# Market-by-price feed (three ports, like MBO)
./dz-conformance \
  --feed mbp \
  --group 239.10.10.20 \
  --mktdata-port 7021 \
  --refdata-port 7022 \
  --snapshot-port 7023 \
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
| `--version` | | Print `<version>+<commit>` and exit (e.g. `v0.2.0+85fd9b6d40cf`, `dev+none` unstamped) |

**Port note.** At least one port flag must be non-zero or the tool exits with code 2. Rule behavior when a port is omitted is rule-specific — some rules are effectively unreachable with no traffic on that port, while others may still fire from related activity on a bound port. Omitting `--snapshot-port` on an MBO or MBP feed is called out explicitly at startup, because those rules would otherwise vanish from the output entirely: see [Rules an unbound port starves](#rules-an-unbound-port-starves).

**Interface note.** On a multi-NIC host, the default IGMP join may use the wrong interface. Pass `--interface doublezero1` (or whatever the GRE tunnel interface is named) to join on the correct one.

**Cadence/cycle flags.** The spec's suggested cadence values (1 s manifest, 30 s definition cycle, 15 s heartbeat) are recommendations, not requirements. These flags let you enforce the value agreed with your publisher. Rules without their corresponding flag set are downgraded to `info` or skipped — they never fire on an assumed hard-coded value.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | No violations (or only `should` violations without `--strict`) |
| `1` | At least one `must` violation; or at least one `should` violation with `--strict` |
| `2` | Startup/runtime error (bad flags, I/O failure, could not write JSON report). An error that ends a run mid-input is also recorded as `read_error` in the [JSON report](#json-report), so a stored report is never mistaken for a clean pass |

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

Pass `--json-report path` to write a machine-readable summary at the end of the run. The report is a JSON object with top-level `version`, `commit`, `strict` and `read_error`, plus a `rules` array; each entry has a `rule_id`, its `severity` and a `counts` map of status name → count (e.g. `{"rule_id": "FRAME.MAGIC_MISMATCH", "severity": "must", "counts": {"violation": 1}}`). A rule with `unverifiable` findings also carries `unverifiable_by_reason`, breaking that count down by cause — this is the only place a `--pcap` CI run reports it, since a one-shot replay has no Prometheus to scrape. See [Coverage vs. silence](#coverage-vs-silence) for how to read the two together.

`version` and `commit` are the build labels (the same ones `build_info` carries), so a stored report can be attributed to the binary that produced it. `severity`, `strict` and `read_error` together are what make the [exit code](#exit-codes) recomputable from the report alone:

- `read_error` non-empty → **2**. The run ended early on an input error (a truncated pcap, a dead socket), so the counts cover only the frames read before it. It is always present and empty on a run that consumed its input to the end, so a reader can tell a completed run from a binary that never recorded whether it completed.
- otherwise **0** or **1** from the counts: only `must` and `should` violations move the code, and a `should` violation resolves to `0` or `1` depending on `--strict`, which the counts do not record.

The remaining exit-2 paths — bad flags, an unbound `--metrics-addr`, no ports configured, an unreadable `--source-registry`, or a failure writing the report itself — abort before or during the write, so they leave no parseable report to misread.

### Prometheus metrics

Pass `--metrics-addr` to expose metrics at `/metrics`. Liveness probe at `/healthz`.

## Metrics

All metrics are prefixed `dz_conformance_`.

| Name | Type | Labels | Meaning |
|------|------|--------|---------|
| `violations_total` | counter | `feed`, `rule_id`, `severity` | Confirmed conformance violations |
| `unverifiable_total` | counter | `feed`, `rule_id`, `reason` | Checks that could not be verified, by cause. `reason` is a closed enum (`core.Reason*`): `loss`, `cold_start`, `reorder`, `pending`, `overflow`, `truncated`, `insufficient_window`, `superseded`, `untrusted`, `bound_subset`, `transition`, `unspecified` |
| `checks_total` | counter | `feed`, `rule_id`, `result` | All checks by result — the **denominator** for every rule, see [Coverage vs. silence](#coverage-vs-silence) |
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

With build info (this is what `--version`, `build_info` and the JSON report show):

```bash
go build -ldflags "-X main.version=v0.1.0 -X main.commit=$(git rev-parse --short=12 HEAD)" -o dz-conformance .
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

Memory is bounded for indefinite operation: the per-`(era,seq)` dedup map is FIFO-evicted via a fixed `2×--reorder-window` ring (it does **not** grow with the sequence space), the per-instance tracker map is capped and evicts the least-recently-seen instance (see [Sequencing keys on the channel instance](#sequencing-keys-on-the-channel-instance)), books/trackers are keyed by the live instrument set and pruned on manifest bumps / cleared on era resets, and metric cardinality is fixed — each finding series is keyed by `feed × rule_id` plus one small bounded enum (`severity`, `result`, or `reason`), never the free-form `detail`.

## Demo monitoring stack

This directory ships a **self-contained** monitoring demo — the conformance subscriber plus Prometheus (scrape + alerts) and Grafana (the per-rule dashboard):

```bash
cp .env.example .env        # edit DZ_MBO_* / DZ_INTERFACE for your feed
docker compose up --build
open http://127.0.0.1:3000  # admin / GF_ADMIN_PASSWORD
```

| Path | Role |
|------|------|
| [docker-compose.yml](docker-compose.yml) | `dz-conformance` + `prometheus` + `grafana` (build context is this self-contained module) |
| [prometheus/prometheus.yml](prometheus/prometheus.yml) | scrapes `dz-conformance` at `127.0.0.1:9094` |
| [prometheus/alerts/conformance.yml](prometheus/alerts/conformance.yml) | must-violation page + coverage-loss / rule-stopped-verifying / transport-loss / target-down warnings |
| [prometheus/rule_tests/](prometheus/rule_tests/) | `promtool test rules` unit tests for those alerts (run in CI; kept out of `alerts/` because `prometheus.yml` globs that directory as `rule_files`) |
| [grafana/dashboards/conformance.json](grafana/dashboards/conformance.json) | the `dz_conformance_*` dashboard, including the per-rule conformance table |
| [grafana/provisioning/](grafana/provisioning/) | auto-registers the Prometheus datasource and the dashboard |

## Known limitations

The tool is correct (no false `Violation`s) and complete for its primary use case: validating a feed captured from the publisher's **session start**. Two boundaries are deliberately conservative — both fail toward `Unverifiable`, never toward a false violation:

- **Deeper state is keyed by channel, not by instance.** Per-instrument sequencing, the reconstructed books, the snapshot groups and the reference-data state machine are keyed by `(channel_id, …)`, so two instances of one channel feed one set of them. Where a spec requires the full `(source, channel, …)` key (*Redundant Channel Instances*), this is a boundary and not conformance to it. It is conservative rather than wrong: the verifiability gate on those rules is the **OR** across the channel's instances, so any instance's loss downgrades them to `Unverifiable` instead of blaming the publisher for an anomaly the loss could explain. `HEARTBEAT.CADENCE` is the exception and gates per instance, because its baseline is per instance. `REFDATA.VALID_FLAG_WHILE_SERVING` reaches the same boundary by a second route: the session-end signal is keyed by channel, so one arm's `EndOfSession` can settle a `Valid=0` the other arm sent, and the verdict is a pass where an instance-keyed tool would report a violation. Same conservative direction — a missed violation, never a fabricated one — but arrived at by excusing rather than by downgrading.
- **Mid-stream join doesn't reconstruct a trusted book.** The referential-integrity and snapshot↔delta oracle checks only run once an instrument's delta book is *trusted*, which today requires observing its delta stream from `Per-Instrument Seq = 1` (i.e. from session start / `Reset Count` boundary). A cold-start or post-`InstrumentReset` recovery snapshot is currently used only to detect divergence, not to *bootstrap* the live book — so an instrument joined mid-stream stays `Unverifiable` for those checks until a fresh era. Capture the publisher from startup to exercise the full oracle. Bootstrapping a trusted book from a clean snapshot is a planned enhancement.

One further gap is a **silence** rather than a conservative downgrade: `HEARTBEAT.CADENCE` measures an instance's cadence when that instance's *next* heartbeat arrives, so an arm that dies and stays dead is never reported — the outage a two-arm deployment is most likely to suffer. Nothing fabricates a violation, but nothing reports the outage either, while the surviving arm keeps `checks_total{result="pass"}` climbing. Closing it needs an end-of-observation sweep over the mktdata instances, and that sweep needs a clock of its own: the naive version — measure each instance's last heartbeat against the last wire timestamp seen — fires on any capture that simply ends mid-silence, reporting 5 s of silence on `nonconformant_mbp.pcap` where nothing died. So it is a follow-up with its own tests, not a rider on a keying change.

Minor: the `dz_conformance_instruments_state` gauge is registered but not yet populated. This does not affect violation detection or the CI exit code.

## Releasing

Releases are cut from the **conformance release** GitHub Actions workflow
(`.github/workflows/conformance-release.yml`), run via *Run workflow*
(`workflow_dispatch`):

- **version** — `vMAJOR.MINOR.PATCH` or a prerelease like `v0.1.0-rc.1`.
- **ref** — git ref to build from (default `main`).

The workflow lints, runs `go test ./...`, builds a static `linux_amd64`
binary (`-ldflags -X main.version=<version> -X main.commit=<sha>`), pushes the
annotated tag `conformance/<version>`, and publishes a GitHub Release with:

- `dz-conformance_<version-without-v>_linux_amd64.tar.gz`
- `dz-conformance_<version-without-v>_linux_amd64.tar.gz.sha256`

Versions containing `-` are marked as prereleases. The deploy side
(`malbeclabs/infra`, role `dz_conformance`) consumes these assets by name.

**`--version` output is part of that contract.** The role runs the installed binary,
trims a leading `v`, and requires `^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$` to decide whether
to re-download; build metadata after a `+` is ignored in the semver comparison, but a
space, a parenthesis or a second line is not accepted and makes every Ansible run
re-download the binary and restart every instance. `main_test.go` asserts the shape,
including the `vX.Y.Z-rc.N+<commit>` prerelease form.
