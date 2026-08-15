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
| **Venue** | The external exchange or market operator (Hyperliquid, Kalshi, Phoenix). One venue may run several matching engines and therefore hold several Source IDs. | A matching engine, a publisher, a host, a product line |
| **Matching engine** | One set of order-matching rules at a venue. Distinct microstructure means a distinct engine: Hyperliquid native perps, each HIP-3 builder DEX, HIP-4 prediction markets, Kalshi events, and Kalshi perps are five engines across two venues. | A venue, a channel, a feed |
| **Source ID** | The `u16` wire field naming the matching engine whose activity a message describes, assigned by the Source ID Registry. Present on every price and event message, and on `InstrumentDefinition` as of Schema Version 3. | A venue, a channel, a publisher instance, a host, a transport, a timestamp |
| **Source ID Registry** | The assignment table in `edge-feed-spec/sources/spec.md`. Sole authority. | Any copy of it |
| **Registry mirror** | Any copy of the Source ID Registry outside `sources/spec.md` — a hardcoded table, or a config artifact a service loads at runtime. Derived, never authoritative. | The registry |
| **Instrument** | One tradable entity, keyed by `Instrument ID` (`u32`), unique within a channel. | A symbol, a market, an asset |
| **Symbol** | The `char[64]` human-readable name in `InstrumentDefinition`. Display and filtering only. | A state key |
| **Operator** | The person or organization running a publisher. | The publisher process |

Use `venue` for the external exchange. `exchange` is a synonym; prefer `venue`.

**A Source ID identifies a matching engine, not a venue.** Split the ID wherever the microstructure or the logic governing how orders match differs, because that is the boundary a subscriber has to reason about: a venue name tells it nothing about how a book behaves. One venue therefore holds as many Source IDs as it runs engines, and a new engine at a known venue is a new ID rather than a reuse of the venue's existing one. Registry IDs are stable and never renumbered, so this only ever adds: an ID already assigned keeps meaning whichever engine has been publishing under it. Venues split their own engines on their own schedule, so treat the set as open and track it against each venue's roadmap rather than assuming today's list is final.

**Venues are sometimes carried under a codename before they launch.** These repos are public, so a venue that has not yet announced its DoubleZero feed may appear under a placeholder name in the Source ID Registry, in multicast group codes, and in operator-facing config. Retire the codename once the venue is public, registry first. Retirement is a sequenced change rather than a search-and-replace: until the DoubleZero ledger re-registers the groups, code paths that resolve ledger strings MUST keep accepting the old name on input, and a `code` that stops matching its live group fails silently rather than loudly.

## Transport

| Term | Definition | Not |
|---|---|---|
| **Multicast group** | The IP group datagrams are sent to. Say "group" only where multicast is unambiguous. | A channel, a feed |
| **Port role** | One of exactly `mktdata`, `refdata`, `snapshot`. Use these tokens verbatim. | A channel |
| **Channel** | A logical shard of the instrument set, named by `Channel ID` (`u8`) in the datagram header. Two redundant paths may carry the same channel. | A port role, a `chan`/`mpsc`, a venue's pub/sub topic, a sequence series |
| **Channel instance** | One path's view of one channel, keyed `(source IP address, Channel ID, destination port)`. The unit that owns a sequence series, a `Reset Count`, and a snapshot cycle. | A channel |
| **Datagram** | The contents of one UDP packet: a 24-byte datagram header plus N application messages. | A message |
| **Message** | One application record inside a datagram, with its own 4-byte header. | A datagram |

Prefer `datagram` over `frame` or `packet` for our own traffic. A frame is a layer-2 construct carrying Ethernet and IP headers; what we define and version is the UDP payload.

**Sequencing keys on the channel instance, never the channel.** `Sequence Number`, `Reset Count`, and the snapshot cycle belong to one path's view of a channel. Redundant paths carrying the same channel run as separate processes on separate hosts and cannot share a counter, so a subscriber binding more than one sees an independent series per instance and MUST key gap detection and recovery state on `(source IP address, Channel ID, destination port)`. Each host publishes on a distinct destination port by deployment convention, defined out of band. Arbitrating between instances of the same channel is a separate concern from sequencing within one.

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
| `arm`, `disarm` (every sense) | see the note below | No exceptions |
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

**`arm` is banned outright**, in every sense and every place: specs, docs, plans, identifiers, CLI flags, config keys, metric names, log fields, and code comments alike. There is no surviving exception. Replace it by sense — a redundant publisher is a `path`, a `match` or `select!` branch is a `branch`, and the Order-Intent dead-man switch is **set** and **cleared** rather than armed and disarmed. That last one costs nothing on the wire: the switch is carried by `ScheduleCancel`'s `Trigger Time`, where a non-zero timestamp sets it and `0` clears it, so `arm` was only ever prose.

`path` is deliberately not defined in this glossary. It is used in its ordinary English sense — one of several redundant routes carrying the same data — and needs no house meaning.

**`source` always takes a qualifier.** The word is banned bare — in specs, docs, plans, identifiers, CLI flags, config keys, metric names, and log fields. Write the qualified form and the sense is unambiguous:

| Qualified form | Means |
|---|---|
| `source_id` | Matching engine identity, as assigned by the Source ID Registry |
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
| Source ID → short name mapping hardcoded in Rust | A `sources` block in the feed registry document | `src/ingest/sources.rs:22-54` (`source_name`, `source_id_of`). The block is generated from `edge-feed-spec/sources/spec.md`, which stays the source of truth, so adding a venue becomes a registry MINOR plus a republish rather than a code change and a release. The delivery pattern already exists and needs no new machinery: URL, then bind-mounted file, then compiled-in fallback, fetched once at startup under a 10s bound with no hot reload (`src/ingest/registry.rs`, `docs/self-hosting.md`). Three things to preserve. The compiled-in table stays as the built-in fallback exactly as `registry.json` is, since a URL failure degrades rather than dying. The unregistered-ID fallback stays (`sources.rs:80-95`, `MAX_UNREGISTERED_SOURCES`): the wire Source ID is authoritative, and an ID absent from the document still needs a distinct synthesized label rather than collapsing, because the arbiter keys dedupe on `(venue, symbol)`. And `registry.rs:816` currently validates the document's `venue` against the compiled-in table — that dependency inverts, since the document would then carry the mapping rather than be checked against code. Adding the block does not bump `SUPPORTED_VERSION`: the module sets no `deny_unknown_fields` and warns-and-ignores unknown keys (`registry.rs:261,281`) |
| `registry.json` notes claim adding a field is a version bump | Match the implemented policy | The built-in document's own notes say "Adding a field to this schema is a version bump: an older binary must reject a document it does not fully understand rather than silently ignore half of it." The code does the opposite and says so: `registry.rs:35` and `:74` state additive changes never bump `SUPPORTED_VERSION`, and `:261` records that `deny_unknown_fields` is deliberately absent throughout, with `warn_unknown` at `:281` warning and continuing. One of the two has to give; the code's policy is the one every caller depends on |
| Codename for Source ID 3: the input-only alias, the two feed row codes, and every doc and test mention | The registry name | `src/ingest/sources.rs:39`, `src/ingest/feeds.rs:392`, `CLAUDE.md`, `CHANGELOG.md`, `docs/metrics.md`, `docs/input-sources.md`, `tests/`. See the note on codenames below. One technical gate: the `code` values in `feeds.rs` are transcribed from what the DoubleZero ledger registers today, and `CHANGELOG.md:108` requires the rows change in the same commit that lands the ledger rename, never before it. The input-only alias exists to keep those ledger strings resolving, so it drops with them. Doc, test, and prose mentions are unblocked |
| `channel` as `u32` / `uint32` | `u8` / `uint8` | `processor.rs:499` widens the decoded `channel_id` with `as u32` for no stated reason; it carries through `model.rs:259,300`, `model.rs:363` (`channel()`), `sinks/ws.rs:130` (`SubFilter.channel`), and `PROTOCOL.md:77,260,392`. Every codec decodes `u8` and the glossary pins `Channel ID` at `u8`. JSON numbers carry no width, so emitted messages are unchanged; the one visible effect is that `{"channel": 300}` becomes a deserialize error the client sees rather than a filter that parses and then matches nothing forever |

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
| A channel's `id=` derives both `channel_id` and the wire `source_id` | Take `source_id` from the registry per channel, independent of `channel_id` | `app/publisher/README.md:212-218,239,275-283`, `app/publisher/server/src/channels.rs`. `id=` is documented as setting both, so a second channel of a spec "shifts that channel's wire `source_id` off `1`" — the README's own builder-DEX example (`spec=tob,…,id=2,dexes=xyz`) therefore stamps `source_id=2`, which the registry assigns to Phoenix. A channel is a shard of the instrument set and a Source ID is the matching engine; they are independent axes, and sharding a feed must not renumber the thing that identifies what matched. Whether any deployed `--channel` config currently trips this needs checking against the private infra repo. Precondition for giving each builder DEX its own Source ID |
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
| Four independent restatements of the 130-byte `InstrumentDefinition` and its `Asset Class` / `Market Model` enumerations | One canonical definition every feed spec references | New supplement following the `sources/spec.md` pattern: no `Magic`, no `Schema Version`, versioned independently. Today the layout is restated in full at `top-of-book/spec.md:125`, `market-by-order/spec.md:163`, `market-by-price/spec.md:184`, and `order-intent/spec.md:149`, and only `perp-stats/spec.md:95` references instead of restating. The `Asset Class` table is mirrored five times and two copies are already stale: `market-by-order/spec.md:189` and `order-intent/spec.md:161` list `0`–`4` and omit `5` (Perpetual Future), which shipped as a MINOR addition per `VERSIONING.md:60` and which `perp-stats/spec.md:95` requires on every definition it carries. Wire-safe today only because every spec makes decoders map unknown `u8` values to `0`. Feed specs keep their feed-specific constraints (perp-stats' "every definition carries `Asset Class = 5`") and drop the mirrored tables. Replacing a table with a reference is editorial, so PATCH on Top-of-Book, Market-by-Price, and Perp-Stats; on Market-by-Order and Order-Intent the reference newly admits value `5`, so MINOR. Midpoint carries its own 64-byte variant and rides its revamp |
| Bare `source` | Qualify it | ~12 sites: `top-of-book/spec.md:209` ("originating source", "a single source"), `order-intent/spec.md:183,377,394,402,404` ("live edge of the source", "source liveness"), `sources/spec.md:18,26` ("Assigned Sources" → "Assigned Source IDs") |
| "identifies the venue whose order book a price message was derived from. Every price message on every feed carries exactly one Source ID." | "identifies the matching engine whose activity the message describes. Every price message, event message, and `InstrumentDefinition` carries exactly one Source ID." | `sources/spec.md:5` — the registry's definition is narrower than the field's actual scope and than this glossary's `Source ID` entry, on two axes. On message kind: Order-Intent carries pre-consensus mempool events with no book, Perp-Stats relays REST-polled state, and as of #29 `InstrumentDefinition` carries a Source ID and is not a price message. All three still identify an engine — an order intent targets one, and funding and mark describe a product that matches on one. On granularity: a Source ID names a matching engine rather than a venue, so the registry's prose, its `Assigned Sources` table, and its "Adding a New Source" process all need the engine framing, and the table wants an operator column so several engines at one venue are legible. Registry PATCH for the prose; the table restructure rides the short-name column change below |
| "MUST NOT interleave two snapshot groups for different instruments on the `snapshot` port" | Market-by-Price's channel-scoped text and its "Scoped to the channel, not to the port" rationale paragraph, adopted verbatim | `market-by-order/spec.md:484`, taking `market-by-price/spec.md:522-524`. MBO's prose is the only source that reads the rule as port-wide: MBP scopes the identical rule to the channel, MBO's own instrument identity is `(channel_id, instrument_id)` at `spec.md:109`, and the conformance checker has always keyed open snapshot groups by channel (`tools/conformance/engine/gate.go:194,878`), so a two-channel `snapshot` port passes today. Editorial correction of a sentence that was never implemented as written, so MBO PATCH release, not a Schema Version bump |
| No statement of instrument identity; `Instrument ID` glossed as "Unique numeric ID for this instrument" | Market-by-Order's two-paragraph *Instrument Identity* section, adopted verbatim | `top-of-book/spec.md:132`, `midpoint/spec.md:136`, `perp-stats/spec.md:128`, taking `market-by-order/spec.md:109-111`. All six feeds carry `Channel ID` in the datagram header as "Logical channel for instrument sharding", but only MBO, MBP, and Order-Intent state that the key is `(channel_id, instrument_id)`. "Unique numeric ID" invites a flat `u32` map that silently merges two instruments the first time an operator shards. Clarifies an ambiguity rather than changing required behavior, so PATCH on each. Midpoint's cross-feed `SHOULD` at `:136` also needs to match on the tuple rather than the `u32`, but that rides the wholesale Midpoint revisit rather than this pass |
| `Sequence Number` glossed "per channel" (`top-of-book/spec.md:76`, `midpoint/spec.md:78`) or "per channel per port" (`market-by-order/spec.md:85`, `market-by-price/spec.md:87`) | Order-Intent's per-host gloss, adopted verbatim | Taking `order-intent/spec.md:83`, the only spec that states it: the series is **per publisher host, per channel, per port**, and a subscriber binding several hosts on one group MUST key them on transport origin. Four documents, not five — `perp-stats/spec.md:24` inherits Top-of-Book's header by reference. `market-by-order/spec.md:115` and `market-by-price/spec.md:129` need the same qualification: "each channel is an independent state machine with its own `Reset Count`, `Sequence Number` series per port, `Manifest Seq`, and snapshot cycle" attributes to the channel what belongs to the channel instance. Clarifies what was always true (a subscriber tracking one series across two hosts sees constant false gaps), so PATCH on each |
| `Bid Source Count` / `Ask Source Count` | `Bid Venue Count` / `Ask Venue Count` | `top-of-book/spec.md:217-218` — "source" here means contributing venues, colliding with `Source ID` nine rows above. The gloss reads "Orders/sources at best bid", which names two different quantities and matches neither. Wire layout is unchanged, so the rename is editorial. Downstream field names follow: `tools/conformance` (rule ID `TOB.QUOTE.SOURCE_COUNT` is a Prometheus label value), `kalshi/app/publisher/crates/dz-tob-protocol/src/quote.rs:26-27`, `edge-multicast-ref/go/topofbook-parser/sink_csv.go:16` |
| "arm"/"disarm" (dead-man switch) | "set"/"clear" | `order-intent/spec.md:5,120,342,356,360` — prose only; `Trigger Time` already encodes it as non-zero vs `0`, so this is editorial |
| Source ID 3 carries a codename and a blank `Kind` | `Kalshi`, with a `Kind` | `sources/spec.md:26`. The venue launched the week of 2026-08-11, so the codename may now be retired; see the note on codenames below. `sources/spec.md` makes `Kind` required and classifies edits to `Name`/`Kind`/`Notes` as PATCH. `Kind` itself waits on the per-matching-engine question in [Unresolved](#unresolved) |
| Registry table has no machine-readable code column | Add a short name column carrying the uppercase full name | `sources/spec.md:22-26`. The form is the uppercase full name — `HYPERLIQUID`, `PHOENIX`, `KALSHI` — not an abbreviation. That is already what reaches consumers as `venue`/`source` on the WebSocket, as every `venue=` metric label value, and as the `SOURCE:SYMBOL` product identifier composed from them, so the registry documents what exists rather than changing it and no dashboard or alert matching a label value breaks. Today it lives only in the mirror at `doublezero-edge-connect/src/ingest/sources.rs:24-26`, which invents it; the registry is the source of truth and must carry it. Because a Source ID names a matching engine, the column names the engine rather than the operator: the three assigned names stand, and each new engine row takes its own uppercase name. Additive to the assignment set, so registry MINOR |
| "epoch" | `era` | `reference-data/spec.md:93` |
| "feed channel" | `channel` | `tools/conformance/README.md:3` |
| README advertises three feeds | Four | `tools/conformance/README.md:3` and its `--feed` table still say TOB, Midpoint, or MBO. Market-by-Price support landed in #22, so the tool implements four of the six specs |
| "publisher operator" | `operator` | `reference-data/spec.md:189` |
| "venue-native asset id" | `venue-native instrument id` | `order-intent/spec.md:193` |
| Per-spec sibling enumerations | Delete | Every `Relationship to Sibling Feeds` section |

## Unresolved

These need a decision before the affected text can be corrected. Several are protocol defects, not wording.

1. **Top-of-Book and Market-by-Price disagree on the "unavailable" sentinel** for the same concept. `market-by-price/spec.md:337` gives `Order Count` the value `0xFFFF` for "the venue does not expose it, or the count exceeds `0xFFFE`", and states that `0` is a real value. Top-of-Book's venue count (`top-of-book/spec.md:217-218`) uses `0` for unavailable, so it cannot express a genuine zero and saturates silently. Harmonizing would redefine a field and therefore needs a MAJOR bump, so it must ride the next Top-of-Book major rather than a rename.
