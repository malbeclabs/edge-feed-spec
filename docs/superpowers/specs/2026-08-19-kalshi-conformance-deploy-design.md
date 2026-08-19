# Kalshi conformance deploy: `dz-conformance` against the Kalshi publisher feeds

**Date:** 2026-08-19
**Status:** Design — approved in outline, pending written-spec review
**Branch:** `specs/kalshi-conformance-deploy`
**Plan:** `docs/superpowers/plans/2026-08-19-kalshi-conformance-deploy.md`

**Repositories.** Paths are repo-qualified wherever they are not in this one. Bare paths
(`tools/conformance/...`) are `malbeclabs/edge-feed-spec`; the Ansible role, inventory, Grafana alerts
and rollout live in `malbeclabs/infra` and are prefixed accordingly. The design is recorded here
because the release this deployment depends on is cut here, and because the checker's rules and their
severities — which decide what pages and what does not — are defined here.

## Goal

Validate the Kalshi edge feeds live and continuously, on the same machinery that already validates
the Hyperliquid mainnet2 publisher: `dz-conformance` as a systemd instance per stream, scraped by
Alloy, observed in Grafana.

Today nothing validates any Kalshi feed. The only conformance that exists is an offline replay
script (`malbeclabs/kalshi:app/publisher/crates/kalshi-publisher/examples/tob_conformance.sh`) that
no workflow runs. On the Hyperliquid side the `dz_conformance` role runs two instances against group
`233.84.178.15` (TOB + MBO), Alloy scrapes them, and three Grafana alerts read the result.

Scope is **three streams on the hosts that already receive them** — perps TOB, perps MBP, and the
sports NFL MBP channel — with the two known Kalshi deviations scoped out of the paging path before
they start emitting.

## Non-goals

- **No publisher change of any kind.** No `kalshi_publisher_version` bump; read-only with respect to
  the publisher. `TobFeed::emit_control_both` is not touched (see §6).
- **No HL pin bump.** HL stays on `conformance/v0.1.0` (§2.2).
- **No new multicast join.** Every group and port below is copied from the capture config already
  running on the same host, so no IGMP join, no subscriber-allowlist change, no DZ ledger work.
- **No 31-channel sports fan-out.** The role is one process per port triple, so full sports coverage
  is 31 processes on one recorder. NFL is also the only channel with a measured baseline.
- **No `sports-tob` (`233.84.178.17`).** No host receives it, so it is a new join and a separate
  decision.
- **No fix for the pre-existing HL alert noise** (§4.1).

---

## 1. What has to happen first: a release that can read the feed

`conformance/v0.1.0` (2026-06-23) is the only published release and it cannot validate Kalshi:

- **No MBP engine at all.** No `tools/conformance/engine/mbp.go`; `-feed` accepts only `tob`, `midpoint`, `mbo`
  (`tools/conformance/main.go:24` at that tag). `-feed mbp` exits 2.
- **It hardcodes schema 1.** `if h.SchemaVersion != 1` at `tools/conformance/wire/decode.go:37`.

The Kalshi publisher emits **schema 3** (`dz-tob-protocol/src/constants.rs:26`), including on the tag
`publisher/v0.9.0` that the fleet actually runs.

`origin/main` of `malbeclabs/edge-feed-spec` (`fea26c3`) is the build that works: `MagicMBP` exists,
`tools/conformance/engine/mbp.go` is merged (PR #24), `-feed mbp` is registered (`tools/conformance/main.go:24,73`), and
`ExpectedSchemaVersion()` returns 3 per-feed (`tools/conformance/wire/header.go:24` — midpoint stays at 1 because it
kept its slimmed 64-byte `InstrumentDefinition`).

**Re-verified 2026-08-19:** `gh release list --repo malbeclabs/edge-feed-spec` returns one row, and
`git ls-remote --tags origin` shows only `refs/tags/conformance/v0.1.0`. No `v0.2.0+` has appeared.
Local `HEAD` == `origin/main` == `fea26c3`.

### 1.1 Two corrections to the received framing

Both strengthen the conclusion rather than change it, and both are recorded because the wrong
mechanism in a note becomes the wrong decision later.

**`FRAME.SCHEMA_VERSION` is `Info` severity, not `Must`** — at v0.1.0 (`tools/conformance/core/registry.go:24`) and on
main. So v0.1.0 pointed at a Kalshi feed does *not* produce a must-violation on every frame. What it
actually does is worse and quieter: it decodes a schema-3 `InstrumentDefinition` at schema-1 offsets,
so `Symbol` and every later field are read two bytes off and every refdata-derived rule grades
shifted bytes. The release is still required; the reason is misalignment, not severity.

**Symmetrically, the reason not to bump HL's pin is `MSG.LENGTH_PER_TYPE` (`Must`), not
`FRAME.SCHEMA_VERSION`.** See §2.2.

### 1.2 A report has to be attributable to a build

Findings §8.2 records that `--version` prints the version and never the commit, and that neither
appears in the JSON report — so a report cannot be attributed to a build. Findings §8.1 adds that
`tools/conformance/report/json.go`'s `ruleStatusCounts` carries no `severity`, so a JSON reader cannot reconstruct the
exit code (only `Must` and `Should` move it; an `info` rule never does, with or without `-strict`).

That is the exact failure mode this whole deployment exists to avoid, and it is cheap to close, so
it lands **before** the tag rather than as a follow-up.

**`--version` must print `v0.2.0+<commit>` — build metadata, not a space and not a second line.**
This is a hard constraint on the output format, and it is measured rather than assumed. The
`dz_conformance` role parses that output (`malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml:123-146`): it trims
stdout, strips a leading `v`, and requires `^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$`. Measured 2026-08-19:

| candidate output | matches the role's shape regex | `is version('0.2.0','ne',semver)` |
| --- | --- | --- |
| `v0.2.0+85fd9b6d40cf` | yes | `False` — compares equal, build metadata ignored |
| `v0.2.0 (85fd9b6d40cf)` | **no** | n/a |
| `v0.2.0` (today) | yes | `False` |

Any separator other than `+` makes `dz_conformance_download_required` permanently true, so **every**
Ansible run re-downloads the binary and restarts every instance — fleet-wide, HL included. A
regression test on the `--version` output shape guards this, and the coupling gets a comment next to
the print, because the constraint lives in another repository.

---

## 2. The two fleets cannot share one pin, and the inventory currently forces them to

### 2.1 The structural problem

`dz_conformance_feeds` lives in `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml` — on the
**parent** group. So any host that joins `dz_conformance` inherits the mainnet2 streams: group
`233.84.178.15`, ports 9201/9202 and 10201-10203. The Kalshi recorders never receive those groups,
so an inherited instance would bind, join, and sit at zero packets forever — a healthy process
validating nothing, which is indistinguishable from a healthy feed.

The fix is to move the feed list down onto the child group. Child `group_vars` beat parent
`group_vars`, so `hyperliquid_feed_capture_mainnet_recorders.yml` becomes the home of the mainnet2
streams and `dz_conformance.yml` keeps only shared tunables.

### 2.2 Why the pins must differ

The Hyperliquid publisher emits **schema 1** (`hyperliquid/app/publisher/server/`), which is why
v0.1.0 works for it. A release built from main expects 3.

Bumping the fleet-wide `dz_conformance_version` would put v0.2.0 on HL's schema-1 feed, where
`InstrumentDefinition` is 80 bytes and the binary expects 128 (`Symbol` widened from `char[16]` to
`char[64]` at schema 2). `MSG.LENGTH_PER_TYPE` is `Must`, and it would fire on every definition
frame. **That** is what would page — not `FRAME.SCHEMA_VERSION`, which is `Info`.

So: HL stays pinned to `v0.1.0`, Kalshi gets `v0.2.0`, and the comment next to each pin states the
schema-1-vs-3 reason, because a future reader will otherwise tidy the two pins into one.

**The host sets are disjoint, so this is safe and there is no binary-path collision.**
`dz_conformance` resolves today to `chi-mn-recorder1`, `nyc-mn-recorder1`, `aws-tyo-mn-recorder1`.
The hosts that receive Kalshi multicast are `aws-cmh-mn-recorder1`, `aws-was-mn-recorder1`,
`aws-dub-mn-recorder1` (group `kalshi_feed_capture`), none of which is in `dz_conformance`.

### 2.3 The role's assert excludes `mbp`, and that is all that excludes it

`malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml:13` allows `['tob', 'midpoint', 'mbo']`, and `:18` requires
`snapshot_port` for `mbo` only. The binary supports `mbp` and `templates/instance.env.j2` already
emits `--snapshot-port` whenever it is defined, already appends `item.extra_args`, and already
honours `item.interface`. This is a widening of the assert, not checker work.

---

## 3. The streams, and the arithmetic behind their flags

### 3.1 Stream table

Every group and port is copied from the capture config on the same host, so nothing here requires an
IGMP join, a source-allowlist change, or a publisher change.

| hosts | instance name | feed | group | mktdata / refdata / snapshot | metrics |
| --- | --- | --- | --- | --- | --- |
| cmh, was, dub | `kalshi_perps_tob` | `tob` | `233.84.178.3` | 31000 / 41000 / — | 9120 |
| cmh, was, dub | `kalshi_perps_mbp` | `mbp` | `233.84.178.4` | 32000 / 42000 / 52000 | 9121 |
| cmh only | `kalshi_sports_mbp_nfl` | `mbp` | `233.84.178.20` | 34010 / 44010 / 54010 | 9122 |

Sources: `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_{cmh,was,dub}.yml`. The per-metro split mirrors coverage
that already differs by metro — cmh receives 33 sources (perps TOB, perps MBP, 31 sports MBP
channels), while was and dub receive only the two perps sources. **No stream is added that the host
does not already join.**

Metrics ports 9120-9122 are unused anywhere in the repo (grepped), and clear of the reservations
already documented: 9090 `multicast_recorder`, 9102, 9108 `hyperliquid-feed-capture`, 9109 gossip
proxy, 9110 `kalshi-feed-capture`, 12345 Alloy, and 9094/9096 HL conformance on the other hosts. The
role asserts `metrics_port` uniqueness per host.

Two sockets on the same host joining the same group both receive copies — `net.ListenMulticastUDP`
sets `SO_REUSEADDR`, and the HL fleet already proves it in production (HL capture and HL conformance
share `233.84.178.15:9201`).

### 3.2 Instance naming

The names keep the `kalshi_` prefix rather than the capture's plane-first convention
(`tob_edge_kalshi_perps`). This is a deliberate divergence: the Alloy scrape stamps the instance name
as the `stream` label (`malbeclabs/infra:ansible/playbooks/roles/monitoring/templates/config.alloy.j2:312`), and `stream` is what the
alert matchers in §4 select on. A common leading prefix makes that matcher `stream=~"^kalshi_"`
instead of an enumeration that has to be edited every time a stream is added.

### 3.3 Cadence expectations are a 1.1x budget, derived not copied

`REFDATA.MANIFEST_CADENCE` and `HEARTBEAT.CADENCE` compare with a bare `>` and no slack
(`tools/conformance/engine/refdata.go:396`, `tools/conformance/engine/engine.go:345`). The measured medians sit *above* target — manifest
1.000008 s, heartbeat 5.000017 s — so a periodic timer whose mean period equals its target puts
roughly half its samples over the line. Measured: 31 of 61 manifest gaps and 6 of 12 heartbeat gaps
violated, at a worst overshoot anywhere in the capture of **830 µs on a 1 s timer** (0.08%).
Re-running the same capture at `1100ms` / `5500ms` exits 0 with both rules silent. It is a flag-side
budget and needs no code change. (`malbeclabs/kalshi:docs/superpowers/specs/2026-08-08-conformance-findings.md`
§3.1, §5.1, §8.1.)

Each flag is **1.1x the deployed publisher value**, read from
`malbeclabs/kalshi:infra/ansible/inventory/group_vars/kalshi_publishers_{perps,sports}.yml`:

| flag | perps TOB (`:314-316`) | perps MBP (`:412-415`) | sports MBP (`:153-156`) |
| --- | --- | --- | --- |
| `--expect-manifest-cadence` | `manifest_cadence_seconds: 1` -> **1100ms** | 1 -> **1100ms** | 1 -> **1100ms** |
| `--expect-heartbeat` | `heartbeat_interval_seconds: 5` -> **5500ms** | 5 -> **5500ms** | 5 -> **5500ms** |
| `--expect-definition-cycle` | `refdata_cycle_seconds: 30` -> **33s** | 30 -> **33s** | 30 -> **33s** |
| `--expect-snapshot-cycle` | n/a | `snapshot_cycle_seconds: 5` -> **5500ms** | 15 -> **16500ms** |

**The snapshot cycle differs by plane and the difference is not incidental.** Perps carries ~1,210
resident levels across 13 perps; sports NFL carries ~12,000 (500 admitted markets at ~24 levels), a
tenfold difference on the same knob. The publisher sets 5 s and 15 s respectively and says so
explicitly (`kalshi_publishers_sports.yml:143-153`: "Same knob, tenfold different input; copying the
number rather than the arithmetic is how it would go wrong"). A single `16500ms` for both planes
would be that mistake. No rule reads `ExpectSnapshotCycle` today — it is parsed and plumbed only
(findings §5.1, §8.1) — which is precisely why it has to be right now, before a rule starts reading
it and the error becomes visible as a finding rather than as a config diff.

`--expect-definition-cycle=33s` enables `REFDATA.DEFINITION_CYCLE_COVERAGE`. The measured NFL run
passed `30s` exactly and produced no findings on that rule, so `33s` carries slack over a value
already known to be clean.

---

## 4. Alerts get scoped before anything emits

### 4.1 The starting state, which is not what the brief assumed

The three rules are stream-agnostic and unfiltered: `dz-conformance-must-violation.json` is
`increase(dz_conformance_violations_total{severity="must"}[5m])` with `for: 0m` at severity `l2`;
`dz-conformance-coverage-loss.json` fires above 5 on `increase(dz_conformance_unverifiable_total[5m])`;
`dz-conformance-transport-loss.json` fires above 0 on `transport_loss_total`. So any new instance is
observed by all three the moment it starts.

**Measured 2026-08-19 — all three rules are already `firing`, on Hyperliquid:**

| rule | state | example instances |
| --- | --- | --- |
| must violation | firing | `FRAME.LENGTH_CONSISTENCY` on `tob_mainnet2`, 435 per 5 min |
| coverage loss | firing | 9 MBO rules incl. `REF.OPERATION_AFTER_REMOVAL` at 16,074 per 5 min |
| transport loss | firing | 4 series across `tob_mainnet2` and `mbo_mainnet2` |

Also measured: **`chi-mn-recorder1` is the only host actually running the validator.**
`dz_conformance_build_info` returns two series, both chi, at 1,367 h uptime. `nyc-mn-recorder1` and
`aws-tyo-mn-recorder1` are in the group and emit nothing.

This changes the argument, not the action. Routing (`malbeclabs/infra:grafana/notification-policies/default.json`)
groups by `alertname` with `repeat_interval: 4h`, so Kalshi instances would fold into an existing
Slack message rather than produce a new page. The reason to scope is therefore **"keep the signal
readable"**, not "stop a new page": a rule that is permanently red cannot report a new regression,
and adding two more permanent reds to it makes the Kalshi deployment unobservable on day one.

The pre-existing HL noise is reported here and deliberately left alone. Triaging 6.6M
`FRAME.LENGTH_CONSISTENCY` violations on the HL TOB plane is real work with an open-ended shape, and
blocking Kalshi coverage behind it trades a known gap for an unknown schedule.

### 4.2 What will fire on the Kalshi streams, and why

**`MSG.WRONG_PORT_PLACEMENT` (`Must`, `tools/conformance/core/registry.go:38`) on both TOB planes.**
`TobFeed::emit_control_both` sends `EndOfSession` (`0x06`) on both ports; the top-of-book spec puts
`0x06` on mktdata only (`top-of-book/spec.md` lines 48 and 104, publisher rule 7: "On shutdown ->
sends EndOfSession on the market data port"). Pre-existing, already documented in
`tob_conformance.sh`'s header, and fires on every session end — so on every publisher restart.

**Permanent coverage loss on both MBP streams.** The snapshot-reconstruction rule declines the large
majority of transitions rather than grading them: on the measured NFL capture, 26,341 unverifiable
split `pending` 18,559 and `cold_start` 7,782, against 5,811 graded. The `pending` reason is a
cross-port race — the snapshot group completes before the delta port drains to `K`, and the checker
discards the opportunity rather than deferring it. At 24% coverage this is far past a threshold of 5
per 5 min, continuously. Note this is *unverifiable*, not violation: the committed code reports 0
violations on that capture (findings §8), so it hits coverage-loss and not must-violation.

**Transport loss is possible but treated as genuine signal.** The shared NIC RX tuning landed on
these hosts 2026-08-18 (`kalshi_feed_capture_apply_nic_rx_tuning: true`, after all three were
measured on stock `rmem_max` with 188k-844k `Udp.RcvbufErrors`), and the `dz_conformance` role
imports `nic_rx_tuning` too. Datagram loss here would be a real finding about the host or the path,
not a known publisher deviation, so this rule is left unscoped.

### 4.3 Mechanism: a PromQL `unless` on the specific (stream, rule_id) pairs

```promql
increase(dz_conformance_violations_total{severity="must"}[5m])
  unless
increase(dz_conformance_violations_total{severity="must",stream=~"^kalshi_",rule_id="MSG.WRONG_PORT_PLACEMENT"}[5m])
```

and the same shape on coverage-loss with `rule_id="MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"`.

Both sides carry identical label sets, so `unless` drops exactly the known pairs and **every other
rule on every Kalshi stream still pages**. That property is the whole point: a blanket
`stream!~"^kalshi_"` would be one matcher instead of two queries, but it would also mean no Kalshi
finding ever reaches Slack, which is a validator that reports to nobody.

Rejected alternatives, for the record:

- **`malbeclabs/infra:grafana/scripts/silence.sh`.** Its silences are duration-bounded by design so they auto-expire
  even if the caller dies (`--duration`, default 1h). A permanent deviation would need a recurring
  job to re-silence, and any lapse in that job reads as a new regression. It is the right instrument
  for a maintenance window, which this is not.
- **A paused Kalshi-only clone rule.** Invisible in the UI and free to drift from the live rule.

Each exclusion carries, in the rule's `annotations.description`, what it hides and the condition for
removing it. Only rules managed under `malbeclabs/infra:grafana/alerts/` are touched, per `malbeclabs/infra:CLAUDE.md`;
`make grafana-lint` gates the edit, and `malbeclabs/infra:grafana/scripts/sync.sh` — the step that reaches Grafana
Cloud — is not run without explicit approval.

---

## 5. Observability plumbing that is easy to miss

`config.alloy.j2:308-317` renders the `dz_conformance` scrape block gated on `'dz_conformance' in
group_names`, iterating `dz_conformance_feeds` and stamping `component` and `stream`. So the block
appears for the Kalshi hosts as soon as they join the group — **but only when the `monitoring` role
next runs there.**

The HL recorders get that via `malbeclabs/infra:ansible/playbooks/update_monitoring.yml`, whose host list includes
`hyperliquid_feed_capture`. The Kalshi recorders are not in that list; they reach `monitoring` only
through `malbeclabs/infra:ansible/playbooks/multicast_recorders.yml`, which also runs `doublezero_client` and
`multicast_recorder` and would therefore restart the capture on a feed-race vantage point.

So `update_monitoring.yml` gains `dz_conformance` in its host list — the group that actually owns the
scrape block, covering both fleets. **Without this step the instances run correctly and emit nothing
to Grafana**, which is the failure this whole design is built to make impossible.

---

## 6. Decisions recorded rather than implemented

### 6.1 `emit_control_both`: allow-list, and file it upstream

The deviation is allow-listed via §4.3's exclusion, and an issue is filed against
`malbeclabs/kalshi` naming `TobFeed::emit_control_both`, the two spec references, and the fact that
this is the behaviour of the **live** perps feed today rather than something a refactor introduced.
The exclusion's removal condition is that issue closing.

The publisher is not touched here. This work is read-only with respect to it, and a wire-behaviour
change on a production feed is not a side effect of standing up a validator.

### 6.2 The reconstruction rule's removal condition

The coverage-loss exclusion is removed when the upstream `pending` deferral lands — parking a
completed snapshot group until the delta port reaches `K`. Findings §8 calls that a real change
rather than a one-liner, and it belongs in `edge-feed-spec`, not here.

---

## 7. What this does **not** catch

Stated so that nobody reads a green conformance dashboard as more coverage than it is.

**The checker has no rule about `BookClear` or `ClearReason` at all.** Departure semantics — a closed
market announced as `Settled` when it should be `SessionEnd` — is entirely invisible to it. It
validates **wire shape, not venue semantics**. A feed can be byte-perfect against the spec and still
be telling subscribers the wrong thing about why a market went away.

That is not a gap to fix in this work; it is a limit to state.

Three further limits:

1. **MBP coverage is thin by construction.** The reconstruction rule grades 24% of transitions and
   declines the rest (§4.2). A silent MBP stream and a clean MBP stream look similar in the alert
   path, which is why the canary verification in the plan asserts
   `dz_conformance_checks_total{result="pass"}` is *advancing* rather than only that violations are
   absent.
2. **Neither cadence rule ever emits a pass**, so a healthy run and a run where the rule never got
   to look are byte-identical in the JSON report (findings §8.1).
3. **One sports channel of thirty-one.** NFL is the only channel with a measured baseline; the other
   30 are unvalidated and stay that way here.

---

## 8. Confirmed live state (2026-08-19)

Read-only checks against Grafana Cloud Prometheus and the two repositories.

| Claim | Method | Result |
| --- | --- | --- |
| No `v0.2.0+` release exists | `gh release list`, `git ls-remote --tags` | one release, one tag |
| `origin/main` carries the MBP engine | `tools/conformance/wire/header.go:24`, `tools/conformance/main.go:24,73`, `tools/conformance/engine/mbp.go` | present |
| `source_id: 3` needs no registry override | inside the embedded registry's accepted 1-1023 range | no override expected |
| `--version` + commit is role-safe | shape regex + Ansible `version(...,semver)` | `+` form safe, space form breaks |
| All three alerts already firing | Grafana rules API | all three `firing` on HL |
| Only chi runs the validator | `dz_conformance_build_info` | 2 series, both chi |
| Metrics ports 9120-9122 free | repo-wide grep | free |

**One stale comment corrected.** `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_dub.yml` states that
`aws-dub-mn-recorder1` "is NOT yet subscribed" to the perps groups. It is: measured
`tob_edge_kalshi_perps` at 290 pps and `mbp_edge_kalshi_perps` at 2,125 pps. This matters because dub
is the chosen canary — if it were still unsubscribed, a clean canary report would have meant nothing.
The note is corrected in the same change.
