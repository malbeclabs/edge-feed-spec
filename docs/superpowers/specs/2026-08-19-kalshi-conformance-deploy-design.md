# Kalshi conformance deploy: `dz-conformance` against the Kalshi publisher feeds

**Date:** 2026-08-19
**Status:** Design — revised after written-spec review. §1.3 (two publishers per port) is a new
blocking prerequisite found in that review and gates the whole rollout; §4.2/§4.3 narrow the alert
exemptions from a stream prefix to named streams; §4.4, §2.1 and §7 correct claims the review
falsified.
**Correction 2026-08-21, not yet re-reviewed:** §1.3's state key is the **channel instance** —
`(source IP address, Channel ID, destination port)` — and not `(port, channel_id)`. That is a change of
normative unit, it post-dates the review above, and it has not been re-reviewed.
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

**One checker change gates all of it.** Every Kalshi stream carries two publishers on a single port,
and the checker's frame-level state is keyed per port, so it merges their independent sequence series
and stops grading. That is §1.3, it lands in the release rather than in the inventory, and nothing
downstream is meaningful until it does.

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
- **No `--channel` filter, no source-IP filter and no per-arm instances.** The two publishers on each
  port are separated by keying the checker's frame state per **channel instance** — `(source IP
  address, Channel ID, destination port)` — which carries the arm in the key, so there is nothing left
  to filter and one validator per stream grades both arms (§1.3, rejected alternatives).

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

### 1.1 Corrections to the received framing

Recorded because the wrong mechanism in a note becomes the wrong decision later — and one of these was
itself a wrong correction in an earlier draft of this section, which is exactly the failure it warns
about.

**`FRAME.SCHEMA_VERSION` is `Info` severity, not `Must`** — at v0.1.0
(`tools/conformance/core/registry.go:24`) and on main. That much is true and worth keeping: it is not
what pages.

**But v0.1.0 on a Kalshi feed *does* fire a must-violation per definition frame, and an earlier draft
of this section said it did not.** At that tag `expectedMsgLen` returns **80** for
`InstrumentDefinition` (`tools/conformance/engine/tier1.go:31` at `conformance/v0.1.0`), Kalshi emits
**130**, and `MSG.LENGTH_PER_TYPE` is `Must` (`core/registry.go:37`). So the loud effect is present, and
this is the exact mirror image of §2.2 rather than a contrast with it.

**Both effects occur, because the two paths are gated differently.** Tier-1 message checks are gated on
the length matching — `exp := expectedMsgLen(...)`, `lengthOK := exp == 0 || m.Length == exp`
(`engine/tier1.go:292-293`) — so a wrong length suppresses the *downstream* per-field Tier-1 rules and
leaves `MSG.LENGTH_PER_TYPE` itself firing. The refdata state machine has no such gate: it reads
`instrDefAllFields` straight off the message (`engine/refdata.go:655-657`). So v0.1.0 against Kalshi
pages on every definition frame **and** feeds misaligned fields into every refdata-derived rule. The
release is required for both reasons.

**The shift is 50 bytes after `Symbol`, not two.** Per `VERSIONING.md:62`: `2.0.0` widened `Symbol` from
`char[16]` to `char[64]`, "moving every later field and growing the message from 80 to 128 bytes", and
`3.0.0` inserted `Source ID` (`u16`) after `Instrument ID`, "shifting every later field by two bytes and
growing the message to 130 bytes". Schema 1 -> 3 is therefore both changes: `Symbol` itself starts two
bytes late, and everything after `Symbol` lands 50 bytes off (2 + 48). An earlier draft cited only the
two-byte figure, which understates the corruption by a factor of 25 and describes a schema-2-to-3 delta
that no deployed feed exercises.

**Symmetrically, the reason not to bump HL's pin is `MSG.LENGTH_PER_TYPE` (`Must`), not
`FRAME.SCHEMA_VERSION`.** See §2.2.

### 1.2 A report has to be attributable to a build

Findings §8.2 records that `--version` prints the version and never the commit, and that neither
appears in the JSON report — so a report cannot be attributed to a build. Findings §8.1 adds that
`tools/conformance/report/json.go`'s `ruleStatusCounts` carries no `severity`, so a JSON reader cannot reconstruct the
exit code (only `Must` and `Should` move it; an `info` rule never does, with or without `-strict`).

**`severity` alone does not close §8.1, and the writer cannot see the build.** Two mechanical
constraints, both measured:

- The exit code is `agg.ExitCode(opts.Cfg.Strict)` (`tools/conformance/run.go:241`), so a report
  containing a `Should` violation resolves to 0 or 1 depending on `--strict`, which the report does
  not record. The report therefore has to carry the effective `strict` setting (or the final exit
  status) as well as per-rule `severity`.
- `report.JSONReport(agg *Aggregator, path string)` (`tools/conformance/report/json.go:26`) receives
  neither value, and `version`/`commit` are package-`main` variables (`tools/conformance/main.go:15-18`).
  The call site is `tools/conformance/run.go:232`, so `run.go` is part of this change: the build
  metadata is plumbed through `RunOpts` into the writer rather than re-derived inside `report`.

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

### 1.3 Two publishers share every Kalshi port, and the checker merges their sequence series

**This is the blocking prerequisite, and it is a checker change rather than a config one.**

Every stream in §3.1 is published by *two* processes to the same multicast group and the same port,
told apart by the **source address**: each arm binds its egress to a distinct address, so the
discriminator rides in the IP header of every datagram
(`malbeclabs/kalshi:docs/superpowers/specs/2026-07-31-multicast-channel-topology-design.md` §3.1 —
`source_ip` names "the **publisher instance** — the arm", `channel_id` names "the **instrument set**,
one meaning everywhere"). The two arms also carry different channel ids today, and that is incidental
— see the correction below:

| stream | mktdata port | arms | channel ids today |
| --- | --- | --- | --- |
| perps TOB | 31000 | `aws-cmh-kl-perps1`, `aws-cmh-kl-perps2` | 1, 101 |
| perps MBP | 32000 | the same pair, both folding the same book | 1, 101 |
| sports NFL MBP | 34010 | `aws-cmh-kl-sports1`, `aws-cmh-kl-sports2` | 10, 110 |

Sources for the ids: `malbeclabs/kalshi:infra/ansible/inventory/host_vars/aws-cmh-kl-perps1/main.yml:14`
(`kalshi_publisher_channel_id: 1`) and `aws-cmh-kl-perps2/main.yml:20` (`101`), sports via
`kalshi_publisher_channel_id_offset: 100` on `aws-cmh-kl-sports2` (`:48`). The capture config still
records the older framing —
`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_cmh.yml:117-132`,
"separable only by `channel_id` (prod1 = 1, prod2 = 2) ... Measured after that deploy: 451 packets/5 s
from prod1 and 1,660 from prod2 on 233.84.178.4", and `:259-261` for sports — and its measurement
stands even though the discriminator it names does not. The publisher's counters are per *process*:
`malbeclabs/kalshi:app/publisher/crates/kalshi-publisher/src/publisher/transport.rs:204` — "Per-port
stream counters. Each port is an independent sequence series" — and `:112` describes "a subscriber
keyed on `(channel_id, reset_count)`", which `malbeclabs/kalshi` issue **#85** records as the wrong
key for exactly the reason below.

**This is the structural difference from Hyperliquid, and it is why the HL deployment proves nothing
about this one.** Eleven HL publishers share group `233.84.178.15`, but each owns a distinct port pair
(9001/9101/9201/9301/..., `group_vars/hyperliquid_feed_capture_mainnet_recorders.yml`), so HL
conformance genuinely does see one publisher per port. Kalshi is one port, two senders.

The checker is not keyed that way, at either layer:

- **Input.** `tools/conformance/input/multicast.go:146` reads `n, _, err := conn.ReadFromUDP(buf)` —
  the sender address is discarded before the engine can see it — and `input/pcap.go` never reads the IP
  header either, so a replay cannot recover what the live path threw away.
- **Engine.** `tools/conformance/engine/engine.go:28` — "per-port reorder buffers and seq trackers
  (keyed by `core.Port`)". `lastHbSendTS` (`:33-36`) is likewise a single field for the whole mktdata
  port.

Two consequences, both permanent and both present from the first minute:

1. **`FRAME.SEQ_RESET_GAP` (`Must`, `tools/conformance/core/registry.go:64`) fires continuously.**
   Interleaving two independent monotonic series in one tracker produces backward motion on nearly
   every alternation (`engine/engine.go:278-282`), and each forward step of the other arm reads as a
   gap, so `dz_conformance_transport_loss_total` climbs continuously too (`:285-288`).
   `FRAME.SEQ_DUP_DIVERGENT` (`Must`, `registry.go:54`) becomes reachable whenever the two counters
   cross.
2. **Every gated detector degrades to Unverifiable for the process lifetime.** The forward gap sets
   `pt.dirtyWindow = true` (`engine/engine.go:286`), and that flag is cleared in exactly one place —
   on a publisher Reset Count change (`engine/state.go:227-241`). In steady state the reset count
   never advances, so `gateDetector()` (`engine/gate.go:365-371`) returns false forever and every rule
   behind it reports Unverifiable instead of Violation.

Consequence 2 is the one that matters most, because it is silent. The validator keeps passing ungated
Tier-1 rules, so `dz_conformance_checks_total{result="pass"}` keeps advancing while every gated rule
is dead — which defeats the §7 limit-1 guard *and* reproduces exactly the failure §2.1 exists to
prevent: a healthy process validating nothing.

**The fix is to key the frame-level state per channel instance — `(source IP address, Channel ID,
destination port)` — rather than per port**, and it lands in `edge-feed-spec` before the tag, because
the tag is what gets deployed. That unit is defined, and keying on it is a subscriber MUST.
`GLOSSARY.md:37` defines a **channel instance** as "One path's view of one channel, keyed `(source IP
address, Channel ID, destination port)`. The unit that owns a sequence series, a `Reset Count`, and a
snapshot cycle", and `:43` requires it: "**Sequencing keys on the channel instance, never the
channel.** ... a subscriber binding more than one sees an independent series per instance and MUST key
gap detection and recovery state on `(source IP address, Channel ID, destination port)`."
`market-by-price/spec.md` says the same on the wire as of **3.1.0** (PR #25): the datagram header's
`Sequence Number` is "Monotonically increasing **per channel instance** — per source IP address, per
channel, per destination port" (`:87`); the *Redundant Channel Instances* section mints every
identifier per instance in a table and notes that "Only `Sequence Number` keys on the full tuple,
because only it is per port role"; and `Source ID` "cannot serve: it names the matching engine, so
redundant instances of one channel emit the same values".

**Correction, 2026-08-21: the key is the channel instance, not `(port, channel_id)`.** An earlier
revision of this section specified `(port, channel_id)`, and §3.1 and the plan's Task 1b were
written against it. `Channel ID` names the instrument set, not the arm, so under that key two arms
of one channel are one series, and the merge described above survives the fix meant to remove it.
The mistake was invisible because today's arms happen to differ by id — 1 and 101 on perps, 10 and
110 on sports — so `(port, channel_id)` separates them by accident, and every check against the live
feed would have passed. Three things end that. `malbeclabs/kalshi` issue **#86** records that the
perps collapse from `channel_id` 1 and 2 to a single `id` 0 is settled and only its timing is open
(topology gate G8), so the day it lands a `(port, channel_id)` key silently re-merges the two arms —
the failure this section exists to prevent, arriving with no code change and no diff to review. On
the sports plane `channel_id` names the league and, per the same issue, "was never available as an
arm discriminator". And issue **#85** files the publisher's own per-port claim as wrong for the same
reason, against the topology design's normative statement
(`malbeclabs/kalshi:docs/superpowers/specs/2026-07-31-multicast-channel-topology-design.md` §3.1):
"**Sequence and reset counters are scoped to `(source_ip, group, port)`, and that is normative.**"

This is a real engine change, not a widening: `Engine.ports`, `portTracker` (seq, reorder buffer, dedup
ring, era, `dirtyWindow`), the heartbeat baseline, and the `gateDetector`/`gateDetectorSnap` lookups
all have to take the instance — and the source address has to be carried from **both** inputs, the
socket live and the IP header on replay, or an offline replay keys instances differently from the live
instance it exists to reproduce. The per-channel machinery already exists beside it and is the
precedent — `mbpState.open` is "keyed by channel" for this exact reason (`engine/mbp.go:136-143`: "a
deployment may shard instruments across channels and carry more than one of them on a port"), and the
refdata state machine is per channel (`engine/refdata.go:154`). Frame sequence is the piece that was
left per-port, and the source address is the piece that was never read at all.

**One boundary is deliberately not crossed.** Per-instrument sequencing, the reconstructed books, the
snapshot groups and the reference-data state machine stay keyed by channel, so the spec's "A subscriber
consuming more than one instance of a channel MUST key all channel and instrument state by `(source IP
address, Channel ID, …)`" is not met there. That is conservative rather than wrong: the verifiability
gate on those rules becomes the **OR** across the channel's instances, so any instance's loss
downgrades them to Unverifiable instead of blaming the publisher for an anomaly the loss could explain.
`HEARTBEAT.CADENCE` is the deliberate exception and gates on its own instance's window, because its
baseline is per instance — one baseline per port is exactly what let one arm's heartbeats cover the
other arm's total outage.

Rejected alternatives, both for a stronger reason than the earlier draft gave. A `--channel=<id>`
filter with one instance per (stream, arm) is much smaller, but it doubles the process count (12
validator instances on cmh rather than 6), needs a second metrics port per stream, leaves the
merged-series bug in place for anyone who omits the flag, and filters on the field that stops
discriminating arms when #86 lands. A source-IP filter would discriminate, but it is unnecessary: the
key carries the source, so one instance grades both arms as separate series and there is nothing left
to filter. The keying fix is correct for both fleets and is a no-op on Hyperliquid, which runs one
publisher per port.

**Two follow-ups this surfaces, both out of scope here.** `top-of-book/spec.md:76` and
`midpoint/spec.md:78` still define `Sequence Number` as "Monotonically increasing per channel" — PR #25
updated market-by-price and market-by-order only — and the perps TOB stream in §3.1 is a two-arm TOB
feed, so if the channel-instance language should extend to them that is a separate spec PR, not this
deployment. Rescoping the deeper per-instrument, book, snapshot and refdata state to the instance is
materially larger than the frame-state change and belongs in its own task.

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
streams and `dz_conformance.yml` keeps only shared tunables. Verified by applying the change to a
scratch copy of the inventory and running `ansible-inventory --host` over all six hosts: the three HL
recorders keep `v0.1.0` with `tob_mainnet2`/`mbo_mainnet2`, and cmh/was/dub resolve to `v0.2.0` with
their own streams. There is no shadowing — no other `group_vars` file or `host_vars` file in
`mainnet-beta` defines any `dz_conformance_*` variable — and therefore no same-depth tiebreak hazard.

**What the move costs, and what has to replace it.** Today the parent group is a single-point
guarantee that *every* member of `dz_conformance` has a non-empty `dz_conformance_feeds`. Two
consumers rely on it, and they fail very differently:

- The role asserts it and fails loudly (`roles/dz_conformance/tasks/main.yml:6`, with a `[]` default at
  `defaults/main.yml:44`).
- `roles/monitoring/templates/config.alloy.j2:311` iterates it with **no `| default([])`**, and the
  `monitoring` role does not load the `dz_conformance` role's defaults. A member without the variable
  is an undefined-variable template failure inside `update_monitoring.yml` — a fleet-wide play
  covering qa, controllers, funders, monitors, sentinels, rewards and hyperliquid. One misconfigured
  recorder would block a monitoring update for everything.

After the move the guarantee lives in four leaf files, and the group this design joins to
`dz_conformance` (`kalshi_feed_capture`) carries the *pin* but not the *feeds* — those sit one level
deeper, in the three per-metro files. So the two variables now resolve at different depths, and a
fourth metro added the way `dub` was would inherit `v0.2.0` with no streams. The replacement is
therefore explicit rather than implied: `config.alloy.j2` gets `| default([])` so the failure mode is
a missing scrape block rather than a fleet-wide play failure, and `dz_conformance.yml` keeps a comment
naming the four files that now have to carry the list.

### 2.2 Why the pins must differ

The Hyperliquid publisher emits **schema 1** (`hyperliquid/app/publisher/server/`), which is why
v0.1.0 works for it. A release built from main expects 3.

Bumping the fleet-wide `dz_conformance_version` would put v0.2.0 on HL's schema-1 feed, where
`InstrumentDefinition` is **80** bytes and the binary expects **130**
(`tools/conformance/engine/tier1.go:31`). `MSG.LENGTH_PER_TYPE` is `Must`, and it would fire on every
definition frame. **That** is what would page — not `FRAME.SCHEMA_VERSION`, which is `Info`.

**130, not 128 — and the difference matters because 128 is a schema no feed runs.** `VERSIONING.md:62`
records two `MAJOR` steps on the shared non-midpoint `InstrumentDefinition`: `2.0.0` widened `Symbol`
from `char[16]` to `char[64]`, growing the message from 80 to 128 bytes, and `3.0.0` inserted
`Source ID` (`u16`) after `Instrument ID`, growing it to 130. Every non-midpoint feed is at schema 3, so
the live gap is 80 vs 130 and it is the sum of both changes. An earlier draft of this section said
"expects 128" and attributed the whole delta to the `Symbol` widening; that number was also copied into
the permanent `group_vars` comment the plan writes onto **both** fleets, where a wrong constant outlives
the review that introduced it.

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

**Each of these three streams carries two publishers on the one port (§1.3), so the instance count is
one per stream and not one per arm.** That is only correct once the frame state is keyed per channel
instance — `(source IP address, Channel ID, destination port)`; until then a single instance merges the
two arms' sequence series and grades nothing. The count of *processes* is therefore a consequence of
the §1.3 fix, not an independent decision.

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
alert matchers in §4 select on, and the Grafana feed-race dashboard buckets on a leading plane prefix
the same way the capture ids do.

**The prefix is a naming convention, not an alert matcher — and an earlier draft of this design made it
one, incorrectly.** That draft used `stream=~"^kalshi_"` in §4.3 to avoid an enumeration that has to be
edited whenever a stream is added. It failed twice over:

1. **It matched nothing.** Prometheus anchors regex label matchers fully — `=~"foo"` compiles to
   `^(?:foo)$` — so `stream=~"^kalshi_"` becomes `^(?:^kalshi_)$` and matches only the literal label
   value `kalshi_`, never `kalshi_perps_tob`. The exclusion was a no-op, so the deviation would have
   paged exactly as if the alert had never been edited.
2. **Repairing it to `kalshi_.*` would then be wrong for the opposite reason.** A working prefix hands
   the exemption to every current *and future* Kalshi stream, which for a `Must` rule means a stream
   nobody assessed inherits an exemption (§4.2).

§4.3 therefore matches each deviating stream by exact name. The cost of editing the matcher when a
stream is added is the point — a new stream pages until someone decides it should not — and an
equality matcher cannot be silently defeated by anchoring.

### 3.3 Cadence expectations are a 1.1x budget, derived not copied

`REFDATA.MANIFEST_CADENCE` and `HEARTBEAT.CADENCE` compare with a bare `>` and no slack
(`tools/conformance/engine/refdata.go:396`, `tools/conformance/engine/engine.go:379`). The measured medians sit *above* target — manifest
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

`--expect-definition-cycle=33s` arms **three** rules, not one, and two of them are `Must`
(`tools/conformance/engine/config.go:39-52`):

| rule | severity | registry |
| --- | --- | --- |
| `REFDATA.DEFINITION_CYCLE_COVERAGE` | `Must` | `core/registry.go:106` |
| `REFDATA.NEVER_REACHES_READY` | `Must` | `core/registry.go:112` |
| `REFDATA.NO_BURST_DEFINITIONS` | `Should` | `core/registry.go:113` |

This matters for the canary gate, not just for completeness: the acceptance criterion stops the rollout
on any `must` violation other than `MSG.WRONG_PORT_PLACEMENT`, so an armed `Must` rule nobody listed
will stop it and look like a new finding. The measured NFL run passed `30s` exactly with no findings on
the coverage rule, so `33s` carries slack over a value already known to be clean — but that measurement
covers one of the three, and the other two are armed by the same flag.

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

**`MSG.WRONG_PORT_PLACEMENT` (`Must`, `tools/conformance/core/registry.go:38`) on the TOB stream, and
only there.** `TobFeed::emit_control_both` sends `EndOfSession` (`0x06`) on both of that feed's ports;
the top-of-book spec puts `0x06` on mktdata only (`top-of-book/spec.md` lines 48 and 104, publisher
rule 7: "On shutdown -> sends EndOfSession on the market data port"). Pre-existing, already documented
in `tob_conformance.sh`'s header, and fires on every session end — so on every publisher restart.

**The MBP streams do not share this deviation, and the exemption must not reach them.** The rule is
registered `allFeeds`, so a stream-prefix matcher would exempt MBP too — but `emit_control_both` exists
only in the top-of-book path (`publisher/tob/venue.rs:1297`, `publisher/tob_feed.rs:701`,
`publisher/tob/perp_stats.rs:137`), and the MBP feed does the opposite *and has a regression test
asserting it*: `publisher/mbp/feed.rs:4255` `async fn end_of_session_lands_only_on_mktdata()`, with
`:4282` "EndOfSession is a mktdata message but reached {name}". Exempting `kalshi_perps_mbp` or
`kalshi_sports_mbp_nfl` from a `Must` rule would blind the alert path in precisely the place the
publisher keeps a test to stay correct. §4.3 therefore matches `stream="kalshi_perps_tob"` exactly.

**Permanent coverage loss on both MBP streams.** The snapshot-reconstruction rule declines the large
majority of transitions rather than grading them: on the measured NFL capture, 26,341 unverifiable
split `pending` 18,559 and `cold_start` 7,782, against 5,811 graded. The `pending` reason is a
cross-port race — the snapshot group completes before the delta port drains to `K`, and the checker
discards the opportunity rather than deferring it. At 24% coverage this is far past a threshold of 5
per 5 min, continuously. Note this is *unverifiable*, not violation: the committed code reports 0
violations on that capture (findings §8), so it hits coverage-loss and not must-violation.

**Coverage loss also depends on the §1.3 fix, and without it no exclusion can make the rule
readable.** A latched `dirtyWindow` degrades *every* gated detector to Unverifiable, so
`dz_conformance_unverifiable_total` climbs across many `rule_id`s rather than the one the exclusion
names. Excluding a single rule cannot keep a rule readable when the cause is fleet-wide, which is why
§1.3 is a prerequisite for §4 and not a parallel workstream.

**Transport loss is treated as genuine signal, and that is only safe after the §1.3 fix.** The shared
NIC RX tuning landed on these hosts 2026-08-18 (`kalshi_feed_capture_apply_nic_rx_tuning: true`, after
all three were measured on stock `rmem_max` with 188k-844k `Udp.RcvbufErrors`), and the
`dz_conformance` role imports `nic_rx_tuning` too. With the fix in place, datagram loss here is a real
finding about the host or the path rather than a known publisher deviation, so this rule stays
unscoped. **Without the fix it is not:** merged sequence series make every alternation between the two
arms read as a forward gap, so `dz_conformance_transport_loss_total` would climb continuously on all
three streams and the one rule this design deliberately leaves unscoped would be the loudest false
positive of the three. That is the second reason §1.3 gates the rollout.

### 4.3 Mechanism: a PromQL `unless` on the specific (stream, rule_id) pairs

```promql
increase(dz_conformance_violations_total{severity="must"}[5m])
  unless
increase(dz_conformance_violations_total{severity="must",stream="kalshi_perps_tob",rule_id="MSG.WRONG_PORT_PLACEMENT"}[5m])
```

and on coverage-loss, enumerating the two streams the measurement actually covers:

```promql
increase(dz_conformance_unverifiable_total[5m])
  unless
increase(dz_conformance_unverifiable_total{stream=~"kalshi_perps_mbp|kalshi_sports_mbp_nfl",rule_id="MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"}[5m])
```

Both sides carry identical label sets, so `unless` drops exactly the known pairs and **every other
rule on every Kalshi stream still pages**. That property is the whole point: a blanket
`stream!~"kalshi_.*"` would be one matcher instead of two queries, but it would also mean no Kalshi
finding ever reaches Slack, which is a validator that reports to nobody.

**Every matcher names its streams; none of them is a prefix.** Two independent reasons, and the first
one is a correctness bug rather than a judgement call:

- **`stream=~"^kalshi_"` matches no series at all.** Prometheus anchors regex label matchers fully
  (`=~"foo"` compiles to `^(?:foo)$`), so that matcher becomes `^(?:^kalshi_)$` and selects only a label
  whose entire value is `kalshi_`. An earlier draft of this design used exactly that form on both rules,
  which means neither deviation would have been scoped out at all.
- **A repaired prefix (`kalshi_.*`) would over-suppress.** `MSG.WRONG_PORT_PLACEMENT` is registered
  `allFeeds` and `MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT` is `mbpOnly`
  (`tools/conformance/core/registry.go:38`, `:70`), so a working prefix would extend each exemption to
  streams where the deviation is unmeasured — the MBP planes for the first (§4.2), and every future
  Kalshi MBP stream for the second.

The exemption is per (stream, rule_id) because that is the granularity at which the deviations were
measured. Note the surviving regex, `stream=~"kalshi_perps_mbp|kalshi_sports_mbp_nfl"`, is correct under
full anchoring — it compiles to `^(?:kalshi_perps_mbp|kalshi_sports_mbp_nfl)$` — and any future
alternation has to be written the same way: no leading `^`, no trailing `.*` needed.

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

### 4.4 The exclusion has to be verified against live series, not against an empty result

**`A unless B` with an empty `B` is `A`.** So checking the new expressions while nothing Kalshi is
emitting returns "unchanged, HL untouched" for *any* right-hand side — including one whose label name,
`rule_id` spelling, or matcher is wrong. That check is tautological, and it is the only one the
tooling offers: `make grafana-lint` is `grafana/scripts/lint.sh`, which for alert rules verifies
required-field presence and warns on a missing `annotations.summary`. It does not parse PromQL and
does not know that `stream` is a label.

**This is how the earlier `^kalshi_` bug survived its own review.** The matcher selected nothing, so the
`unless` dropped nothing, so the expression returned the HL series unchanged — which is precisely the
"no-op today" the verify step was written to look for. A broken matcher and a correct one produced
identical output, and the check was reported as passing.

Three consequences for the rollout order, all carried into the plan:

1. **The mechanism is provable before the canary, by substituting a stream that IS emitting.** HL is
   emitting today, so building the exclusion clause against a live HL `(stream, rule_id)` pair — say
   `stream="tob_mainnet2"`, `rule_id="FRAME.LENGTH_CONSISTENCY"` — and confirming those series
   *disappear* from the full expression proves the `unless`, the label names and the anchoring all work.
   This is not tautological and it does not wait for Kalshi. Revert to the Kalshi pair afterwards.
2. **The real matcher is verified on its own, once the canary emits.** Querying only the exclusion
   selector must return the specific series it is meant to drop, and it can only do that after the
   instances start. So: sync the scoped rules, start the canary, then confirm the selector actually
   selects, and confirm the full expression no longer carries those series while still carrying every
   other Kalshi rule.
3. **The sync happens before the canary, not after.** §4's premise is that alerts are scoped *before
   anything emits*; an approval gate that stops at "edited the JSON" leaves Grafana holding the old
   unscoped rules while the canary starts. The gate is therefore on the sync itself, and the sync is a
   step, not an afterthought.

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

Adding the group newly targets exactly the three Kalshi recorders (chi and nyc already arrive via
`hyperliquid_feed_capture`, and so does aws-tyo), and the effect on them is the Alloy config plus the
`Restart alloy` handler — nothing else. Every gated block in `config.alloy.j2` uses literal addresses,
so none of them depends on a variable that only `multicast_recorders.yml` supplies; the play's own
`aws_ec2_setup` role already runs on these hosts from that playbook. The two things this step does
*not* fix, and which the plan handles separately:

- **`config.alloy.j2:311` has no `| default([])`.** See §2.1. Adding the group to a fleet-wide play is
  what turns that into a fleet-wide failure mode, so the default lands in the same change.
- **`playbooks/dz_conformance.yml` has neither `serial` nor `force_handlers`.** The sibling playbook
  for the same fleet has both, with the reasoning written out
  (`ansible/playbooks/multicast_recorders.yml:5-15`): `serial: 1` because "each recorder is a distinct
  capture vantage point, so a version bump must not restart the whole fleet at once (ansible.cfg runs
  the free strategy with forks=10, which otherwise would)", and `force_handlers: true` because a
  failure after the binary is installed "leaves the new binary on disk with the service still on the
  old build, and the next run's precheck reads the new on-disk version, so download_required=false and
  no restart is ever notified — the stale process runs indefinitely while every version signal reports
  the new build". The `dz_conformance` role has the identical shape — install at
  `roles/dz_conformance/tasks/main.yml:190-193`, restart only via a handler (`:226`, `:235`),
  `download_required` computed from the on-disk `--version` (`:138-146`) — so it inherits the identical
  trap. This design introduces the fleet's *second* version pin and the first-ever install on three
  hosts, which is when that trap starts costing something.

---

## 6. Decisions recorded rather than implemented

### 6.1 `emit_control_both`: allow-list, and file it upstream

The deviation is allow-listed **for `kalshi_perps_tob` only** via §4.3's exclusion, and an issue is
filed against `malbeclabs/kalshi` naming `TobFeed::emit_control_both`, the two spec references, and
the fact that this is the behaviour of the **live** perps feed today rather than something a refactor
introduced. The exclusion's removal condition is that issue closing.

**The MBP feed is deliberately not allow-listed**, even though `MSG.WRONG_PORT_PLACEMENT` is
registered `allFeeds`. It already emits `EndOfSession` on mktdata only and keeps a test saying so
(§4.2), so an exemption there would remove paging from a rule the publisher is actively holding
correct. That asymmetry is the reason §4.3 names streams instead of matching a prefix.

The publisher is not touched here. This work is read-only with respect to it, and a wire-behaviour
change on a production feed is not a side effect of standing up a validator.

### 6.2 The reconstruction rule's removal condition

The coverage-loss exclusion is removed when the upstream `pending` deferral lands — parking a
completed snapshot group until the delta port reaches `K`. Findings §8 calls that a real change
rather than a one-liner, and it belongs in `edge-feed-spec`, not here.

### 6.3 Source ID `3` is Kalshi, and this repo owns the row

`sources/spec.md:24` reads `| 3 | Lashay | | |` — assigned, but with a blank `Kind` and no note. That is
not an unrelated tenant: `kalshi_feed_capture_cmh.yml:27-29` records that the capture ids were
`tob_lashay_1`/`mbp_lashay_2` until 2026-08-11, because "lashay" was the group's name "before the edge
feeds were renamed to `edge-kalshi-*` on the DZ ledger". So ID `3` is the Kalshi publisher under its
former name, and the registry that grades it is owned in **this** repository.

Fill the row in (`Kind`, and a note naming the rename) as part of Task 1a. Two reasons it is not
cosmetic: the embedded registry is what `TOB.QUOTE.SOURCE_ID_REGISTRY` checks against, so a reader
auditing a source-ID finding lands on a row that identifies nothing; and if the assumption is wrong — if
ID `3` is *not* Kalshi — then the recorded pin is wrong and the canary's "no source-ID violations"
criterion is measuring the wrong thing. Either way the row has to say which.

---

## 7. What this does **not** catch

Stated so that nobody reads a green conformance dashboard as more coverage than it is.

**The checker has no rule about `ClearReason` — and that is the narrow claim, because `BookClear`
itself is checked.** An earlier draft of this section said there was no rule about either, which
overstates the gap and misdirects: a malformed `BookClear` *does* produce a finding. `BookClear` is
graded for length and port placement (`tools/conformance/engine/tier1.go:81`, `:147`, `:210`), its
scope/side combination is validated by `MBP.DELTA.ABSOLUTE_APPLY` — "a range BookClear names one side"
(`tools/conformance/core/ruledoc.go:88`), emitted as a `Violation` at
`tools/conformance/engine/mbp.go:285` for `Scope = 1` with `Clear Side = Both` — and it shares the
per-instrument sequence series with `LevelUpdate` under `MBP.DELTA.PERINSTR_DENSITY`
(`ruledoc.go:86`, "across LevelUpdate and BookClear alike").

What is genuinely absent is the *reason* field: `ClearReason` appears nowhere in
`tools/conformance/` (grepped), and there is no accessor for it in `engine/mbp_fields.go`. So
departure semantics — a closed market announced as `Settled` when it should be `SessionEnd` — is
invisible. The checker validates **wire shape, not venue semantics**: a feed can be byte-perfect
against the spec, and produce a well-formed `BookClear` that grades clean, while telling subscribers
the wrong thing about why the market went away.

That is not a gap to fix in this work; it is a limit to state — and stating it at the right width
matters, because an operator told "no `BookClear` validation at all" would not know that a malformed
clear already pages.

Three further limits:

1. **MBP coverage is thin by construction.** The reconstruction rule grades 24% of transitions and
   declines the rest (§4.2). A silent MBP stream and a clean MBP stream look similar in the alert
   path, which is why the canary verification in the plan asserts
   `dz_conformance_checks_total{result="pass"}` is *advancing* rather than only that violations are
   absent.
2. **Neither cadence rule ever emits a pass**, so a healthy run and a run where the rule never got
   to look are byte-identical in the JSON report (findings §8.1).
3. **The two cadence rules are not symmetric on the page path, and the heartbeat half pages nowhere.**
   `REFDATA.MANIFEST_CADENCE` is `Must` (`core/registry.go:105`) but `HEARTBEAT.CADENCE` is `Info`
   (`core/registry.go:62`). `Info` never moves the exit code, with or without `--strict`, and never
   matches the must-violation rule's `severity="must"` selector — so a real heartbeat stall on a Kalshi
   stream reaches no alert and no non-zero exit. It is visible only by reading the metric. (§1.3 adds a
   second, independent reason the heartbeat rule is blind pre-fix: one `lastHbSendTS` for two
   interleaved arms.)
4. **A 1.1x budget on a bare `>` hides a real cadence regression.** Both rules compare a single observed
   gap against the expectation with no slack and no distributional test
   (`engine/refdata.go:396`, `engine/engine.go:379`). With `--expect-manifest-cadence=1100ms`, a
   publisher that degraded to a ~1.09 s *mean* manifest cadence — a 9% regression on a 1 s timer —
   produces no gap over the threshold and stays permanently invisible. The budget buys silence on a
   healthy feed (§3.3) at the cost of a blind band directly above target; that is the right trade for a
   `Must` rule that would otherwise fire on half of all samples, but it is a blind band and not a
   tolerance.
5. **Source-ID identity is never validated; only its range is, and only on TOB.** The single source-ID
   rule is `TOB.QUOTE.SOURCE_ID_REGISTRY` — `Must`, but `tobOnly` (`core/registry.go:123`) — and it
   checks membership of the accepted ranges (`engine/sources.go:26-36`), not that the ID identifies the
   publisher it claims. On MBP there is no check at all: `Source ID` is documented in the message layout
   but has no accessor (`engine/mbp_fields.go:12-32`), so two of the three deployed streams do not
   inspect it even for range. A clean source-ID line in the canary output therefore means "the TOB
   stream's IDs are inside 1-1023", not "the publisher is who it says it is".
6. **One sports channel of thirty-one.** NFL is the only channel with a measured baseline; the other
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
| Metrics ports 9120-9122 free | repo-wide grep (both repos) | free |
| Two publishers per Kalshi port | `kalshi_feed_capture_cmh.yml:117-132`, `:259-261`; `transport.rs:112,204` | confirmed, all three streams |
| Checker discards the arm discriminator | `input/multicast.go:146` discards the sender; `input/pcap.go` never reads the IP header | confirmed; the fix carries the source in the key rather than filtering on it (§1.3) |
| Seq/reorder state is per port | `engine/engine.go:28`; `dirtyWindow` cleared only in `state.go:227-241` | confirmed (§1.3) |
| The unit is the channel instance, not `(port, channel_id)` | `GLOSSARY.md:37,43`; `market-by-price/spec.md` 3.1.0 *Redundant Channel Instances*; topology design §3.1; `malbeclabs/kalshi` #85, #86 | §1.3 corrected 2026-08-21 |
| `emit_control_both` is TOB-only | `tob/venue.rs:1297`; MBP test `mbp/feed.rs:4255` asserts mktdata-only | confirmed |
| `BookClear` is graded, `ClearReason` is not | `ruledoc.go:86,88`; `mbp.go:285`; grep for `ClearReason` | §7 corrected |
| Exit code depends on `--strict` | `run.go:241` `agg.ExitCode(opts.Cfg.Strict)` | report must record it (§1.2) |
| Child group_vars beat the parent here | `ansible-inventory --host` on all six hosts, change applied to a scratch inventory | HL unchanged, Kalshi as intended |
| `make grafana-lint` does not parse PromQL | `grafana/scripts/lint.sh` — required fields + a summary warning only | verification widened (§4.4) |
| Prometheus anchors regex matchers fully | `=~"foo"` compiles to `^(?:foo)$` | `^kalshi_` matched nothing; §3.2/§4.3 corrected |
| v0.1.0 on Kalshi fires `MSG.LENGTH_PER_TYPE` | `engine/tier1.go:31` at `conformance/v0.1.0` returns 80 vs the 130 emitted | §1.1 was inverted, corrected |
| `InstrumentDefinition` is 80 / 128 / 130 at schema 1 / 2 / 3 | `VERSIONING.md:62`; `engine/tier1.go:31` on main | §1.1/§2.2 constants corrected |
| Refdata decode is not length-gated | `engine/refdata.go:655-657` vs `engine/tier1.go:292-293` | both effects occur |
| `--expect-definition-cycle` arms three rules | `engine/config.go:39-52`; `registry.go:106`, `:112`, `:113` | two `Must`, one `Should` (§3.3) |
| `HEARTBEAT.CADENCE` is `Info` | `core/registry.go:62` vs `:105` | pages nowhere (§7 limit 3) |
| `Source ID` has no MBP accessor | `engine/mbp_fields.go:12-32` | unchecked on 2 of 3 streams (§7 limit 5) |
| Source ID `3` is recorded as "Lashay", `Kind` blank | `sources/spec.md:24` | filled in as Kalshi (§6.3) |

**One stale comment corrected.** `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_dub.yml` states that
`aws-dub-mn-recorder1` "is NOT yet subscribed" to the perps groups. It is: measured
`tob_edge_kalshi_perps` at 290 pps and `mbp_edge_kalshi_perps` at 2,125 pps. This matters because dub
is the chosen canary — if it were still unsubscribed, a clean canary report would have meant nothing.
The note is corrected in the same change.
