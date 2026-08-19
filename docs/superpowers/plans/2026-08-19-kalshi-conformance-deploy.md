# Kalshi Conformance Deploy Implementation Plan

**Goal:** Run `dz-conformance` live and continuously against three Kalshi edge-feed streams (perps
TOB, perps MBP, sports NFL MBP) on the recorders that already receive those multicast groups, scraped
by Alloy and observed in Grafana — the same machinery that validates the Hyperliquid mainnet2
publisher today.

**Architecture:** Cut a `conformance/v0.2.0` release that can read schema 3 and speak `-feed mbp`;
widen the `dz_conformance` role's feed assert to accept `mbp`; push `dz_conformance_feeds` down from
the parent group onto the two child groups so the HL and Kalshi fleets carry different streams and
different version pins; scope the two known Kalshi deviations out of the paging path; canary on dub.

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
`--check --diff` dry runs, and `make grafana-lint`. **Every Ansible and Grafana command below runs
from the `malbeclabs/infra` checkout, from its `ansible/` directory unless stated otherwise** — the
shell snippets are written relative to that checkout, not to this one. Task 1's Go commands run here,
from `tools/conformance`.

**Approval gates — do not cross these without asking:**

- No live Ansible run outside the dub canary. `--check --diff` freely.
- No `malbeclabs/infra:grafana/scripts/sync.sh` run (the step that reaches Grafana Cloud).
- No roll to cmh/was before the canary's actual counters have been reported and accepted.

---

## File structure

| File | Change | Responsibility |
| --- | --- | --- |
| **`malbeclabs/edge-feed-spec`** (this repo) | | |
| `tools/conformance/main.go` | Modify | `--version` prints `vX.Y.Z+<commit>` |
| `tools/conformance/main_test.go` | Create | Regression test on the `--version` output shape |
| `tools/conformance/report/json.go` | Modify | Top-level `version`/`commit`, per-rule `severity` |
| `tools/conformance/README.md` | Modify | Document both |
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

New `tools/conformance/main_test.go`: assert the `--version` output matches
`^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`, and that the default `dev`/`none` build still
produces something the test can reason about. This test is the guard for Step 1's coupling.

- [ ] **Step 3: version, commit and severity in the JSON report**

`tools/conformance/report/json.go`: add top-level `version` and `commit`, and `severity` on each rule entry. Closes
findings §8.2 (a report cannot be attributed to a build) and §8.1 (a JSON reader cannot reconstruct
the exit code, because only `Must`/`Should` move it and the report carries no severity).

- [ ] **Step 4: document both in `tools/conformance/README.md`**

- [ ] **Verify:** `cd tools/conformance && go test ./... && golangci-lint run`. Build locally and
  confirm `--version` prints `dev+none` in the default build and `v0.0.0-test+abc123def456` under
  `-ldflags "-X main.version=v0.0.0-test -X main.commit=abc123def456"`. Run once against a pcap with
  `-json-report` and confirm `version`, `commit` and per-rule `severity` are present.

- [ ] **Step 5: PR to `malbeclabs/edge-feed-spec`**, `Summary of Changes` / `Testing Verification`,
  commit style `conformance: <lowercase description>`. Merge before Task 1b.

### Task 1b: cut `conformance/v0.2.0`

- [ ] **Step 1: dispatch the release workflow**

```bash
gh workflow run conformance-release.yml --repo malbeclabs/edge-feed-spec \
  -f version=v0.2.0 -f ref=main
```

`.github/workflows/conformance-release.yml` refuses an existing tag and an existing release, runs
`golangci-lint` and `go test -v ./...`, and stamps `-X main.version` / `-X main.commit`.

- [ ] **Verify:** the release carries **exactly**
  `dz-conformance_0.2.0_linux_amd64.tar.gz` and `dz-conformance_0.2.0_linux_amd64.tar.gz.sha256` —
  the role's `dz_conformance_asset_name` assert
  (`malbeclabs/infra:ansible/playbooks/roles/dz_conformance/tasks/main.yml:102-121`) depends on that shape. Download it and confirm:
  `--version` prints `v0.2.0+<12 hex>`; `-feed mbp` is accepted (v0.1.0 exits 2); `--help` lists
  `mbp` in the `-feed` description.

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

- [ ] **Step 1: move the mainnet2 feed list onto the HL child group**

Cut the whole `dz_conformance_feeds:` block (`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml:13-26`, `tob_mainnet2` +
`mbo_mainnet2`) and paste it **verbatim** into
`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/hyperliquid_feed_capture_mainnet_recorders.yml`, with a comment stating that child
`group_vars` beat parent `group_vars` and that this is what keeps the mainnet2 streams off the Kalshi
recorders. Carry the existing port-map comment with it.

- [ ] **Step 2: leave `dz_conformance.yml` holding only shared tunables and the HL pin**

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

- [ ] **Verify (no live run):**

```bash
cd ansible
ansible-lint playbooks/roles/dz_conformance/ playbooks/update_monitoring.yml
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/dz_conformance.yml --syntax-check
ansible-playbook -i inventory/mainnet-beta/hosts.yml playbooks/update_monitoring.yml --syntax-check

# The group now resolves to six hosts (3 HL + 3 Kalshi)
ansible-inventory -i inventory/mainnet-beta/hosts.yml --graph dz_conformance

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
`tob_mainnet2` means the child-over-parent precedence did not take and Step 1 is wrong.

---

## Task 4: scope the alerts before anything emits

**Files:**
- Modify: `malbeclabs/infra:grafana/alerts/dz-conformance-must-violation.json`
- Modify: `malbeclabs/infra:grafana/alerts/dz-conformance-coverage-loss.json`

Two deviations will fire the moment the instances start (spec §4.2): `MSG.WRONG_PORT_PLACEMENT`
(`Must`) on both TOB planes on every session end, and permanent coverage loss on both MBP streams
(26,341 unverifiable, `pending` 18,559 + `cold_start` 7,782, against a threshold of 5 per 5 min).

- [ ] **Step 1: must-violation — exclude the known port-placement pair**

Replace the `refId: A` expression with:

```promql
increase(dz_conformance_violations_total{severity="must"}[5m]) unless increase(dz_conformance_violations_total{severity="must",stream=~"^kalshi_",rule_id="MSG.WRONG_PORT_PLACEMENT"}[5m])
```

- [ ] **Step 2: coverage-loss — exclude the known reconstruction pair**

```promql
increase(dz_conformance_unverifiable_total[5m]) unless increase(dz_conformance_unverifiable_total{stream=~"^kalshi_",rule_id="MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"}[5m])
```

Both sides carry identical label sets, so `unless` drops exactly the excluded series and **every
other rule on every Kalshi stream still pages**. `stream` comes from the Alloy scrape
(`malbeclabs/infra:ansible/playbooks/roles/monitoring/templates/config.alloy.j2:312`); `rule_id` and `severity` are native metric labels
(`tools/conformance/report/prom.go`).

- [ ] **Step 3: state each exclusion and its removal condition in `annotations.description`**

Port placement: `TobFeed::emit_control_both` sends `EndOfSession` (0x06) on both ports while the
top-of-book spec puts it on mktdata only; pre-existing on the live perps feed; removed when the filed
upstream issue closes. Reconstruction: the rule declines ~76% of transitions as `pending`/`cold_start`
rather than grading them; removed when the upstream `pending` deferral lands.

- [ ] **Step 4: leave `dz-conformance-transport-loss.json` unscoped.** Datagram loss on these hosts
  is a real finding about the host or the path, not a known publisher deviation. NIC RX tuning landed
  2026-08-18, so it may well be quiet.

- [ ] **Verify:** `make grafana-lint`. Then dry-run both expressions read-only against Grafana Cloud
  and confirm the exclusion is a no-op today (nothing Kalshi is emitting yet) and that HL series are
  untouched:

```bash
grafana/scripts/query.sh promql '<the new expression>'
```

- [ ] **APPROVAL GATE:** do **not** run `malbeclabs/infra:grafana/scripts/sync.sh`. Only rules managed under
  `malbeclabs/infra:grafana/alerts/` may be modified (`malbeclabs/infra:CLAUDE.md`), and the sync is what reaches Grafana Cloud. Ask
  first.

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
resolution prints `v0.2.0` for Kalshi and `v0.1.0` for HL. `--check` skips the download block and the
`systemd` task by design, so this proves rendering, not runtime.

- [ ] **Step 2: canary — `aws-dub-mn-recorder1` only**

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
ssh aws-dub-mn-recorder1 '
  systemctl is-active dz-conformance@kalshi_perps_tob dz-conformance@kalshi_perps_mbp
  echo "--- healthz ---"
  curl -s 127.0.0.1:9120/healthz; echo; curl -s 127.0.0.1:9121/healthz; echo
  echo "--- build_info (expect version=v0.2.0 and a real commit) ---"
  curl -s 127.0.0.1:9120/metrics | grep dz_conformance_build_info
  echo "--- rules actually ran (pass must be ADVANCING, not merely present) ---"
  curl -s 127.0.0.1:9120/metrics | grep "dz_conformance_checks_total" | grep "pass"
  echo "--- source-ID violations (expect NONE) ---"
  curl -s 127.0.0.1:9120/metrics | grep dz_conformance_violations_total | grep -i source
  echo "--- all violations by rule/severity ---"
  curl -s 127.0.0.1:9120/metrics | grep "^dz_conformance_violations_total"
  curl -s 127.0.0.1:9121/metrics | grep "^dz_conformance_violations_total"
  echo "--- unverifiable by rule/reason ---"
  curl -s 127.0.0.1:9121/metrics | grep "^dz_conformance_unverifiable_total"
'
```

Acceptance:

- both units `active`
- `/healthz` answers on 9120 and 9121
- `dz_conformance_build_info{version="v0.2.0",commit="<12 hex>"}` present
- **no source-ID rule violations.** `source_id: 3` is inside the embedded registry's accepted 1-1023
  range, so `dz_conformance_source_registry_path` stays empty. If source-ID rules do fire, that is
  the documented trigger for setting it to an on-host registry path.
- `checks_total{result="pass"}` advancing across two scrapes — a silent stream and a clean stream are
  otherwise indistinguishable
- the only `must` violations are `MSG.WRONG_PORT_PLACEMENT` on the TOB stream; anything else is a new
  finding and stops the rollout

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

cmh additionally runs `kalshi_sports_mbp_nfl` on 9122 — verify that unit and port too. cmh is the
near-venue feed-race baseline, so confirm `kalshi-feed-capture` is still `active` and its
`udp_packets_total` still advancing afterwards.

- [ ] **Step 7: document the Kalshi half in `malbeclabs/infra:ansible/README.md`**

Extend §"Deploy Conformance Service" with: the two pins and why they differ; the dub-first canary
order; the `update_monitoring.yml` step (and why `multicast_recorders.yml` is the wrong instrument
here); and the two expected-deviation counters a reader should **not** be alarmed by, with their
removal conditions.

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
counter-control does not produce `FRAME.MAGIC_MISMATCH` on **every** frame. That counter-control is
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

- `TobFeed::emit_control_both` is not modified — allow-listed and filed upstream (spec §6.1).
- No `kalshi_publisher_version` or other publisher pin bump; read-only w.r.t. the publisher.
- No HL pin bump (schema 1 vs 3, spec §2.2).
- No 31-channel sports fan-out; no `sports-tob` (`233.84.178.17`), which no host receives.
- No fix for the pre-existing HL alert noise on `chi-mn-recorder1` (spec §4.1) — reported, not touched.
- No secrets in plaintext; no fabricated `!vault` blocks.
