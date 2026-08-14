# Glossary

Canonical vocabulary for the DoubleZero Edge market-data repos: `edge-feed-spec`, `edge-multicast-ref`, `doublezero-edge-connect`, `hyperliquid`, `kalshi`.

Use these words with these meanings in specs, docs, plans, comments, identifiers, CLI flags, config keys, metric names, and log fields. If a word you want is listed under [Banned](#banned-words), use the replacement.

## Precedence

1. The public docs glossary (`docs/glossary.md` in the docs repo) wins. It ships to customers in six languages.
2. This file wins over any repo-local definition.
3. A repo may add terms it alone needs. It may not redefine a term listed here.

Open questions are in [Unresolved](#unresolved). Do not invent an answer to one.

## Identity

| Term | Definition | Not |
|---|---|---|
| **Venue** | The external exchange or market operator whose activity a message describes (Hyperliquid, Kalshi, Phoenix). | A publisher, a host, a product line |
| **Source ID** | The `u16` wire field carrying the venue's number from the Source ID Registry. Present on every price and event message, and on `InstrumentDefinition` as of Schema Version 3. | A publisher instance, a host, a transport, a timestamp |
| **Source ID Registry** | The assignment table in `edge-feed-spec/sources/spec.md`. Sole authority. | Any copy of it |
| **Registry mirror** | A repo-local copy of the Source ID Registry, e.g. `doublezero-edge-connect/src/ingest/sources.rs`. Derived, never authoritative. | The registry |
| **Instrument** | One tradable entity, keyed by `Instrument ID` (`u32`), unique within a channel. | A symbol, a market, an asset |
| **Symbol** | The `char[64]` human-readable name in `InstrumentDefinition`. Display and filtering only. | A state key |
| **Operator** | The person or organization running a publisher. | The publisher process |

Use `venue` for the external exchange. `exchange` is a synonym; prefer `venue`.

## Transport

| Term | Definition | Not |
|---|---|---|
| **Multicast group** | The IP group datagrams are sent to. Say "group" only where multicast is unambiguous. | A channel, a feed |
| **Port role** | One of exactly `mktdata`, `refdata`, `snapshot`. Use these tokens verbatim. | A channel |
| **Channel** | A logical shard of instruments with its own `Channel ID` (`u8`), sequence series, reset count, and snapshot cycle. | A port role, a `chan`/`mpsc`, a venue's pub/sub topic |
| **Datagram** | The contents of one UDP packet: a 24-byte datagram header plus N application messages. | A message |
| **Message** | One application record inside a datagram, with its own 4-byte header. | A datagram |
| **Path** | One of several redundant publishers or transports carrying the same data, raced against each other. | A channel, a network route |

Prefer `datagram` over `frame` or `packet` for our own traffic. A frame is a layer-2 construct carrying Ethernet and IP headers; what we define and version is the UDP payload.

## Protocol and roles

| Term | Definition | Not |
|---|---|---|
| **Feed** | Both the wire protocol defined by a spec in `edge-feed-spec` (Top-of-Book, Midpoint, Market-by-Order, Market-by-Price, Order-Intent, Perp-Stats) and the live traffic carrying it on one channel. Say "feed spec" or "live feed" only where the difference matters. | A multicast group, an upstream vendor |
| **Publisher** | The process that transmits datagrams using a multicast group. Matches the public glossary's multicast sender role. | A deploy unit, a struct, one redundant path |
| **Subscriber** | A process that joins a multicast group, strips headers, and decodes datagrams. | A trading client |
| **Era** | The span between two `Reset Count` values. | An epoch |
| **Snapshot** | A moment-in-time copy of the order book, published on the `snapshot` port. | A blockchain state dump |
| **Delta** | An incremental book-mutating message on `mktdata`. | |

Two pairs, one per layer, and both are correct. From the network's point of view a publisher is a **transmitter** (or **sender**) and a subscriber is a **receiver**: use those for the act of moving datagrams. For the roles themselves use **publisher** and **subscriber**, matching the public glossary.

Neither half of either pair is redefined here. `receiver` in particular keeps its plain meaning — anything that receives — so every subscriber is a receiver, and the specs' normative "Publishers SHOULD … receivers MUST …" is correct as written.

## Components

| Term | Definition | Not |
|---|---|---|
| **Decoder** | The pure function turning wire bytes into a struct. | A binary |
| **Parser** | A binary that subscribes, decodes, and republishes records downstream. | The decoder inside it |
| **Book-builder** | A binary consuming parser output that maintains book state and optionally persists it. | A bot |
| **Bot** | A real automated trading client. We ship none. | Any component in these repos |

## Banned words

| Banned | Use instead | Note |
|---|---|---|
| `arm` (redundant publisher) | `path` | |
| `arm` (`match`/`select!` branch) | `branch` | `arm` is acceptable inside code comments only; never in specs, docs, or plans |
| `arm` / `disarm` | — | Allowed only for the Order-Intent dead-man switch |
| `bot` (our components) | `book-builder` | |
| `lane` | `feed` or `path` | |
| `feed` (upstream vendor) | `upstream <vendor>` | |
| `stream` (our live traffic) | `feed` | A live feed is a feed; the extra word bought nothing |
| `frame` (our own traffic) | `datagram` | |
| `channel` (port role) | `port role` | |
| `channel` (venue pub/sub topic) | `venue topic` | |
| `source` (unqualified) | a qualified form | Never bare — see the note below |
| `epoch` | `era` | |
| `sibling feed` | `feed` | All feeds are siblings; the word adds nothing |
| `tee` | `fan-out` | |
| `sweep` | Name the operation | Currently means three unrelated things |
| `normalization` | `decode` | Reserve `normalization` for cross-feed latency metrics |
| `venue` (Rust trait over product lines) | `product line` or `adapter` | |
| `roster`, `active set` | `published set` | |

**`source` always takes a qualifier.** The word is banned bare — in specs, docs, plans, identifiers, CLI flags, config keys, metric names, and log fields. Write the qualified form and the sense is unambiguous:

| Qualified form | Means |
|---|---|
| `source_id` | Venue identity, as assigned by the Source ID Registry |
| `source_ts` / `source_timestamp_ns` | The venue's own clock |
| `source IP address` | The layer-3 sender of a datagram |
| `source publisher` | Which publisher a subscriber received given data from |
| `upstream source` | The external vendor or venue API a publisher reads |

These mean unrelated things and several share a prefix, which is why the qualifier is mandatory rather than stylistic. Where a bare use means none of the above, replace the word outright: an ingest transport choice is a `transport`, an input is an `input`, and a metrics sample origin needs its type renamed.

## Rename worklist

Ordered by blast radius. `file:line` citations are starting points, not the full set.

### doublezero-edge-connect

| Current | Replacement | Where |
|---|---|---|
| `Arm`, `OTHER_ARM`, `arm_race.rs`, `dz_arm_*` metrics | `Path`, `path_race.rs`, `dz_path_*` | `src/ingest/authority.rs`, `arm_race.rs`, `arbiter.rs`, `docs/metrics.md` |
| `enum Publisher { Edge, PublicWs }` | `enum Transport` | `src/ingest/arbiter.rs:113` |
| `publisher` metric label (two cardinalities: base port vs `edge`/`public`) | Split into `publisher` and `transport` | `docs/metrics.md:44,113` |
| "input source" / "source transport" / "feeder" | `input` | `docs/input-sources.md`, `CLAUDE.md` |
| "the bridge" vs "the reference producer" | Pick one name for the process | `README.md` vs `PROTOCOL.md:7` |
| "source registry" | `registry mirror` | `src/ingest/sources.rs:1` |

### kalshi

| Current | Replacement | Where |
|---|---|---|
| `publisher::Channel { MarketData, RefData, Snapshot }` | `PortRole` | `crates/kalshi-publisher/src/publisher/mod.rs:30` |
| `SharedKeyFixArm` | `SharedKeyFixPath` | `crates/kalshi-publisher/examples/events_fix_md_probe.rs:758` |
| `FeedSource` | `FeedTransport` | `crates/kalshi-publisher/src/config.rs:972` |
| `SourceKind` | `CaptureInput` | `crates/kalshi-capture/src/config.rs:39` |
| `metrics::Source` | `SampleOrigin` | `crates/kalshi-publisher/src/metrics.rs:739` |
| `TobVenue`, `MbpVenue`, `PerpsFixVenue` | `*ProductLine` or `*Adapter` | `crates/kalshi-publisher/src/publisher/` |
| "lane" | `feed` | `config.rs:28,93,158` |
| "sweep" (three senses) | Name each | `perp_stats.rs`, `publisher/mbp/feed.rs`, `tools/catalog-sweep/` |

### hyperliquid

| Current | Replacement | Where |
|---|---|---|
| "feed source" (external vendors) | `upstream <vendor>` | `docs/superpowers/specs/*-feed-source-design.md` |
| `emitter` (JSONL telemetry logger) | `event logger` | `research/gossip/decoder/internal/gossipcli/passivepeer/observability.go:36` |
| `snapshot` (consensus bootstrap blob) | `state snapshot` | `research/gossip/decoder/` |
| `app/publisher/README.md` opening section | Delete | Inherited third-party text describing different software |

### edge-multicast-ref

| Current | Replacement | Where |
|---|---|---|
| `topofbook-bot`, `marketbyorder-bot`, `marketbyprice-bot` | `*-book-builder` | `go/`, plus every README and design doc |
| `PacketMeta.Channel` (port role label) | `PortRole` | `go/topofbook-parser/tob/parser.go:17` |
| `engine` (book-building subsystem) | `book engine` | `go/marketbyprice-bot/` — disambiguates from SQL `ENGINE =` |
| "normalization" (latency metrics) | Keep, it is the reserved sense | `docs/superpowers/specs/2026-06-06-*` |

### edge-feed-spec

| Current | Replacement | Where |
|---|---|---|
| "frame", "Frame Header" | `datagram`, `Datagram Header` | All six specs, `VERSIONING.md`, `README.md`, `tools/conformance/wire/header.go` — 174 uses. Wire layout is unchanged, so this is a doc rename, but `Frame Header` is a named section in every spec: schedule it with a spec revision |
| Bare `source` | Qualify it | ~12 sites: `top-of-book/spec.md:209` ("originating source", "a single source"), `order-intent/spec.md:183,377,394,402,404` ("live edge of the source", "source liveness"), `sources/spec.md:18,26` ("Assigned Sources" → "Assigned Source IDs") |
| `Bid Source Count` / `Ask Source Count` | `Bid Venue Count` / `Ask Venue Count` | `top-of-book/spec.md:217-218` — "source" meant contributing venues, colliding with `Source ID`. Renamed on branch `fix/tob-venue-count`; the wire layout is unchanged, so it is editorial (`PATCH`). Downstream field names still to follow: `kalshi/app/publisher/crates/dz-tob-protocol/src/quote.rs:26-27`, `edge-multicast-ref/go/topofbook-parser/sink_csv.go:16` |
| "epoch" | `era` | `reference-data/spec.md:93` |
| "feed channel" | `channel` | `tools/conformance/README.md:3` |
| "publisher operator" | `operator` | `reference-data/spec.md:189` |
| "venue-native asset id" | `venue-native instrument id` | `order-intent/spec.md:193` |
| Per-spec sibling enumerations | Delete | Every `Relationship to Sibling Feeds` section |

## Unresolved

These need a decision before the affected text can be corrected. Several are protocol defects, not wording.

1. **Source ID's definition is too narrow.** `sources/spec.md:5` says a Source ID identifies "the venue whose order book a price message was derived from" and that "every price message on every feed carries exactly one." Order-Intent carries pre-consensus mempool events with no book and sometimes no price; Perp-Stats relays REST-polled venue state; and as of #29 `InstrumentDefinition` carries a Source ID and is not a price message at all. The sentence describes none of these. Proposed: "identifies the venue whose activity the message describes." Registry PATCH release.

2. **MBO and MBP contradict each other on channel scope.** `market-by-order/spec.md:484` forbids interleaving snapshot groups "on the `snapshot` port", unqualified. `market-by-price/spec.md:522` scopes the identical rule to the channel and states that two channels alternating on one port are each conformant. Read literally, MBO bans a deployment MBP permits. Untested: the conformance tool handles one channel per port only.

3. **Instrument ID scoping is unstated for half the feeds.** MBO, MBP, and Order-Intent define the unique key as `(Channel ID, Instrument ID)`. Top-of-Book, Midpoint, and Perp-Stats carry the same `Channel ID` field and say nothing, leaving subscribers no text on whether Instrument ID is globally unique.

4. **`Channel ID` width disagrees across the boundary.** It is `u8` on the wire in every spec and in both publishers; `doublezero-edge-connect/PROTOCOL.md:77` exposes `channel` as `uint32` on its WebSocket output. Intentional widening or drift?

5. **Asset Class `5` (Perpetual Future) is missing from two specs** that share the 130-byte `InstrumentDefinition`. `market-by-order/spec.md:193` lists `0`–`4` in its Asset Class table; `order-intent/spec.md:161` lists `0`–`4` inline. Top-of-Book and Market-by-Price list `5`, and `perp-stats/spec.md:95` requires every definition on that feed to carry it. Wire-safe, documentation is wrong.

6. **Source ID 3 is `Lashay` with a blank `Kind`**, though `sources/spec.md:26` makes `Kind` required. `doublezero-edge-connect` treats `LASHAY` as a legacy input-only alias for Kalshi. Decide the canonical name and fill in `Kind`.

7. **`tools/conformance/engine/source_ids.json` is stale.** Its `spec_revision` reads "assigned IDs: 1=Hyperliquid, 2=Phoenix" and was last read 2026-06-15, omitting ID 3, which shipped in the registry's first stable release. Harmless in practice — the range check accepts all of `[1,1023]`. Separately, `tools/conformance/README.md:3` and its `--feed` table advertise three feeds while the tool implements four of six.

8. **Top-of-Book and Market-by-Price disagree on the "unavailable" sentinel** for the same concept. `market-by-price/spec.md:337` gives `Order Count` the value `0xFFFF` for "the venue does not expose it, or the count exceeds `0xFFFE`", and states that `0` is a real value. Top-of-Book's venue count (`top-of-book/spec.md:217-218`) uses `0` for unavailable, so it cannot express a genuine zero and saturates silently. Harmonizing would redefine a field and therefore needs a MAJOR bump, so it must ride the next Top-of-Book major rather than a rename.
