# Fleet conformance: continuous validation of every edge feed in testnet and mainnet-beta

**Date:** 2026-09-09
**Status:** Design — revised twice on 2026-09-09. Second revision, after operator review: §3's
"reuse the recorder hosts" is withdrawn (§12 replaces it), and §11 adds the second pipeline. First
revision, after reading the venue conformance suites: §1.3, §7 and
§9 previously called the venue suites duplicates of this tool and proposed deleting one. They are a
different layer and nothing is deleted; that is corrected here. §4 moves the port table out of this
public repository. §10 is new.
**Branch:** `specs/fleet-conformance`
**Plan:** §2 is `docs/superpowers/plans/2026-09-09-fleet-conformance-a1-multi-schema-decoder.md` (phase A1). The rest of phase A (§3, §4, deploy) is phase A2, not yet planned.

**Repositories.** Bare paths (`tools/conformance/...`) are `malbeclabs/edge-feed-spec`. Everything
else is repo-qualified. The Ansible role, inventory and Grafana alerts live in `malbeclabs/infra`.
The feed catalog and the Go serviceability SDK live in `malbeclabs/doublezero`. Per-venue CI lives in
each venue repository. The design is recorded here because the checker changes land here and because
the rule severities that decide what pages are defined here.

## Goal

Every feed registered onchain is validated continuously, from a subscriber's vantage, in both
testnet and mainnet-beta. A venue gets coverage by registering its feed, not by anyone editing a
list. **Never at the cost of a dropped packet on a capture host — see §12.**

There are two end-to-end pipelines, not one (§11). This document covers the first, which ends at the
wire specs. The second ends at Edge Connect's JSON contract and needs its own checker and its own
design.

Four failures must be caught, in this order of priority:

1. A live feed went bad. It stopped emitting, dropped conformance, gapped its sequence, or stalled
   its reference data.
2. A publisher regressed before it shipped.
3. A subscriber cannot consume the feed, because of the access pass, the tunnel, the group join, or
   the ledger, rather than the bytes.
4. Latency regressed.

## Non-goals

- **No publisher change.** Nothing here bumps a publisher version or alters what any publisher
  emits. The one exception is the testnet canary in §5, which is a new publisher of recorded data.
- **No ledger change.** No serviceability program change and no account migration. §4 resolves ports
  without one, deliberately.
- **No book-builder and no ClickHouse.** §6 says why the Playbook's Phase 9 harness is not needed for
  the latency question.
- **Pipeline B is not in scope.** §11 records the Edge Connect path and says why it needs a
  separate checker; phases A through D do not build it.
- **Nothing here is worth a dropped capture.** §12 makes the pcap recorders' loss budget the binding
  constraint on where and how validators run, and withdraws the earlier draft's assumption that the
  recorder hosts were spare capacity.
- **No venue test suite is deleted.** §7 explains why the publisher-side suites and this tool are
  two layers rather than two implementations of one.
- **No replacement for the venue capture recorders.** Publisher-side conformance stays where it is.
  This adds a subscriber-side view; it does not remove the publisher-side one.

## 1. What exists today

Measured on 2026-09-09 against both ledgers and both repositories.

**The catalog.** Mainnet-beta holds 306 `Feed` accounts, one per SKU and metro:

| feed code | metros |
| --- | --- |
| `edge-binance-usdsm-tob` | 31 |
| `kalshi-elections-pol-tob`, `kalshi-elections-pol-mbp` | 31, 31 |
| `kalshi-perps-tob`, `kalshi-perps-mbp` | 30, 30 |
| `kalshi-sports-tob`, `kalshi-sports-mbp` | 30, 30 |
| `phoenix-tob`, `phoenix-mbp` | 31, 31 |
| `solana-shreds-full` | 30, three groups each |
| `qa-payments` | 1 |

Testnet holds 12: `edge-shreds` in 8 metros, `edge-solana-retrans`, two developer feeds, and
`qa-payments`. No venue feed exists in testnet.

Hyperliquid and edge-builder have no feed account in either environment. Both are expected to
register later. The design in §4 requires no work when they do.

**The coverage.**

| venue | pre-deploy pcap gate | live conformance | subscriber-side check | latency |
| --- | --- | --- | --- | --- |
| binance | yes, `malbeclabs/binance:scripts/conformance.sh` in CI | no | no | no |
| kalshi | no; an example script no workflow runs | 3 of 30 metros | no | no |
| hyperliquid | publisher-side suite, no shared-tool gate | recorder hosts | no | no |
| phoenix | publisher-side suite, emits a pcap nothing reads | no | no | no |
| solana-shreds-full | no | no | no | no |
| edge-builder | no | no | no | no |

### 1.1 Coverage is hand-listed while the fleet is discovered

306 feeds exist. `dz-conformance` runs on about four capture hosts, and which feeds it checks is
written by hand in `malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/`. That file already
records where the approach breaks:

> The two variables therefore resolve at different depths, so a fourth Kalshi metro added the way
> `dub` was would inherit `v0.2.0` with no feeds.

A list maintained by hand against a catalog that grows onchain will always lag it. The lag is
silent, because a feed nobody listed produces no alert.

### 1.2 It watches from the publisher's side

Conformance runs on capture and recorder hosts near the publisher. That grades the bytes the
publisher writes. It does not grade what a subscriber in a distant metro receives over the Edge.
The second is the product, and it is the one with the tunnel, the access pass, the group join and
the network path in it.

### 1.3 Two layers exist and neither is named as a layer

Read quickly, the venues look like four implementations of one thing: the shared tool (binance), a
shell example (kalshi), a Rust crate (`malbeclabs/hyperliquid:app/publisher/conformance/`) and Rust
integration tests (phoenix). They are not. There are two layers here, and the confusion comes from
both being called conformance.

**Publisher-side, white box.** Hyperliquid and phoenix test their own encoder. Hyperliquid's crate
drives the real encoder with crafted state, replays recorded fixtures through the real pipeline, and
runs mutation sensitivity over hand-authored byte vectors: flip a byte at a field's offset, assert
exactly that field's decoded value moves. That is what proves a test is not echoing the encoder it
tests, and it audits 21 of 21 emitted message types for offset, size, endianness and signedness.

**Subscriber-side, black box.** `dz-conformance` sees datagrams and grades them against a rule
catalog. It never sees an encoder, so it cannot do any of the above.

Neither layer can replace the other. What is genuinely missing is the join between them, and phoenix
has already designed it:

> Setting `DZ_CONFORMANCE_PCAP_OUT` additionally writes the capture as a pcap for
> `edge-feed-spec/tools/conformance`. The tests never invoke that checker: a Rust test binary that
> shells out to Go is a test that fails on machines where the sibling repo is absent.

Phoenix emits the pcap and has no CI step reading it. Binance has the CI step. Nobody has both, and
nobody has written the handoff down as the standard. §7 does that.

## 2. The blocking prerequisite: a multi-schema decoder

`dz_conformance_version` is pinned per venue, and the pin is load-bearing. Hyperliquid emits wire
schema 1; Kalshi emits schema 3. A release built for one mis-sizes `InstrumentDefinition` against
the other, by 50 bytes, and fires `MSG.LENGTH_PER_TYPE` — a **must** rule — on every definition
datagram.

A fleet mode sees every venue at once, so it cannot carry a per-venue pin. This has to be solved
before anything else in this document is possible.

**Change.** Dispatch the `InstrumentDefinition` layout on the `Schema Version` byte the frame header
already carries, instead of on the build. One binary reads schema 1 and schema 3. The 80-byte
(schema 1) and 130-byte (schema 3) layouts both live in `tools/conformance/wire/`, selected per
datagram.

Schema 2 is not implemented. No deployed feed runs it, and a decoder that claims a layout it has
never seen on the wire is worse than one that rejects it. An unknown `Schema Version` stays a
rejection.

**This bends a stated rule.** `VERSIONING.md` says a decoder MUST reject a `Schema Version` it was
not built for. A validator that accepts several versions is a deliberate exception, for this tool
only, and `VERSIONING.md` must record it as such. Production consumers keep the rule.

**Why this is the right shape.** The pin is not configuration. It is the same knowledge held in two
places: the byte on the wire, and a string in a group_vars file that a human has to keep equal to
it. Deleting the second place deletes the whole class of failure, including the one the group_vars
comment describes.

**Outcome.** `dz_conformance_version` becomes one value for the fleet. The per-venue pins in
`malbeclabs/infra:ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml` and
`kalshi_feed_capture.yml` are deleted, along with the comment explaining them.

## 3. Fleet mode

A new mode in `dz-conformance`. It reads the feed catalog from the ledger and runs one checker per
feed group, in one process. A feed usually carries one group; `solana-shreds-full` carries three, so
the unit is the group and not the feed.

```
ledger (Feed accounts) ──► fleet mode ──► one checker per group ──► Prometheus ──► existing alerts
                                │
                          multicast, joined on a subscriber host
```

**Discovery.** Read `Feed` accounts with the Go serviceability SDK
(`malbeclabs/doublezero:sdk/serviceability`). Follow `Feed.groups` to each `MulticastGroup` account
and take `multicast_ip`. Select the rule set from the feed code suffix (`-tob`, `-mbp`). Resolve
ports per §4.

Re-read the catalog on an interval. A feed added onchain starts being checked without a deploy; a
feed deleted onchain stops being checked without one.

**Feeds this catalog does not cover.** `solana-shreds-full` and `edge-shreds` carry Solana shreds,
not an edge-feed-spec wire format, so no rule set applies to them and none should be invented here.
Fleet mode classifies each discovered feed as *graded* or *ungraded*, and counts both. An ungraded
feed still gets the §3.1 liveness and join series, which is the part of the check that does not
depend on the wire format, and it raises no conformance alert. The coverage table in §1 marks shreds
as unchecked because nothing watches it at all today, not because the rule catalog is missing.

**Where it runs.** An earlier draft said: put it on the `multicast_recorders` hosts, they already
exist and already join the groups, so the sentinel is just another service. **That is withdrawn.**
Those hosts write the pcaps, the pcaps are the lifeline, and they are not free capacity. §12 sets
the placement rule and the evidence behind it. Read §12 before deploying anything from this section.

**Filtering.** A recorder in one metro should check the feeds joinable from that metro, not all 306.
`Feed.exchange` names the metro, so the host filters the catalog by its own exchange. That is the
default. A `--feed-filter` flag stays available for a host that should carry a narrower or wider set.

**Failure isolation.** One feed that cannot be joined must not stop the other checkers. A join
failure is a metric and a log line, not an exit. The process exits only when it cannot reach the
ledger at all, which is a real and separate alert.

### 3.1 Layer 3 falls out of this

The subscriber-side check does not need its own test. The sentinel cannot receive a single datagram
unless the access pass, the tunnel, the route and the group join all work. Its receiving is the
subscriber check.

That only holds if the sentinel is loud about not receiving. It must export, per feed:

- whether the group join succeeded,
- time since the last datagram,
- whether the tunnel and its routes are present on the host.

A checker sitting silently on a group it never joined must read differently from one on a healthy
quiet market. Without those three series, layer 3 is not covered and this section is a claim rather
than a check.

## 4. Port resolution

`MulticastGroup` carries `multicast_ip` and no port. The mktdata, refdata and snapshot split is a
convention from the reference-data spec, not onchain state.

**Change.** The tool gains a `--feed-ports` flag reading a table keyed by feed code, giving the port
for each role. **The table lives in `malbeclabs/infra`, which is private, and not in this
repository, which is public.** See §10.

An earlier draft embedded the table here, alongside the pinned source registry the tool already
ships. That was wrong on disclosure grounds: a venue-keyed port table in a public repository names
every venue in it, including any that has not announced. The code stays public because it names no
venue. The table that names them stays private, next to the deploy configuration that already does.

No ledger change and no migration across 306 accounts. A venue launch adds a row to the private
table, reviewed in `infra`.

**The cost, stated.** A feed registered onchain with no registry row cannot be checked. That is a
silent gap of exactly the kind §1.1 objects to, so it must not be silent: fleet mode counts feeds it
discovered but could not resolve, and an alert fires on a non-zero count. Discovering an unknown
feed is then a page, not a shrug.

Putting ports onchain is the better end state and is out of scope here. It needs a serviceability
program change and an account migration, and it would block every layer behind it.

## 5. The testnet canary

Register one feed in testnet, published by a replay publisher looping a recorded capture. The
testnet `multicast_recorders` already write captures to S3 under the `testnet` prefix, so the sample
is on hand.

The canary's `Feed.exchange` must be a metro that holds a testnet recorder, or the §3 exchange filter
excludes it from every host and the canary is never checked. Pick the exchange from the testnet
inventory, and pin that dependency in the plan.

Two jobs.

**A rehearsal target.** A publisher release can soak against a testnet feed before it reaches
mainnet-beta.

**A monitor that has been seen to fail.** The canary exists to be broken on purpose. This is the
acceptance test for the whole document:

| break | expected alert |
| --- | --- |
| stop the publisher | liveness, time since last datagram |
| skip sequence numbers | `FRAME.SEQ_*`, transport loss |
| emit an unknown `Schema Version` byte | schema rejection, not a mis-decode |
| stall reference data | refdata coverage |
| leave the group unjoined | join failure, distinct from a quiet market |
| register a feed with no registry row | unresolved-feed count non-zero (§4) |

Each break must produce its alert and only its alert. A monitor nobody has watched fail is not a
monitor.

## 6. Latency

The Playbook's Phase 9 asks for a book-builder that writes per-update rows to ClickHouse. For the
latency question that is more machinery than the question needs.

The sentinel already holds both numbers. It reads the publisher's send timestamp from every datagram
header to keep its per-instance baseline, and it knows when it received the datagram.

**Change.** Export `wire_latency_ns` as a Prometheus histogram, labelled by feed, source and metro.
Percentile alerting follows from the existing Grafana setup.

**The caveat travels with the number, wherever it appears.** This is a wall-clock difference between
two hosts. It includes clock skew. It is a useful relative measure and not an absolute latency.

ClickHouse rows and a book-builder are worth adding when somebody needs per-instrument price
analysis. That is a different question from latency, and it should be built when it is asked for.

## 7. Layer 2: one pcap gate, everywhere

**Nothing is deleted.** §1.3 establishes that the publisher-side suites and this tool are two
layers. Every venue keeps its suite. What spreads is the handoff between the layers, which phoenix
already built and nobody standardised.

**The convention.** A venue's publisher-side suite writes its capture to a pcap. CI runs
`dz-conformance --pcap` over that capture and fails the pull request on a violation. The suite never
shells out to the checker, for the reason phoenix gives: a Rust test binary that calls a Go tool in
a sibling repository fails on any machine without that repository.

Per venue:

- **binance** — has the CI step (`malbeclabs/binance:scripts/conformance.sh`). It is the reference.
- **phoenix** — emits the pcap already, via `DZ_CONFORMANCE_PCAP_OUT`. Add the CI step that reads it.
- **hyperliquid** — keeps `app/publisher/conformance/` in full. Add a pcap emission and the CI step.
- **kalshi** — promote `app/publisher/crates/kalshi-publisher/examples/tob_conformance.sh` from an
  example nothing runs into a CI job.
- **edge-builder** — add the gate before its first feed registers.
- **edge-publisher-template** — add both halves to the template, so a new venue inherits the suite
  shape and the gate. Add the §4 port-table row to the Playbook's Phase 1 Source ID step.

**Where a venue rule outgrows the venue.** A check a publisher-side suite performs that the shared
catalog cannot express is a rule proposal against the catalog, raised here. Hyperliquid's `GAPS.md`
and `TESTING-LIMITATIONS.md` are the first inputs, and `TESTING-LIMITATIONS.md` is the more useful
of the two: it records rules that are true of the spec but undrivable in-process, which is exactly
the class a live subscriber-side checker can reach and a white-box suite cannot. The shutdown
`Valid = 0` ManifestSummary is one such rule.

That direction is the point. The two layers cover each other's blind spots, so a gap found in one is
a candidate rule for the other.

## 8. Phases

Each phase is independently shippable and leaves the fleet better than it found it. Each phase gets
its own implementation plan; this document is too broad for one.

**Phase A — layer 1.** §2 multi-schema decoder. §4 port registry. §3 fleet mode. Delete the
per-venue pins. Every registered mainnet-beta feed is checked continuously from a subscriber host.

**Phase A does not deploy onto the recorder fleet.** §12 rule 1 puts validators on separate
subscriber hosts, and §12 rule 4 gates any rollout on a before-and-after capture-loss baseline in one
metro. The §2 decoder work is independent of all of this and ships regardless: it is a change to what
one binary can decode, with no deployment topology in it.

**Phase B — layer 2.** §7. Every venue gates on the shared tool in CI. Hyperliquid's crate is
retired. The template carries the gate forward.

**Phase C — layer 3.** §3.1 join, liveness and tunnel series. §5 testnet canary, and the break table
run end to end. This is where the monitor is proven.

**Phase D — layer 4.** §6 latency histogram, dashboard and alert.

Phase C proves phases A and B. If the break table cannot be made to pass, the earlier phases are
unverified regardless of how green they look.

## 9. Risks

**A subscriber-side checker grades the network as well as the publisher.** A datagram lost on the
Edge and a datagram never sent look similar from a distant metro. The tool's existing two-tier model
already handles this correctly: loss-explicable anomalies report `unverifiable` rather than
violation. Alerting must read `unverifiable_total` by reason, not just violation counts, or a broken
network will read as a clean feed. This is the same trap the tool's README describes, arriving from
a new direction.

**One process, many checkers, on hosts that already drop packets when one core saturates.** This
was written as a resource-profile curiosity and it is not. A metro carries roughly nine gradeable
feeds against the two or three validator instances a host runs today, and §12.2 documents an
observed loss path — GRE from one tunnel endpoint hashes to a single RX queue, that core saturates,
the NIC ring drops. Bounded per-feed state exists (`maxChannelInstances`); a bound on what the
process does to its host does not. §12 is the mitigation and it is a precondition, not a follow-up.

**Grading one pipeline and reporting it as coverage.** §11: a clean pipeline-A result says the bytes
on the wire were right. It says nothing about what Edge Connect served, which is what most consumers
actually read. The risk is not the gap, which is known; it is a dashboard that shows green and does
not say which surface it graded.

**The registry becomes a second catalog.** §4 accepts a per-feed table outside the ledger. The
unresolved-feed alert is what keeps it honest. If that alert is ever silenced, §4 has quietly become
§1.1.

**The two layers can drift into one.** §7 keeps both. The failure mode is a later reading of this
document that sees two things called conformance and consolidates them, deleting the mutation
sensitivity and field-level audit that only a white-box suite can do. §1.3 and §7 say why that is a
loss, and the non-goals say it outright.

**The private port table can go stale where the public code cannot.** §4 splits the code from the
table that configures it, across a repository boundary and a visibility boundary. The
unresolved-feed alert in §4 is the only thing making that split safe. If it is ever silenced, §4 has
become §1.1 with an extra repository in the way.

## 10. Repository split and disclosure

Some venues are not publicly associated with DoubleZero. Half the repositories here are public, so
where a thing lives decides what it discloses.

| public | private |
| --- | --- |
| `doublezero`, `edge-feed-spec`, `edge-multicast-ref`, `doublezero-edge-connect`, `lake` | `infra`, `binance`, `kalshi`, `hyperliquid`, `phoenix`, `miax`, `edge-builder`, `edge-publisher-template` |

The split follows one rule: **code that names no venue is public, and anything that names a venue is
private.**

- **Fleet mode** (§3) is public, here. It hardcodes no venue. It reads whatever the ledger returns
  and grades it by feed type.
- **The port table** (§4) is private, in `infra`. It is keyed by feed code, so it names venues.
- **Per-venue pcap gates** (§7) stay in the venue repositories, which are already private.
- **Deploy configuration and alerts** stay in `infra`, where they already are.
- **The canary** (§5) publishes recorded data under a synthetic feed code, so it names nobody.

Discovery from the ledger turns out to be the disclosure-safe design as well as the
low-maintenance one. A hand-written list has to name each venue somewhere; a discovered one names
none. That is a second argument for §3 that the first draft did not notice.

### 10.1 What is already public, and the limit of any obfuscation here

Deciding what this document may name requires knowing what is already disclosed. As of 2026-09-09:

- **The ledger is public.** All 306 mainnet-beta `Feed` accounts, with codes such as
  `edge-binance-usdsm-tob`, `kalshi-perps-tob` and `phoenix-tob`, were read from a public RPC
  endpoint with no credentials.
- **The Source ID registry is public and names venues.** `sources/spec.md`, in this repository,
  carries Hyperliquid (`1`), Phoenix (`2`), Kalshi (`3`), Setai (`4`, `5`) and Binance USD-Margined
  Futures (`6`). The Binance row names the operator of record and its regulatory status.
- **A venue-named design already sits in this public repository.**
  `docs/superpowers/specs/2026-08-19-kalshi-conformance-deploy-design.md`.

So obfuscating a venue in this document alone changes nothing while the registry beside it names
that venue. The registry is the disclosure; this document would only be a second copy of it.

**The codename mechanism exists and works, at the registry.** The Kalshi row records it: *"Registered
under the codename `Lashay` until the venue launched."* The retired codename is still visible today
as the `lashay1-feed` and `lashay2-feed` accounts in the testnet ledger, which is a fair illustration
of the Playbook's warning that codename retirement is sequenced rather than a search and replace.

**Decision: nothing is renamed.** Codenaming Hyperliquid or Binance was considered on 2026-09-09 and
rejected. The surfaces are not comparable, and neither argues for a rename.

**Hyperliquid is in a shipped public contract.** `malbeclabs/doublezero-edge-connect` is public, has
an install one-liner at `get.doublezero.xyz`, and its `PROTOCOL.md` is by its own statement the only
contract a consumer codes against. The venue name is a value in that contract and a subscription
filter key:

```json
{"type":"quote","venue":"Hyperliquid","source_name":"Hyperliquid","source_id":1,"symbol":"SOL"}
{"method":"subscribe","subscription":{"venue":"Hyperliquid","symbol":"SOL"}}
```

Renaming stops every existing subscription filter matching, with no error raised. That is a breaking
change to a public API, and a silent one. Beyond it: 62 references in that repository including
`src/products.rs`, `src/model.rs` and `src/metrics.rs`; 22 in `malbeclabs/lake`; 8 in
`malbeclabs/edge-multicast-ref`; and two in this repository's `GLOSSARY.md`, where Hyperliquid is
the worked example for both **Venue** and **Matching engine**.

**Binance is cheap to rename and pointless to rename.** It appears only in this repository (the ID 6
row, a revision string in `tools/conformance/engine/source_ids.json`, one out-of-scope mention in
`perp-stats/spec.md`) and in the ledger, as `edge-binance-usdsm-tob` across 31 metros with their
group codes. No public consumer carries it. But the disclosure already happened when those accounts
were registered and that row landed, so a rename now would cost a sequenced onchain migration to
close a door that is already open.

**The mechanism still works, at registration time.** A venue that has not announced gets a codename
when its Source ID is claimed, as Kalshi did with `Lashay`. It does not work retroactively. Phase B
of §8 adds the codename question to the template's Phase 1 step so a future venue gets it right
before anything is public.


## 11. The second pipeline: Edge Connect

There are two end-to-end paths from a venue to a consumer, and §1–§10 only cover the first.

```
venue feed ──► publisher ──► DZ network ──┬──► reference crates (feed capture recorders) ──► conformance vs the wire specs
                                          │
                                          └──► doublezero-edge-connect ──► normalized JSON WebSocket ──► conformance vs PROTOCOL.md
```

**Pipeline A** is what the rest of this document describes. It grades the binary multicast against
`edge-feed-spec`, and its checker is `dz-conformance`.

**Pipeline B** ends at `malbeclabs/doublezero-edge-connect`, the bridge most consumers actually run.
It decodes the binary multicast, drives the reference-data state machine, and re-serves everything as
one normalized JSON WebSocket. Its own README is explicit that this, not the multicast, is the
contract a consumer codes against:

> The binary multicast, the two-port split, and the manifest/precision handshake all stay on this
> side of the bridge. The **only** contract a consumer codes against is the WebSocket JSON, fully
> specified in **PROTOCOL.md**.

**Pipeline A passing does not imply pipeline B passing.** A conformant feed can still reach a
subscriber wrong, because everything between the two is unchecked: the decode, the refdata state
machine that supplies exponents, the venue and symbol tagging, the four latency timestamps, the
subscription filter matching. A `tick_size` misapplied on the bridge produces a well-formed JSON
quote with a wrong price, and pipeline A grades that feed clean because the bytes on the wire were
clean.

**These are different checkers against different specs.** `dz-conformance` reads datagrams against
the wire specs; nothing today reads `PROTOCOL.md` against a live WebSocket. Pipeline B needs its own
checker — a subscribing client that validates the JSON contract — and it should not be bolted into
`dz-conformance`, whose entire rule catalog is framed in datagrams, ports, sequence series and reset
counts. A second tool with its own rule catalog, sharing only the venue and metro labels so the two
results line up per feed.

The one place they must meet is reporting: a per-feed dashboard has to show the same feed graded on
both surfaces, or an operator cannot tell "the publisher is wrong" from "the bridge is wrong". That
shared axis is the feed code plus the metro, which §3 already derives from the ledger.

**Scope.** Pipeline B is out of scope for phases A through D and gets its own design. It is recorded
here because a plan that says "e2e coverage for the feeds" while covering only pipeline A would read
as finished when it is half done.

## 12. Recording capacity and isolation

**The pcaps come first.** They are the basis of trading research and an eventual product, they cannot
be regenerated, and a datagram dropped on the capture host is gone. Every other workload on a
recorder host, this document's validators included, is subordinate to lossless capture. Where the two
conflict, capture wins and the validator is what moves.

### 12.1 The risk is live today, not hypothetical

Read from `malbeclabs/infra`'s mainnet-beta inventory on 2026-09-09:

- **`multicast_recorders`**: `aws-cmh-mn-recorder1`, `aws-tyo-mn-recorder1`, `aws-was-mn-recorder1`,
  `aws-fra-mn-recorder1`, `aws-dub-mn-recorder1`. Baremetal: `chi-mn-recorder1`, `nyc-mn-recorder1`.
- **`dz_conformance`** resolves to `hyperliquid_feed_capture_mainnet_recorders` plus
  `kalshi_feed_capture`, and **every host in both is a recorder host.** `kalshi_feed_capture` is
  `aws-cmh-mn-recorder1`, `aws-was-mn-recorder1`, `aws-dub-mn-recorder1`; the inventory comment calls
  it "a SECOND capture service co-located on the shared multicast recorders".
- **`dz_recorder`** is "the same three hosts" again, and its group_vars notes that "every one of these
  hosts evicts continuously".

So the validators already run on the capture machines. This document's fleet mode would have
multiplied them: today a host runs two or three validator instances; a metro carries roughly nine
gradeable feeds, so §3 as first drafted was a three- to fourfold increase in validator instances on
exactly those hosts.

### 12.2 The failure mode is documented and already observed

`malbeclabs/infra`'s `nic_rx_tuning` role does not describe a theoretical risk. It describes one that
happened:

> the edge feed arrives GRE-encapsulated from a single tunnel endpoint, so hardware RSS hashes the
> outer IP to ONE queue/core and that core saturates (`time_squeeze`) -> NIC ring drops
> (`rx_no_buffer`). RPS spreads that queue's softirq across cores in software.

Three things follow. The loss path is a **single saturated core**, not aggregate host load, so a
host at 40% CPU can still be dropping. RPS, the mitigation, is **off by default** and opt-in per
host. And a validator scheduled onto the core servicing that queue's softirq competes directly with
the thing that must not lose packets.

The `dz-conformance` systemd unit as deployed carries **no resource limits at all** — no `CPUQuota`,
no `CPUAffinity`, no `MemoryMax`, no IO weight. Each feed is a separate templated instance, so the
count scales with coverage and nothing bounds the total.

### 12.3 Rules

1. **Validators do not share a host with a pcap recorder by default.** Fleet mode's subscriber hosts
   are separate instances that join the same groups. A subscriber is cheap; a lost capture is not.
2. **Where co-location is unavoidable, it is bounded in the unit file, not by convention.** Pin
   validators with `CPUAffinity` off the cores servicing the capture NIC's RX softirq, cap them with
   `CPUQuota`, bound them with `MemoryMax` so a leak cannot OOM the recorder, and set `IOWeight` below
   the recorder so pcap writes and S3 uploads win contention. An unbounded `Type=simple` unit beside
   the recorder is the current state and is not good enough for a fleet-sized instance count.
3. **Capture loss is measured and alerted independently of conformance**, because a validator
   reporting a clean feed while the recorder drops frames is the worst reading available. The
   counters are per-interface `rx_no_buffer` and `rx_missed_errors`, `/proc/net/softnet_stat`'s
   `time_squeeze`, UDP receive-buffer drops, and the recorder's own drop and gap counters. Alert on
   the recorder's loss, not on the validator's.
4. **Measure before adding a single instance.** Phase A's rollout is gated on a per-host baseline of
   those counters, taken before and after, on one metro. A rollout that cannot show capture loss
   unchanged does not proceed.
5. **Enable RPS on the hosts with the GRE single-queue problem** before adding validator load, not
   after. It is opt-in today, so a host that needs it and has not been given it is one saturated core
   away from dropping.
6. **Size per metro, not uniformly.** The inventory already differentiates: `was` is on the same
   `c6i.2xlarge` as `cmh` deliberately so the two differ in location and not in spec, and it also
   records the Solana shreds groups, while `dub` is a smaller box. Fleet mode's per-metro feed count
   varies the same way and the instance sizing has to follow it.

### 12.4 Open questions for the hardware owner

These need answers from whoever owns the fleet; the design does not assume them.

- **What produces the pcap timestamps today, and to what accuracy?** Kernel `SO_TIMESTAMPING`, or NIC
  hardware timestamping? This matters for both the research value of the captures and §6's latency
  histogram, and the answer likely differs between the AWS hosts and the `chi`/`nyc` baremetal ones.
  If hardware timestamping is available on the baremetal hosts and not on the AWS ones, the captures
  are not comparable across metros and that limitation belongs in the data, not in folklore.
- **Is capture loss currently zero, and how do we know?** Rule 3 assumes counters exist to read. If
  the recorder does not already expose a drop and gap count, adding one is a prerequisite for phase A,
  not a follow-up.
- **What headroom do the existing hosts actually have?** Rule 1's default is separate hosts precisely
  because nobody has measured this. A measurement showing real headroom can relax it per metro.
- **There are open issues on exactly this.** The inventory cites #1980 ("recorder-role membership
  stays decoupled from feed-capture/conformance") and a #1984 cutover, with `tyo` still co-hosting
  tenants until then. This section should be reconciled with that work rather than run beside it.
