# Glossary

Canonical vocabulary for DoubleZero Edge market data.

Use these words with these meanings in specs, docs, plans, comments, identifiers, CLI flags, config keys, metric names, and log fields. If a word you want is listed under [Banned](#banned-words), use the replacement.

## Precedence

This file is the authority. A definition here overrides any local one, wherever it appears. A project may add terms it alone needs; it may not redefine a term listed here.

## Identity

| Term | Definition | Not |
|---|---|---|
| **Venue** | The external exchange or market operator (Hyperliquid, Kalshi, Phoenix). One venue may run several matching engines and therefore hold several Source IDs. | A matching engine, a publisher, a host, a product line |
| **Matching engine** | One set of order-matching rules at a venue. Distinct microstructure means a distinct engine: Hyperliquid native perps, each HIP-3 builder DEX, HIP-4 prediction markets, Kalshi events, and Kalshi perps are five engines across two venues. | A venue, a channel, a feed |
| **Source ID** | The `u16` wire field naming the matching engine whose activity a message describes, assigned by the Source ID Registry. Present on every price and event message, and on `InstrumentDefinition` as of Schema Version 3. | A venue, a channel, a publisher instance, a host, a transport, a timestamp |
| **Source ID Registry** | The assignment table in `sources/spec.md`. Sole authority. | Any copy of it |
| **Registry mirror** | Any copy of the Source ID Registry outside `sources/spec.md` — a hardcoded table, or a config artifact a service loads at runtime. Derived, never authoritative. | The registry |
| **Instrument** | One tradable entity, keyed by `Instrument ID` (`u32`), unique within a channel. | A symbol, a market, an asset |
| **Symbol** | The `char[64]` human-readable name in `InstrumentDefinition`. Display and filtering only. | A state key |
| **Operator** | The person or organization running a publisher. | The publisher process |

Use `venue` for the external exchange. `exchange` is a synonym; prefer `venue`.

**A Source ID identifies a matching engine, not a venue.** Split the ID wherever the microstructure or the logic governing how orders match differs, because that is the boundary a subscriber has to reason about: a venue name tells it nothing about how a book behaves. One venue therefore holds as many Source IDs as it runs engines, and a new engine at a known venue is a new ID rather than a reuse of the venue's existing one. Registry IDs are stable and never renumbered, so this only ever adds: an ID already assigned keeps meaning whichever engine has been publishing under it. Venues split their own engines on their own schedule, so treat the set as open and track it against each venue's roadmap rather than assuming today's list is final.

**Venues are sometimes carried under a codename before they launch.** The Source ID Registry is public, so a venue that has not yet announced its DoubleZero feed may appear under a placeholder name there, in multicast group codes, and in operator-facing config. Retire the codename once the venue is public, registry first. Retirement is a sequenced change rather than a search-and-replace: until the DoubleZero ledger re-registers the groups, code paths that resolve ledger strings MUST keep accepting the old name on input, and a `code` that stops matching its live group fails silently rather than loudly.

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
| **Feed** | Both the wire protocol defined by a feed spec (Top-of-Book, Midpoint, Market-by-Order, Market-by-Price, Order-Intent, Perp-Stats) and the live traffic carrying it on one channel. Say "feed spec" or "live feed" only where the difference matters. | A multicast group, an upstream vendor |
| **Publisher** | The process that transmits datagrams using a multicast group. The multicast sender role. | A deploy unit, a struct, one redundant path |
| **Subscriber** | A process that joins a multicast group, strips headers, and decodes datagrams. | A trading client |
| **Era** | The span between two `Reset Count` values. | An epoch |
| **Snapshot** | A moment-in-time copy of the order book, published on the `snapshot` port. | A blockchain state dump |
| **Delta** | An incremental book-mutating message on `mktdata`. | |

Two pairs, one per layer, and both are correct. From the network's point of view a publisher is a **transmitter** (or **sender**) and a subscriber is a **receiver**: use those for the act of moving datagrams. For the roles themselves use **publisher** and **subscriber**.

Neither half of either pair is redefined here. `receiver` in particular keeps its plain meaning — anything that receives — so every subscriber is a receiver, and the specs' normative "Publishers SHOULD … receivers MUST …" is correct as written.

## Components

| Term | Definition | Not |
|---|---|---|
| **Decoder** | The pure function turning wire bytes into a struct. | A binary |
| **Parser** | A binary that subscribes, decodes, and republishes records downstream. | The decoder inside it |
| **Book-builder** | A binary consuming parser output that maintains book state and optionally persists it. | A bot |
| **Bot** | A real automated trading client. We ship none. | Any component we build |

## Banned words

| Banned | Use instead | Note |
|---|---|---|
| `arm`, `disarm` (every sense) | see the note below | Two external proper nouns aside — see the note below |
| `bot` (our components) | `book-builder` | |
| `lane` | `feed` or `path` | |
| `feed` (upstream vendor) | `upstream <vendor>` | |
| `stream` (our live traffic) | `feed` | A live feed is a feed; the extra word bought nothing. `snapshot stream` and `delta stream` are the exception: they name the two traffic shapes within one three-port feed, a distinction `feed` cannot carry |
| `frame` (our own traffic) | `datagram` | |
| `channel` (port role) | `port role` | |
| `channel` (venue pub/sub topic) | `venue topic` | |
| `source` (unqualified) | a qualified form | Never bare — see the note below. `source of truth` is the exception: a fixed English idiom naming authority, not an origin |
| `epoch` | `era` | `Unix epoch` is the exception: it is the externally owned name of the 1970-01-01T00:00:00Z origin, not a sense of the word. Use `era` for our own `Reset Count` span |
| `sibling feed` | `feed` | All feeds are siblings; the word adds nothing |
| `tee` | `fan-out` | |
| `sweep` | Name the operation | Currently means three unrelated things. `Trade Flags` bit 1 keeps the name: it is the standard term for an order sweeping several levels, externally defined and carried on the wire |
| `normalization` | `decode` | Reserve `normalization` for cross-feed latency metrics |
| `venue` (Rust trait over product lines) | `product line` or `adapter` | |
| `roster`, `active set` | `published set` | |

**`arm` is banned outright**, in every sense and every place: specs, docs, plans, identifiers, CLI flags, config keys, metric names, log fields, and code comments alike. The only things that survive are names we do not own — the `ARM64` architecture and the `ARM` vendor — which are proper nouns rather than a sense of the word. Replace it by sense — a redundant publisher is a `path`, a `match` or `select!` branch is a `branch`, and the Order-Intent dead-man switch is **set** and **cleared** rather than armed and disarmed. That last one costs nothing on the wire: the switch is carried by `ScheduleCancel`'s `Trigger Time`, where a non-zero timestamp sets it and `0` clears it, so `arm` was only ever prose.

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
