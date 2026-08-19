# Kalshi Conformance Deploy Implementation Plan

**Goal:** Run `dz-conformance` live and continuously against three Kalshi edge-feed streams (perps
TOB, perps MBP, sports NFL MBP) on the recorders that already receive those multicast groups, scraped
by Alloy and observed in Grafana — the same machinery that validates the Hyperliquid mainnet2
publisher today.

**Architecture:** Key the checker's frame-level state per `(port, channel_id)` so it stops merging the
two publishers that share every Kalshi port; cut a `conformance/v0.2.0` release that carries that fix,
reads schema 3 and speaks `-feed mbp`; widen the `dz_conformance` role's feed assert to accept `mbp`;
push `dz_conformance_feeds` down from the parent group onto the two child groups so the HL and Kalshi
fleets carry different streams and different version pins; scope the two known Kalshi deviations out
of the paging path, per stream and not per prefix; sync the scoped alerts *before* anything emits;
canary on dub.

**The `(port, channel_id)` keying is the gate on everything else.** Each Kalshi stream is published by
two processes to one group and one port, separable only by the frame header's `Channel ID`
(spec §1.3). The checker discards the datagram sender (`tools/conformance/input/multicast.go:146`) and
keys its sequence tracker on `core.Port` alone (`tools/conformance/engine/engine.go:28`), so it merges
the two arms' independent sequence series: `FRAME.SEQ_RESET_GAP` (`Must`) and
`transport_loss_total` fire continuously, and `dirtyWindow` latches — cleared only on a publisher reset
(`engine/state.go:227-241`) — so every gated detector reports Unverifiable for the process lifetime
while `checks_total{result="pass"}` keeps advancing. Deploying without this fix produces a validator
that pages constantly and grades nothing.

**Tech Stack:** Go 1.25 (`tools/conformance`), Ansible (production `ansible-lint` profile),
ansible-vault, systemd, Grafana Cloud alert JSON, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-19-kalshi-conformance-deploy-design.md`

**Repositories:** this plan spans three checkouts, and paths below are repo-qualified wherever they
are not in this one. Task 1 is **`malbeclabs/edge-feed-spec`** (this repo) — the only compiled code in
the whole change. Tasks 2-5 are **`malbeclabs/infra`**, which is the bulk of it: the `dz_conformance`
role, the inventory, the Grafana alerts and the rollout. Task 6 is **`malbeclabs/kalshi`** — one CI
job. Per that repo's `CLAUDE.md`, capture-side and conformance-side config lives in
`malbeclabs/infra`, not in `malbeclabs/kalshi`, which is why none of Tasks 2-5 land there.

**Testing note (read first):** `malbeclabs/infra` has no molecule/pytest harness for Ansible.
"Verify" steps there use `ansible-lint`, `ansible-playbook --syntax-check`, `ansible-inventory`,
`--check --diff` dry runs, and `make grafana-lint`. Two limits of that toolchain shape the plan and are
worth knowing before trusting a green Verify: **`make grafana-lint` does not parse PromQL** (see Task 4
Verify), and **an inventory change can be proved offline** by copying
`inventory/mainnet-beta/` to a scratch directory, applying the edit there, and diffing
`ansible-inventory --host` output per host against the unmodified tree — which is how Task 3's
precedence claim was established without touching the live inventory. **Every Ansible and Grafana command below runs
from the `malbeclabs/infra` checkout, from its `ansible/` directory unless stated otherwise** — the
shell snippets are written relative to that checkout, not to this one. Task 1's Go commands run here,
from `tools/conformance`.

**Approval gates — do not cross these without asking:**

- No live Ansible run outside the dub canary. `--check --diff` freely.
- No `malbeclabs/infra:grafana/scripts/sync.sh` run without asking. It **is** a required step
  (Task 4 Step 5) and it must run **before** the canary, not after — but it reaches Grafana Cloud, so
  it is gated.
- No canary before Tasks 1a and 1b are merged, `conformance/v0.2.0` is cut, and the scoped alerts are
  live in Grafana.
- No roll to cmh/was before the canary's actual counters have been reported and accepted.

---

## File structure

| File | Change | Responsibility |
| --- | --- | --- |
| **`malbeclabs/edge-feed-spec`** (this repo) | | |
| `tools/conformance/main.go` | Modify | `--version` prints `vX.Y.Z+<commit>` |
| `tools/conformance/main_test.go` | Create | Regression test on the `--version` output shape |
| `tools/conformance/run.go` | Modify | Plumb `version`/`commit`/`strict` into the report writer |
| `tools/conformance/report/json.go` | Modify | Top-level `version`/`commit`/`strict`, per-rule `severity` |
| `tools/conformance/engine/state.go` | Modify | `portTracker` keyed per `(port, channel)` |
| `tools/conformance/engine/engine.go` | Modify | `Engine.ports` key, `lastHbSendTS` per channel |
| `tools/conformance/engine/gate.go` | Modify | `gateDetector`/`gateDetectorSnap` take the channel |
| `tools/conformance/engine/*_test.go` | Modify/Create | Two-channel-on-one-port regression tests |
| `tools/conformance/README.md` | Modify | Document all three |
| **`malbeclabs/infra`** (paths below relative to that checkout) | | |
| `ansible/playbooks/roles/dz_conformance/tasks/main.yml` | Modify | Allow `mbp`; `snapshot_port` for `mbo` and `mbp` |
| `ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml` | Modify | Shared tunables + HL pin only; feeds removed |
| `ansible/inventory/mainnet-beta/group_vars/hyperliquid_feed_capture_mainnet_recorders.yml` | Modify | Receives the mainnet2 feed list |
| `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml` | Modify | `dz_conformance_version: v0.2.0` |
| `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_cmh.yml` | Modify | Three streams |
| `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_was.yml` | Modify | Two perps streams |
| `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_dub.yml` | Modify | Two perps streams; correct the stale subscription note |
| `ansible/inventory/mainnet-beta/hosts.yml` | Modify | `kalshi_feed_capture` under `dz_conformance: children:` |
| `ansible/playbooks/update_monitoring.yml` | Modify | Add `dz_conformance` so Alloy re-renders |
| `ansible/playbooks/dz_conformance.yml` | Modify | `serial: 1` + `force_handlers: true` |
| `ansible/playbooks/roles/monitoring/templates/config.alloy.j2` | Modify | `dz_conformance_feeds \| default([])` |
| `ansible/playbooks/roles/kalshi_feed_capture/defaults/main.yml` | Modify | Re-point the moved port-map reference |
| `grafana/alerts/dz-conformance-must-violation.json` | Modify | Exclude the known port-placement pair |
| `grafana/alerts/dz-conformance-coverage-loss.json` | Modify | Exclude the known reconstruction pair |
| `ansible/README.md` | Modify | Kalshi half of the deploy procedure |
| **`malbeclabs/kalshi`** | | |
| `.github/workflows/publisher-ci.yml` | Modify | `tob-conformance` job (Task 6, optional) |

---

## Task 1: `edge-feed-spec` — attributable builds, then the release

### Task 1a: make a report attributable to a build

**Files:**
- Modify: `tools/conformance/main.go`
- Create: `tools/conformance/main_test.go`
- Modify: `tools/conformance/run.go`
- Modify: `tools/conformance/report/json.go`
- Modify: `tools/conformance/README.md`

- [ ] **Step 1: `--version` prints version and commit**

`tools/conformance/main.go:61` currently prints the bare version. Print `version + "+" + commit` instead.

**The `+` separator is a hard constraint, not a style choice.** `malbeclabs/infra`'s
`malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml:123-146` trims this output, strips a leading `v`, and requires
`^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$`. Verified 2026-08-19: `0.2.0+85fd9b6d40cf` matches and compares
`eq` to `0.2.0` under Ansible's `version_type='semver'` (build metadata is ignored in precedence);
`0.2.0 (85fd9b6d40cf)` does not match. A space, a newline, or a parenthesis makes
`dz_conformance_download_required` permanently true, so every Ansible run re-downloads the binary and
restarts every instance on **both** fleets.

Leave a comment at the print naming that consumer, because the constraint lives in another repo.

- [ ] **Step 2: regression test on the output shape**

New `tools/conformance/main_test.go`. Assert two shapes separately, because the release form and the
unstamped default are different strings and one regex for both is how the test ends up asserting
nothing:

```
release form:   ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?\+[0-9a-f]{7,40}$
default build:  ^dev\+none$
```

**The prerelease case is not hypothetical and an earlier draft of this regex rejected it.**
`.github/workflows/conformance-release.yml:47` validates the input against
`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$` and `:122` sets `PRERELEASE_FLAG="--prerelease"`, so
`v0.2.1-rc.1` is a supported release — and Step 1 always appends `+<commit>`, so the real output is
`v0.2.1-rc.1+abc123def456`. A single optional `([-+][0-9A-Za-z.-]+)?` group cannot match that (the
character class excludes `+`), which would fail CI on the first `-rc` tag. The role's own regex is
`^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$` (`roles/dz_conformance/tasks/main.yml:144`) and does accept it, so
this is a test-only trap — the kind that is only ever found by the release it blocks.

This test is the guard for Step 1's coupling.

- [ ] **Step 3: version, commit, strict and severity in the JSON report**

`tools/conformance/report/json.go`: add top-level `version`, `commit` and `strict`, and `severity` on
each rule entry. Closes findings §8.2 (a report cannot be attributed to a build) and §8.1 (a JSON
reader cannot reconstruct the exit code).

**Two mechanical constraints, both of which change the file list.**

`report.JSONReport(agg *Aggregator, path string)` (`tools/conformance/report/json.go:26`) receives
neither the build metadata nor the config, and `version`/`commit` are package-`main` variables
(`tools/conformance/main.go:15-18`). The call site is `tools/conformance/run.go:232`, so **`run.go` is
part of this step**: carry the two strings on `RunOpts` (they are already assembled in `main`) and pass
them, plus `opts.Cfg.Strict`, into the writer. Do not re-derive them inside `report` — there is nothing
there to derive them from, and an implementation confined to the originally listed files would have to
fabricate them.

`severity` alone does not close §8.1. The exit code is `agg.ExitCode(opts.Cfg.Strict)`
(`tools/conformance/run.go:241`), so a report containing a `Should` violation resolves to 0 or 1
depending on a flag the report does not record. Emit the effective `strict` setting (or the final exit
status) at the top level and cover **both** modes in the test — a report that says
`should: 1` without saying whether strict was on still does not determine the exit code.

- [ ] **Step 4: document both in `tools/conformance/README.md`**

- [ ] **Verify:** `cd tools/conformance && go test ./... && golangci-lint run`. Build locally and
  confirm `--version` prints `dev+none` in the default build and `v0.0.0-test+abc123def456` under
  `-ldflags "-X main.version=v0.0.0-test -X main.commit=abc123def456"`; also check a prerelease form,
  `-X main.version=v0.0.0-rc.1`, against both the test regex and the role's
  (`^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$` after trimming the leading `v`). Run once against a pcap with
  `-json-report` and confirm `version`, `commit`, `strict` and per-rule `severity` are present, then
  re-run the same pcap with `--strict` and confirm the recorded value tracks the flag and the exit
  code.

- [ ] **Step 5: PR to `malbeclabs/edge-feed-spec`**, `Summary of Changes` / `Testing Verification`,
  commit style `conformance: <lowercase description>`. Merge before Task 1c.

### Task 1b: key frame state per `(port, channel_id)`

**Files:**
- Modify: `tools/conformance/engine/state.go`
- Modify: `tools/conformance/engine/engine.go`
- Modify: `tools/conformance/engine/gate.go`
- Modify/Create: `tools/conformance/engine/*_test.go`

**Why this is in the release and not a follow-up.** Every Kalshi stream carries two publisher
processes on one group and one port, separable only by the frame header's `Channel ID` — perps TOB and
perps MBP from `aws-cmh-kl-perps1`/`2` (channels 1 and 101,
`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_cmh.yml:117-132`), sports NFL from
`aws-cmh-kl-sports1`/`2` (channels 10 and 110, `:259-261`). The publisher's contract is explicit:
`malbeclabs/kalshi:app/publisher/crates/kalshi-publisher/src/publisher/transport.rs:174` — "Per-port
stream counters. Each port is an independent sequence series" — per *process* — and `:99` requires "a
subscriber keyed on `(channel_id, reset_count)`". This is the opposite of Hyperliquid, where eleven
publishers share one group but each owns a distinct port pair, which is why the HL deployment does not
exercise this path.

- [ ] **Step 1: key `Engine.ports` per `(port, channel)`**

`tools/conformance/engine/engine.go:28-29` is `ports map[core.Port]*portTracker` — "per-port reorder
buffers and seq trackers (keyed by `core.Port`)". Introduce a `portKey{port core.Port, ch uint8}` and
key on it. `ChannelID` is in the frame header, so it is available at intake with no new plumbing.

- [ ] **Step 2: everything `portTracker` owns follows the key**

`tools/conformance/engine/state.go`: `lastSeq`, `lastSendTS`, `era`, the dedup ring, the reorder buffer
and `dirtyWindow` are all per-stream state and all currently per-port. `advanceEra` (`state.go:227-241`)
is the only place `dirtyWindow` is cleared, so with two arms merged the first alternation latches it and
`gateDetector` returns false for the process lifetime.

- [ ] **Step 3: `lastHbSendTS` becomes per channel**

`engine.go:33-36` is one field for the whole mktdata port. Two arms heartbeating on it interleave into
gaps that never exceed `--expect-heartbeat`, so `HEARTBEAT.CADENCE` cannot fire — a total heartbeat
outage on one arm is masked by the other. That is a false negative rather than a false positive, which
is why it has to be fixed in the same change as the loud half.

- [ ] **Step 4: `gateDetector` / `gateDetectorSnap` take the channel**

`tools/conformance/engine/gate.go:365-381` look up `e.ports[core.PortMktData]` and
`e.ports[core.PortSnapshot]`. Both need the channel of the frame being graded.

- [ ] **Step 5: regression tests for two channels on one port**

Two independent sequence series on one port, interleaved: assert **no** `FRAME.SEQ_RESET_GAP`, **no**
`transport_loss_total`, and that a gated detector still grades (does not return Unverifiable). Then the
converse, so the tests are not vacuous: a real gap *within* one channel must still produce both. The
precedent for the shape is `engine/mbp.go:136-143`, where `mbpState.open` is already "keyed by
channel" for exactly this reason — "a deployment may shard instruments across channels and carry more
than one of them on a port. A single slot made two conformant channels sharing a snapshot port look
like an interleaving publisher" — and the refdata state machine is already per channel
(`engine/refdata.go:154`). Frame sequence is the piece that was left per-port.

**Scope note.** This is an engine change, not a widening, and it is a no-op on Hyperliquid, which is
single-channel per port. Do not substitute a `--channel` filter: it is smaller but doubles the process
count (12 validator instances on cmh instead of 6), needs a second metrics port per stream, and leaves
the merged-series bug in place for anyone who omits the flag.

- [ ] **Verify:** `go test ./... && golangci-lint run`. Then replay a real capture that contains both
  arms — `mbp_edge_kalshi_perps` on `233.84.178.4` carries both (measured 451 packets/5 s from prod1 and
  1,660 from prod2) — and confirm `FRAME.SEQ_RESET_GAP` and `transport_loss_total` are **zero** and the
  gated MBP rules report graded results rather than Unverifiable. Confirm the HL golden pcaps produce a
  byte-identical report to the pre-change build.

- [ ] **Step 6: PR to `malbeclabs/edge-feed-spec`**, commit style `conformance: <lowercase
  description>`. Merge before Task 1c.

### Task 1c: cut `conformance/v0.2.0`

- [ ] **Step 1: dispatch the release workflow**

```bash
gh workflow run conformance-release.yml --repo malbeclabs/edge-feed-spec \
  -f version=v0.2.0 -f ref=main
```

`.github/workflows/conformance-release.yml` refuses an existing tag and an existing release, runs
`golangci-lint` and `go test -v ./...`, and stamps `-X main.version` / `-X main.commit`.

**Do not cut this tag until Tasks 1a and 1b are both merged.** The tag is what the fleet installs, so a
release without the `(port, channel_id)` keying deploys a validator that pages constantly and grades
nothing.

- [ ] **Verify:** the release carries **exactly**
  `dz-conformance_0.2.0_linux_amd64.tar.gz` and `dz-conformance_0.2.0_linux_amd64.tar.gz.sha256` —
  the role's `dz_conformance_asset_name` assert
  (`malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml:102-121`) depends on that shape. Download it and confirm:
  `--version` prints `v0.2.0+<12 hex>`; `-feed mbp` is accepted (v0.1.0 exits 2); `--help` lists
  `mbp` in the `-feed` description; and the Task 1b two-channel replay above still reports zero
  `FRAME.SEQ_RESET_GAP` when run from the released binary rather than a local build.

**Nothing downstream can proceed without this task.**

---

## Task 2: role — widen the feed assert to accept `mbp`

**Files:** Modify `malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml`

- [ ] **Step 1: allow `mbp` at `:13`**

```yaml
      - item.feed in ['tob', 'midpoint', 'mbo', 'mbp']
```

- [ ] **Step 2: require `snapshot_port` for `mbp` too, at `:18`**

```yaml
      - (item.feed not in ['mbo', 'mbp']) or (item.snapshot_port is defined)
```

- [ ] **Step 3: update the `fail_msg` at `:20-22`** to name `mbp` in both the feed list and the
  snapshot-port sentence.

Nothing else in the role changes. `templates/instance.env.j2` already emits `--snapshot-port` whenever
`item.snapshot_port` is defined, already appends `item.extra_args`, and already honours
`item.interface`.

- [ ] **Verify:** `ansible-lint playbooks/roles/dz_conformance/` and
  `ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml --syntax-check`.

---

## Task 3: inventory — give the two fleets different feeds and different pins

**Files:**
- Modify: `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml`
- Modify: `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/hyperliquid_feed_capture_mainnet_recorders.yml`
- Modify: `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml`
- Modify: `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_{cmh,was,dub}.yml`
- Modify: `malbeclabs/infra:ansible/inventory/mainnet-beta/hosts.yml`
- Modify: `malbeclabs/infra:ansible/playbooks/update_monitoring.yml`
- Modify: `malbeclabs/infra:ansible/playbooks/dz_conformance.yml`
- Modify: `malbeclabs/infra:ansible/playbooks/roles/monitoring/templates/config.alloy.j2`
- Modify: `malbeclabs/infra:ansible/playbooks/roles/kalshi_feed_capture/defaults/main.yml`

- [ ] **Step 1: move the mainnet2 feed list onto the HL child group**

Cut the whole `dz_conformance_feeds:` block (`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml:13-26`, `tob_mainnet2` +
`mbo_mainnet2`) and paste it **verbatim** into
`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/hyperliquid_feed_capture_mainnet_recorders.yml`, with a comment stating that child
`group_vars` beat parent `group_vars` and that this is what keeps the mainnet2 streams off the Kalshi
recorders. Carry the existing port-map comment with it.

**Two live files cite that port-map comment by path, and moving it orphans both.** Re-point them in
the same commit — a reservation nobody can find is a reservation the next service will collide with:

- `malbeclabs/infra:ansible/playbooks/roles/kalshi_feed_capture/defaults/main.yml:67-68` — ":9109 is
  reserved for the gossip-proxy in the recorder fleet port map (see
  inventory/mainnet-beta/group_vars/dz_conformance.yml)".
- `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml:95-96` — "the :9109
  gossip-proxy reservation (see dz_conformance.yml port map)".

Leave a one-line pointer in `dz_conformance.yml` naming where the map went, so the path in those two
comments still lands somewhere useful even if one of them is missed.

- [ ] **Step 2: leave `dz_conformance.yml` holding only shared tunables and the HL pin**

Also record what the file no longer guarantees. Until this change, the parent group was a single-point
guarantee that **every** member of `dz_conformance` has a non-empty `dz_conformance_feeds`; afterwards
that lives in four leaf files (`hyperliquid_feed_capture_mainnet_recorders.yml` and the three
`kalshi_feed_capture_<metro>.yml`), and `kalshi_feed_capture.yml` carries the *pin* but not the
*feeds*. So the two variables now resolve at different depths, and a fourth metro added the way `dub`
was would inherit `v0.2.0` with no streams. Name the four files in the comment. Step 8 handles the
consumer that fails badly.

Keep `dz_conformance_version: "v0.1.0"` there as the fleet default and add the reason:

```yaml
# **This is the Hyperliquid pin and it must not be unified with Kalshi's.** The HL
# publisher emits wire schema 1; a release built from edge-feed-spec main expects 3
# (tools/conformance/wire/header.go). v0.2.0 on this feed would mis-size
# InstrumentDefinition (80 vs 128 bytes, Symbol char[16] -> char[64] at schema 2) and
# fire MSG.LENGTH_PER_TYPE — a **must** rule — on every definition frame.
# Kalshi pins v0.2.0 in group_vars/kalshi_feed_capture.yml for the mirror-image reason.
# Two pins is the correct state here, not an untidied one.
```

- [ ] **Step 3: make `kalshi_feed_capture` a child of `dz_conformance`**

In `hosts.yml:376-378`:

```yaml
    dz_conformance:
      children:
        hyperliquid_feed_capture_mainnet_recorders:
        # The Kalshi recorders run a SECOND set of validator instances against the
        # Kalshi edge groups. Their streams and their version pin come from the
        # kalshi_feed_capture_* group_vars, because the two fleets validate different
        # publishers on different wire schemas (see group_vars/dz_conformance.yml).
        kalshi_feed_capture:
```

- [ ] **Step 4: pin Kalshi to `v0.2.0`**

In `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml`, next to the existing `kalshi_feed_capture_version` block:

```yaml
# dz-conformance validator pin for the Kalshi streams (role dz_conformance, joined
# via hosts.yml). **v0.2.0, not the fleet default v0.1.0**, and the two must stay
# separate: v0.1.0 has no MBP engine at all (-feed accepts only tob|midpoint|mbo) and
# hardcodes `SchemaVersion != 1`, while this publisher emits schema 3
# (dz-tob-protocol/src/constants.rs:26, including on the deployed publisher/v0.9.0).
# Against a Kalshi feed v0.1.0 decodes InstrumentDefinition at schema-1 offsets, so
# every refdata-derived rule grades bytes shifted by two. See
# group_vars/dz_conformance.yml for why HL cannot move to v0.2.0.
dz_conformance_version: "v0.2.0"
```

- [ ] **Step 5: per-metro feed lists**

`kalshi_feed_capture_cmh.yml` gets all three streams; `_was.yml` and `_dub.yml` get the two perps
streams. Every group and port is copied from the `kalshi_feed_capture_edge_sources` entry already in
the same file — **no stream that the host does not already join.**

The cmh block, in full (was/dub are this minus `kalshi_sports_mbp_nfl`, and with
`--expect-snapshot-cycle=5500ms` on the one MBP entry):

```yaml
# dz-conformance validator instances for the Kalshi streams on this metro. One
# systemd instance per (feed, port triple); the version pin is in
# group_vars/kalshi_feed_capture.yml.
#
# Groups and ports are COPIED from kalshi_feed_capture_edge_sources above, so this
# adds no IGMP join, no subscriber-allowlist entry and no publisher change — it
# validates traffic the host already receives.
#
# Only NFL of the 31 sports channels. The role is one process per port triple with a
# unique metrics_port, so full sports coverage is 31 processes on this one recorder,
# and NFL is the only channel with a measured conformance baseline
# (malbeclabs/kalshi:docs/superpowers/specs/2026-08-08-conformance-findings.md).
#
# metrics_port 9120-9122: clear of 9090 (multicast_recorder), 9102, 9108
# (hyperliquid-feed-capture), 9109 (gossip proxy), 9110 (kalshi-feed-capture) and
# 12345 (Alloy). HL conformance uses 9094/9096 on the other recorders. The role
# asserts these are unique per host.
dz_conformance_feeds:
  - name: kalshi_perps_tob
    feed: tob
    group: "233.84.178.3"       # edge-kalshi-perps-tob
    mktdata_port: 31000
    refdata_port: 41000
    metrics_port: 9120
    # Cadence expectations at 1.1x the publisher's configured values, NOT copies of
    # them. REFDATA.MANIFEST_CADENCE and HEARTBEAT.CADENCE compare with a bare `>`
    # and no slack (engine/refdata.go:396, engine/engine.go:345), and the measured
    # medians sit above target (manifest 1.000008s, heartbeat 5.000017s) — so exact
    # values violate on ~half of all samples on a healthy feed. Worst overshoot ever
    # measured is 830us on a 1s timer; re-running the same capture at 1100/5500ms
    # exits 0 with both rules silent. Flag-side budget, no code change.
    # Source: malbeclabs/kalshi group_vars/kalshi_publishers_perps.yml:314-316.
    extra_args:
      - --expect-manifest-cadence=1100ms   # 1.1 x manifest_cadence_seconds: 1
      - --expect-heartbeat=5500ms          # 1.1 x heartbeat_interval_seconds: 5
      - --expect-definition-cycle=33s      # 1.1 x refdata_cycle_seconds: 30

  - name: kalshi_perps_mbp
    feed: mbp
    group: "233.84.178.4"       # edge-kalshi-perps-mbp (L2)
    mktdata_port: 32000
    refdata_port: 42000
    snapshot_port: 52000
    metrics_port: 9121
    extra_args:
      - --expect-manifest-cadence=1100ms   # 1.1 x manifest_cadence_seconds: 1
      - --expect-heartbeat=5500ms          # 1.1 x heartbeat_interval_seconds: 5
      - --expect-definition-cycle=33s      # 1.1 x refdata_cycle_seconds: 30
      # 1.1 x snapshot_cycle_seconds: 5 (kalshi_publishers_perps.yml:412). **5s, not
      # sports' 15s** — perps carries ~1,210 resident levels to sports' ~12,000, and
      # the publisher's own comment warns that copying this number rather than the
      # arithmetic is how it goes wrong. No rule reads the flag today (parsed and
      # plumbed only), which is exactly why it must be right before one does.
      - --expect-snapshot-cycle=5500ms

  - name: kalshi_sports_mbp_nfl
    feed: mbp
    group: "233.84.178.20"      # edge-kalshi-sports-mbp, channel 10 (nfl)
    mktdata_port: 34010
    refdata_port: 44010
    snapshot_port: 54010
    metrics_port: 9122
    extra_args:
      - --expect-manifest-cadence=1100ms   # 1.1 x manifest_cadence_seconds: 1
      - --expect-heartbeat=5500ms          # 1.1 x heartbeat_interval_seconds: 5
      - --expect-definition-cycle=33s      # 1.1 x refdata_cycle_seconds: 30
      # 1.1 x snapshot_cycle_seconds: 15 (kalshi_publishers_sports.yml:153).
      - --expect-snapshot-cycle=16500ms
```

- [ ] **Step 6: correct dub's stale subscription note**

`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture_dub.yml:12-13` says the host "is NOT yet subscribed" and lists four
outstanding prerequisites. Measured 2026-08-19: it receives `tob_edge_kalshi_perps` at 290 pps and
`mbp_edge_kalshi_perps` at 2,125 pps. Replace the "not yet subscribed" claim with the confirmation
and the date, keep the client-IP/EIP warning (still load-bearing), and note that this is why dub is a
valid canary rather than a vacuous one.

- [ ] **Step 7: let Alloy see the new instances**

`malbeclabs/infra:ansible/playbooks/update_monitoring.yml` — add `dz_conformance` to the host list:

```yaml
    # dz_conformance owns the Alloy scrape block for the validator instances
    # (roles/monitoring/templates/config.alloy.j2, gated on group_names). The HL
    # recorders arrive here via hyperliquid_feed_capture; the Kalshi recorders reach
    # `monitoring` only through multicast_recorders.yml, which also runs
    # doublezero_client + multicast_recorder and would restart the capture on a
    # feed-race vantage point. Without this line the instances run and emit nothing.
    - dz_conformance
```

Adding the group newly targets exactly the three Kalshi recorders — chi, nyc and aws-tyo already arrive
via `hyperliquid_feed_capture` — and the effect on them is the Alloy config plus the `Restart alloy`
handler (`roles/monitoring/tasks/setup_grafana_alloy.yml:55-65`), nothing else. Checked: every gated
block in `config.alloy.j2` uses literal addresses, so none of them needs a variable that only
`multicast_recorders.yml` supplies, and the play's own `aws_ec2_setup` role already runs on these hosts
from that playbook.

- [ ] **Step 8: make the Alloy scrape block fail small, not fleet-wide**

`malbeclabs/infra:ansible/playbooks/roles/monitoring/templates/config.alloy.j2:311` is
`{% for feed in dz_conformance_feeds %}` with **no `| default([])`**, and the `monitoring` role does not
load the `dz_conformance` role's defaults (`roles/dz_conformance/defaults/main.yml:44`) — a repo-wide
grep finds `dz_conformance_feeds` defined in exactly that one role default and in the inventory. Step 3
put the variable's guarantee into leaf files and Step 7 put the group into a fleet-wide play, so a
member without the variable is now an undefined-variable template failure inside
`update_monitoring.yml`, which also covers qa, controllers, funders, monitors, sentinels, rewards and
hyperliquid. One misconfigured recorder would block a monitoring update for everything.

Add `| default([])`. The failure mode becomes a missing scrape block on one host — which the role's own
assert (`roles/dz_conformance/tasks/main.yml:6`) still catches loudly on the next conformance run —
rather than a fleet-wide play failure. Leave a comment saying why the default is there, because a
`default([])` with no reason reads like defensive noise and gets removed.

- [ ] **Step 9: `serial` and `force_handlers` on `playbooks/dz_conformance.yml`**

```yaml
  # One host at a time: each recorder is a distinct capture vantage point (see
  # multicast_recorders.yml, which sets this for the same fleet and the same reason).
  serial: 1
  # Flush the restart handler even if a later task fails. The role installs the
  # binary at tasks/main.yml:190-193 but restarts only via a handler (:226, :235),
  # and download_required is computed from the on-disk --version (:138-146) — so a
  # failure after the install leaves the new binary on disk with the old process
  # running, and the next run reads the new version, notifies nothing, and the stale
  # process runs indefinitely while every version signal reports the new build.
  force_handlers: true
```

`malbeclabs/infra:ansible/playbooks/multicast_recorders.yml:5-15` sets both for this fleet with that
reasoning written out; the `dz_conformance` role has the identical install-then-handler shape and
therefore the identical trap. This change is what makes it matter: it introduces the fleet's second
version pin and the first-ever install on three hosts, and `ansible.cfg` runs the `free` strategy with
`forks = 10`, so without `serial: 1` Step 6 of Task 5 would restart the near-venue and
far-from-venue vantage points concurrently.

- [ ] **Step 10: correct the now-false `nic_rx_tuning` reasoning on the Kalshi fleet**

`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml:191-194` justifies
`kalshi_feed_capture_apply_nic_rx_tuning: true` with "the second does but runs only on chi/nyc/aws-tyo,
and no kalshi capture host is in that group". Step 3 makes that false — the `dz_conformance` role
imports `nic_rx_tuning` (`roles/dz_conformance/tasks/main.yml:42-45`) and now runs on all three. Say so,
and say that the flag stays `true` regardless: the sysctls are host-wide and persisted, but the
kalshi-capture *restart* that makes a running process pick up the larger buffer only happens from its
own role. A reader who concludes the flag is now redundant would silently lose that restart.

- [ ] **Verify (no live run):**

```bash
cd ansible
ansible-lint playbooks/roles/dz_conformance/ playbooks/roles/monitoring/ \
  playbooks/update_monitoring.yml playbooks/dz_conformance.yml
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml --syntax-check
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/update_monitoring.yml --syntax-check

# The group now resolves to six hosts (3 HL + 3 Kalshi)
ansible-inventory -i inventory/mainnet-beta/hosts.yml --graph dz_conformance

# EVERY member must still resolve a non-empty feed list. This is the invariant the
# parent group used to guarantee on its own (Step 2), so assert it rather than eyeball
# it — and re-run it whenever a metro is added.
for h in $(ansible-inventory -i inventory/mainnet-beta/hosts.yml --graph dz_conformance \
             | grep -o '[a-z0-9-]*recorder1' | sort -u); do
  n=$(ansible-inventory -i inventory/mainnet-beta/hosts.yml --host "$h" \
        | jq '[.dz_conformance_feeds // []] | flatten | length')
  printf '%-24s feeds=%s %s\n' "$h" "$n" "$([ "$n" -gt 0 ] || echo '<-- BROKEN')"
done

# HL must be UNCHANGED: two mainnet2 streams, still pinned v0.1.0
ansible-inventory -i inventory/mainnet-beta/hosts.yml --host chi-mn-recorder1 \
  | jq '{v: .dz_conformance_version, feeds: [.dz_conformance_feeds[].name]}'

# Kalshi: three streams on cmh, two on was/dub, all pinned v0.2.0
for h in aws-cmh-mn-recorder1 aws-was-mn-recorder1 aws-dub-mn-recorder1; do
  ansible-inventory -i inventory/mainnet-beta/hosts.yml --host "$h" \
    | jq --arg h "$h" '{host: $h, v: .dz_conformance_version,
        feeds: [.dz_conformance_feeds[] | {name, feed, metrics_port}]}'
done
```

Expected: chi `v0.1.0` with `tob_mainnet2`/`mbo_mainnet2`; cmh `v0.2.0` with three streams on
9120/9121/9122; was and dub `v0.2.0` with two streams on 9120/9121. Any Kalshi host showing
`tob_mainnet2` means the child-over-parent precedence did not take and Step 1 is wrong. Any host
reporting `feeds=0` means Step 2's invariant has already been lost.

**Measured on a scratch copy of the inventory with Steps 1-5 applied (2026-08-19):** the precedence
holds exactly as described — chi/nyc/aws-tyo keep `v0.1.0` + `tob_mainnet2`/`mbo_mainnet2`, cmh resolves
`v0.2.0` + three streams, was and dub `v0.2.0` + two. There is no shadowing: no other `group_vars` or
`host_vars` file in `mainnet-beta` defines any `dz_conformance_*` variable, so neither
`hyperliquid_feed_capture`, `baremetal_multicast_recorders`, `multicast_recorders`, `ec2_nodes` nor
`all` can win a tiebreak. `hyperliquid_feed_capture_mainnet_recorders` and `kalshi_feed_capture` are
both direct children of `dz_conformance`, and the three `kalshi_feed_capture_<metro>` groups sit one
level below that, so the pin and the feeds resolve in the intended order.

---

## Task 4: scope the alerts before anything emits

**Files:**
- Modify: `malbeclabs/infra:grafana/alerts/dz-conformance-must-violation.json`
- Modify: `malbeclabs/infra:grafana/alerts/dz-conformance-coverage-loss.json`

Two deviations will fire once the instances start (spec §4.2): `MSG.WRONG_PORT_PLACEMENT` (`Must`) on
the **TOB stream only**, on every session end, and permanent coverage loss on the two MBP streams
(26,341 unverifiable, `pending` 18,559 + `cold_start` 7,782, against a threshold of 5 per 5 min).

**Task 1b is a prerequisite for this task, not a parallel one.** Without the `(port, channel_id)`
keying, the merged sequence series add `FRAME.SEQ_RESET_GAP` (`Must`) plus continuous
`transport_loss_total` plus a latched `dirtyWindow` that pushes *every* gated rule into
`unverifiable_total`. No per-pair exclusion can keep either rule readable against that, and the
transport-loss rule this task deliberately leaves unscoped would become the loudest false positive of
the three. Do not edit these files against the pre-fix behaviour.

- [ ] **Step 1: must-violation — exclude the known port-placement pair**

Replace the `refId: A` expression with:

```promql
increase(dz_conformance_violations_total{severity="must"}[5m]) unless increase(dz_conformance_violations_total{severity="must",stream="kalshi_perps_tob",rule_id="MSG.WRONG_PORT_PLACEMENT"}[5m])
```

**`stream="kalshi_perps_tob"` exactly — not `stream=~"^kalshi_"`.** The rule is registered `allFeeds`
(`tools/conformance/core/registry.go:38`), so a prefix would exempt the two MBP streams as well. The
deviation is not theirs: `emit_control_both` exists only in the top-of-book path
(`malbeclabs/kalshi:.../publisher/tob/venue.rs:1297`, `.../publisher/tob_feed.rs:701`,
`.../publisher/tob/perp_stats.rs:137`), and the MBP feed emits `EndOfSession` on mktdata only **with a
regression test asserting it** — `.../publisher/mbp/feed.rs:4255`
`async fn end_of_session_lands_only_on_mktdata()`, and `:4282` "EndOfSession is a mktdata message but
reached {name}". A prefix matcher would blind a `Must` rule in the one place the publisher keeps a test
to stay correct.

- [ ] **Step 2: coverage-loss — exclude the known reconstruction pair**

```promql
increase(dz_conformance_unverifiable_total[5m]) unless increase(dz_conformance_unverifiable_total{stream=~"kalshi_perps_mbp|kalshi_sports_mbp_nfl",rule_id="MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"}[5m])
```

Enumerate the two streams the measurement covers rather than the prefix. The rule is `mbpOnly`
(`registry.go:70`), so today's TOB stream cannot emit it either way — but a prefix would hand the
exemption to every *future* Kalshi MBP stream before anyone assessed it, and adding a stream is exactly
when that assessment should be forced.

Both sides carry identical label sets, so `unless` drops exactly the excluded series and **every
other rule on every Kalshi stream still pages**. `stream` comes from the Alloy scrape
(`malbeclabs/infra:ansible/playbooks/roles/monitoring/templates/config.alloy.j2:312`); `rule_id` and `severity` are native metric labels
(`tools/conformance/report/prom.go:50`, `:56`). Confirmed that `prometheus.relabel "common"`
(`config.alloy.j2:51-70`) only adds `hostname`/`env`/`role` and drops nothing, so `stream` survives to
Grafana.

- [ ] **Step 3: state each exclusion and its removal condition in `annotations.description`**

Port placement: `TobFeed::emit_control_both` sends `EndOfSession` (0x06) on both ports while the
top-of-book spec puts it on mktdata only; pre-existing on the live perps feed; removed when the filed
upstream issue closes. Reconstruction: the rule declines ~76% of transitions as `pending`/`cold_start`
rather than grading them; removed when the upstream `pending` deferral lands.

- [ ] **Step 4: leave `dz-conformance-transport-loss.json` unscoped.** Datagram loss on these hosts
  is a real finding about the host or the path, not a known publisher deviation. NIC RX tuning landed
  2026-08-18, so it may well be quiet.

- [ ] **Verify (pre-sync, and it is deliberately weak — read why):**

```bash
make grafana-lint
grafana/scripts/query.sh promql '<the new full expression>'
grafana/scripts/query.sh promql '<just the right-hand side of the unless>'
```

**`A unless B` with an empty `B` is `A`, so this cannot confirm the exclusion works.** Nothing Kalshi
is emitting yet, so the right-hand side returns nothing and the full expression returns exactly the HL
series it returns today — for **any** right-hand side, including one with the wrong label name, a
misspelled `rule_id`, or a matcher that selects nothing. The lint is no help either: `make grafana-lint`
is `grafana/scripts/lint.sh`, which for alert rules checks required-field presence and warns on a
missing `annotations.summary`. It does not parse PromQL and does not know `stream` is a label.

So what this step establishes is only: the JSON is well-formed, the rules still target the managed
folder, and HL is unaffected. Three things to check by eye, because no tool will:

- `stream`, `rule_id` and `severity` are spelled as they appear in `prom.go:50`/`:56` and
  `config.alloy.j2:312`, and the stream names match `dz_conformance_feeds[].name` from Task 3 Step 5
  character for character.
- Only the `refId: A` `expr` changed. Leave `datasourceUid`, `instant: true`, `range: false`,
  `intervalMs`, `legendFormat`, `relativeTimeRange`, the `refId: C` threshold node (`expression: "A"`,
  `gt 0` / `gt 5`), `noDataState: OK`, `execErrState: Error`, `for`, and `labels.severity` untouched.
  `unless` preserves the left-hand label set, so `{{ $labels.rule_id }}`/`hostname`/`env` templating and
  the `for:` semantics keep working and nothing else needs to move.
- The right-hand side is a strict label-subset of the left. If it is not, `unless` silently drops
  nothing.

**The real verification is Task 5 Step 4b, after the canary emits.**

- [ ] **Step 5: APPROVAL GATE — then sync, because scoping has to happen before anything emits**

This task's premise is that the alerts are scoped *before* the instances start. A gate that stops at
"edited the JSON" does not achieve that: Grafana would still hold the old unscoped rules while Task 5
starts the canary. So the gate is on the sync, and the sync is a step rather than an afterthought.

Ask for approval. Then:

```bash
make grafana-sync                       # grafana/scripts/sync.sh — the step that reaches Grafana Cloud
grafana/scripts/query.sh alert dz-conformance-must-violation | jq '.data[0].model.expr'
grafana/scripts/query.sh alert dz-conformance-coverage-loss  | jq '.data[0].model.expr'
```

Confirm the two live expressions are byte-identical to the files and that
`dz-conformance-transport-loss` is unchanged. Only rules managed under
`malbeclabs/infra:grafana/alerts/` may be modified (`malbeclabs/infra:CLAUDE.md`).

**Do not run the sync without asking, and do not start Task 5 Step 2 until it has run.**

---

## Task 5: dry run, then the dub canary

- [ ] **Step 1: whole-group dry run**

```bash
cd ansible
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml \
  --check --diff --ask-vault-password --limit dz_conformance
```

Read the diff for: HL hosts show **no** env-file change (the move in Task 3 Step 1 must be
byte-neutral); Kalshi hosts show three/two new `*.env` files plus the unit template; the version
resolution prints `v0.2.0` for Kalshi and `v0.1.0` for HL. `--check` skips the download block
(`when: ... and not ansible_check_mode`, role `tasks/main.yml:156`) and the `systemd` task (`:246`) by
design, so this proves rendering, not runtime.

Two things `--check` does **not** skip, both carrying `check_mode: false`: the GitHub release lookup
(`:81-92`) and `dz-conformance --version` (`:123-128`). So on the Kalshi hosts, where the binary does not
exist yet, expect `download_required=true` and `current=not installed` — that is correct output, not a
failure. Templating into the not-yet-created `/etc/dz-conformance` is also fine in check mode (verified),
so the env-file diffs render.

- [ ] **Step 2: canary — `aws-dub-mn-recorder1` only**

**Preconditions, all four:** Task 1a and Task 1b merged; `conformance/v0.2.0` cut from a `main` that
contains both; Task 3 merged; and Task 4 Step 5's sync done, so Grafana already holds the scoped rules.
Starting here with the unscoped rules live contradicts §4's premise, and starting here without Task 1b
produces a validator that pages continuously and grades nothing.

Dub carries just the two perps streams and is the farthest from the venue, so a mistake there does
not disturb the cmh feed-race baseline.

```bash
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml \
  --ask-vault-password --limit aws-dub-mn-recorder1
```

- [ ] **Step 3: Alloy scrape on the canary**

```bash
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/update_monitoring.yml \
  --ask-vault-password --limit aws-dub-mn-recorder1
```

- [ ] **Step 4: verify on the canary**

Following the shape in `malbeclabs/infra:ansible/README.md` §"Deploy Conformance Service":

```bash
# bash -s so process substitution and arrays are available regardless of the login shell.
ssh aws-dub-mn-recorder1 bash -s <<'EOS'
set -u
PORTS="9120 9121"        # add 9122 on cmh (kalshi_sports_mbp_nfl)

systemctl is-active dz-conformance@kalshi_perps_tob dz-conformance@kalshi_perps_mbp

for pt in $PORTS; do
  echo "=== port $pt: healthz ==="
  curl -s "127.0.0.1:$pt/healthz"; echo
  echo "=== port $pt: build_info (expect version=v0.2.0 + a real commit) ==="
  curl -s "127.0.0.1:$pt/metrics" | grep dz_conformance_build_info
done

# Two samples, 30 s apart. checks_total is a cumulative counter, so a single sample
# cannot show it advancing — and a silent stream and a clean stream are otherwise
# indistinguishable.
snap() {
  curl -s "127.0.0.1:$1/metrics" \
    | grep -E '^dz_conformance_(checks|violations|unverifiable|transport_loss)_total'
}
for pt in $PORTS; do snap "$pt" > "/tmp/dzc.$pt.1"; done
sleep 30
for pt in $PORTS; do snap "$pt" > "/tmp/dzc.$pt.2"; done

for pt in $PORTS; do
  echo "=== port $pt: pass counters that MOVED between the two samples ==="
  # Prints one line per rule whose pass counter advanced. NO OUTPUT HERE FAILS THE RUN.
  join -j1 \
    <(grep 'checks_total.*result="pass"' "/tmp/dzc.$pt.1" | awk '{print $1, $2}' | sort) \
    <(grep 'checks_total.*result="pass"' "/tmp/dzc.$pt.2" | awk '{print $1, $2}' | sort) \
    | awk '$3 > $2 { print $1, $2, "->", $3 }'

  echo "=== port $pt: violations by rule/severity ==="
  grep '^dz_conformance_violations_total' "/tmp/dzc.$pt.2" || echo "(none)"
  echo "=== port $pt: unverifiable by rule/reason ==="
  grep '^dz_conformance_unverifiable_total' "/tmp/dzc.$pt.2" || echo "(none)"
  echo "=== port $pt: transport loss (expect 0 — this is the Task 1b check) ==="
  grep '^dz_conformance_transport_loss_total' "/tmp/dzc.$pt.2" || echo "(none)"
  echo "=== port $pt: source-ID violations (expect none) ==="
  grep '^dz_conformance_violations_total' "/tmp/dzc.$pt.2" | grep -i source || echo "(none)"
done
EOS
```

**An empty "pass counters that MOVED" block fails the run.** The loop prints only the rules whose
counter advanced, so silence there means the instance is bound and healthy and inspecting nothing —
which is the failure mode this whole deployment exists to detect, and it looks identical to success in
every other line of the output.

Acceptance:

- both units `active`
- `/healthz` answers on 9120 and 9121
- `dz_conformance_build_info{version="v0.2.0",commit="<12 hex>"}` present **on both instances**
- **`FRAME.SEQ_RESET_GAP` absent and `dz_conformance_transport_loss_total` at 0 on both instances.**
  This is the Task 1b acceptance test in production: both streams carry two publishers on one port, so
  a non-zero value here means the `(port, channel_id)` keying is not in the deployed binary or does not
  work, and the rollout stops. Do not reinterpret it as host or path loss until this reads 0 once.
- **no source-ID rule violations.** `source_id: 3` is inside the embedded registry's accepted 1-1023
  range, so `dz_conformance_source_registry_path` stays empty. If source-ID rules do fire, that is
  the documented trigger for setting it to an on-host registry path.
- `checks_total{result="pass"}` advancing across the two scrapes **on both ports** — a silent stream and
  a clean stream are otherwise indistinguishable, and this is also the only signal that the gated rules
  are alive rather than parked behind a latched `dirtyWindow`
- **gated MBP rules produce graded results, not only Unverifiable.** `checks_total{result="pass"}`
  advancing on ungated Tier-1 rules alone would satisfy the line above while every gated rule is dead
  (spec §1.3), so confirm at least one `MBP.*` rule appears with a `pass` or a `Violation` rather than
  exclusively in `unverifiable_total`
- the only `must` violations are `MSG.WRONG_PORT_PLACEMENT` on the TOB stream; anything else is a new
  finding and stops the rollout

- [ ] **Step 4b: now verify the alert exclusions against live series**

This is the verification Task 4's own Verify step could not perform, because `A unless B` with an empty
`B` is `A`. The canary is emitting, so `B` is finally non-empty:

```bash
# 1. The exclusion selector must select the excluded series, and only those.
grafana/scripts/query.sh promql 'increase(dz_conformance_violations_total{severity="must",stream="kalshi_perps_tob",rule_id="MSG.WRONG_PORT_PLACEMENT"}[5m])'
grafana/scripts/query.sh promql 'increase(dz_conformance_unverifiable_total{stream=~"kalshi_perps_mbp|kalshi_sports_mbp_nfl",rule_id="MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"}[5m])'

# 2. The full expression must no longer carry them, and must still carry everything else.
grafana/scripts/query.sh promql '<the full must-violation expression>'
grafana/scripts/query.sh promql '<the full coverage-loss expression>'
```

Accept only if: selector 1 returns a non-empty result (an empty one means the matcher is wrong — wrong
label, wrong spelling, wrong stream name — and the exclusion has never done anything); the full
expressions no longer contain those series; and they still contain every other Kalshi `rule_id` the
canary is reporting. Then check the live rule states — `dz-conformance-must-violation` must not be
firing *because of* Kalshi, which is only visible now.

**If selector 1 is empty, stop.** The alerts are already synced, so a wrong matcher means the paging
path is unscoped in production and the next Kalshi finding folds into a permanently red rule.

- [ ] **Step 5: APPROVAL GATE — report the canary's actual counters per stream and stop.**

Report per stream: every `violations_total` by `rule_id`/`severity`, every `unverifiable_total` by
`rule_id`/`reason`, `transport_loss_total`, and the `checks_total{result="pass"}` delta. Do not roll
to cmh or was until those numbers have been shown and accepted.

- [ ] **Step 6: after approval — roll to cmh and was**

```bash
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml \
  --ask-vault-password --limit aws-cmh-mn-recorder1,aws-was-mn-recorder1
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/update_monitoring.yml \
  --ask-vault-password --limit aws-cmh-mn-recorder1,aws-was-mn-recorder1
```

With Task 3 Step 9 applied the playbook is `serial: 1`, so the two hosts are done one at a time rather
than concurrently under `ansible.cfg`'s `free` strategy — which is the point, since cmh and was are the
paired near-venue and far-from-venue vantage points.

cmh additionally runs `kalshi_sports_mbp_nfl` on 9122 — verify that unit and port too, including
`build_info` and the two-sample `checks_total` comparison from Step 4, which are what actually prove the
new binary is the one running (`force_handlers` from Task 3 Step 9 closes the trap, but verify rather
than assume). cmh is the near-venue feed-race baseline, so confirm `kalshi-feed-capture` is still
`active` and its `udp_packets_total` still advancing afterwards.

- [ ] **Step 7: document the Kalshi half in `malbeclabs/infra:ansible/README.md`**

Extend §"Deploy Conformance Service" with: the two pins and why they differ; **the fact that the feed
list no longer lives in `group_vars/dz_conformance.yml`** and the four files that now carry it (the
README currently points readers at the old location); the dub-first canary order; the
`update_monitoring.yml` step (and why `multicast_recorders.yml` is the wrong instrument here); the
alert-sync-before-canary ordering; the `serial`/`force_handlers` behaviour and what it protects against;
and the two expected-deviation counters a reader should **not** be alarmed by, with their removal
conditions — plus the one counter that is *not* expected and stops a rollout,
`dz_conformance_transport_loss_total` on a Kalshi stream (Task 1b).

---

## Task 6 (optional, `malbeclabs/kalshi`): wire the offline gate into CI

`app/publisher/crates/kalshi-publisher/examples/tob_conformance.sh` is referenced by no workflow.
As a CI job it becomes a per-PR wire-contract gate against the golden pcaps, independent of everything
above.

- [ ] **Step 1: add a `tob-conformance` job to `.github/workflows/publisher-ci.yml`**

The workflow already filters on `app/publisher/**`, `.github/workflows/publisher-ci.yml`, and
`infra/ansible/inventory/group_vars/**`. Model the job on the existing `test` job (checkout,
`dtolnay/rust-toolchain@1.88.0`, cargo cache), then download the `conformance/v0.2.0` release asset
from `malbeclabs/edge-feed-spec` rather than building Go from source, verify its `.sha256`, and run
the script with the binary's path as `$1`.

**v0.2.0 is a hard dependency.** The script's mandatory counter-control runs `-feed mbp`, which
v0.1.0 rejects with exit 2 — so at the old pin the job cannot pass. The repo is private, so the
download needs a token with read access to `malbeclabs/edge-feed-spec`.

- [ ] **Step 2: leave the script's semantics alone**

It already allow-lists exactly `MSG.WRONG_PORT_PLACEMENT` and already fails when the `-feed mbp`
counter-control does not produce `FRAME.MAGIC_MISMATCH` on **every** frame.

**Know what this gate cannot see.** It replays `cargo run --example tob_golden_replay`, a single
process, so every frame carries one `Channel ID` and one sequence series. That is why the two-publisher
merge in Task 1b was invisible to it, and it stays invisible: this job is a wire-contract gate against
the golden scenarios, not a topology test. Task 1b's own regression tests are what cover two channels on
one port, and they belong in `edge-feed-spec` beside the engine. That counter-control is
not optional: Tier-1 rules are silent on success, so a clean report with no counter-control is
indistinguishable from a checker that never inspected anything. Needs `cargo run --example
tob_golden_replay` and `python3`, both available on `ubuntu-latest`.

- [ ] **Verify:** run the script locally against a v0.2.0 binary first and confirm it exits 0 with
  only the documented deviation, and that the counter-control reports `N/N frames rejected`. Then
  confirm the job passes on the PR that adds it, and deliberately break the counter-control once
  (point it at a `-feed tob` report) to confirm the job actually fails.

- [ ] **Step 3: PR to `malbeclabs/kalshi`**, commit style `publisher: <lowercase description>` or
  `repo:` for the workflow, with `Summary of Changes` / `Testing Verification`.

---

## Out of scope

- `TobFeed::emit_control_both` is not modified — allow-listed for the TOB stream only and filed
  upstream (spec §6.1). The MBP feed is not allow-listed: it already emits `EndOfSession` on mktdata
  only, with a test asserting it (Task 4 Step 1).
- No `--channel` filter and no per-arm instances. The two publishers per port are separated by keying
  the checker's frame state per `(port, channel_id)` (Task 1b), which keeps one instance per stream.
- No `kalshi_publisher_version` or other publisher pin bump; read-only w.r.t. the publisher.
- No HL pin bump (schema 1 vs 3, spec §2.2).
- No 31-channel sports fan-out; no `sports-tob` (`233.84.178.17`), which no host receives.
- No fix for the pre-existing HL alert noise on `chi-mn-recorder1` (spec §4.1) — reported, not touched.
- No secrets in plaintext; no fabricated `!vault` blocks.
