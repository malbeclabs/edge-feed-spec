# DoubleZero Market-by-Price Feed

The DoubleZero Market-by-Price Feed is a wire format for price-aggregated (L2) book data delivered over the DoubleZero Edge service. It defines a fixed-size, multicast-native binary protocol for publishing full-depth aggregated price levels and their changes as a continuous snapshot + delta stream, from any venue with an order book the publisher can observe.

This is a sibling protocol to the DoubleZero Top-of-Book & Trades Feed, the DoubleZero Midpoint Feed, and the DoubleZero Market-by-Order Feed, not a layer on top. Where the top-of-book feed carries two-sided BBO data and trades, the midpoint feed carries a single derived price per instrument, and the market-by-order feed carries the full resting-order population, this feed carries the aggregate resting quantity at every price level of each instrument — the price-aggregated projection of the same book the market-by-order feed carries order-by-order — plus a continuous in-band snapshot mechanism that lets subscribers bootstrap and recover from packet loss over multicast alone.

This document specifies version **3.0.0**: the frame header, application message header, the message types sufficient to operate a working publisher and subscriber, and the sequence-number-anchored snapshot/delta recovery model that is the core of the design.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups. Simple to parse in any language.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram. The protocol defines application messages only; transport, addressing, and group membership are out of scope.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price-carrying message on the market data path carries a `u16` source ID. With a single publisher this is redundant; with many it is essential. Snapshot-path messages omit it: a snapshot group is scoped to one `(channel_id, instrument_id)`, which identifies one book from one source.
7. **Domain-agnostic.** Anything with a two-sided book of resting limit orders — crypto spot, equities, futures, FX, prediction markets — is a valid instrument.
8. **In-band recovery only.** Subscribers bootstrap and recover from packet loss via a continuous publisher-driven snapshot stream. No TCP replay, no out-of-band snapshot service, no subscriber-initiated requests.
9. **Recovery blast radius minimised.** A single lost multicast packet invalidates only the specific instruments whose deltas were in the lost frame, not the whole channel. A per-instrument sequence number carried on every delta message lets subscribers localise the loss.
10. **Price-keyed, absolute quantity.** A level is identified by its `(Side, Price)`, never by a positional index, and carries the absolute aggregate quantity resting there rather than a signed change. A level update is therefore independently interpretable and cannot compound an earlier error.
11. **Depth is carried, and its extent is declared.** The feed is built for the complete book — every price level with resting quantity — and that is the expected case. Where a publisher genuinely cannot observe the whole book it declares the bound rather than presenting a truncated book as complete, so a consumer always knows which it is holding. A subscriber needing only the inside market is served by the top-of-book feed.

---

## Data Types

| Type | Size | Description |
|------|------|-------------|
| `u8` | 1 | Unsigned 8-bit integer |
| `u16` | 2 | Unsigned 16-bit integer, little-endian |
| `u32` | 4 | Unsigned 32-bit integer, little-endian |
| `u64` | 8 | Unsigned 64-bit integer, little-endian |
| `i64` | 8 | Signed 64-bit integer, little-endian |
| `i8` | 1 | Signed 8-bit integer |
| `char[N]` | N | Fixed-length ASCII, left-justified, null-padded on right |
| `ts_ns` | 8 | Nanoseconds since Unix epoch (`u64`) |
| `price` | 8 | Signed 64-bit integer with per-instrument implied exponent (`i64`) |
| `qty` | 8 | Unsigned 64-bit integer with per-instrument implied exponent (`u64`) |

---

## Transport Framing

One UDP datagram = one frame. Frames do not span packet boundaries. Multiple application messages may be packed into a single frame. The maximum frame size is **1,232 bytes** to leave room for GRE encapsulation headers used by the DoubleZero network's last-mile delivery.

### Three-Port Channel Model

Each channel is delivered to **one multicast group on three destination ports**, extending the two-port model of the [Reference Data Distribution supplement](../reference-data/spec.md) with a dedicated snapshot stream:

| Port | Carries |
|------|---------|
| mktdata | `LevelUpdate`, `BookClear`, `Trade`, `Liquidation`, `BatchBoundary`, `InstrumentReset`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |
| snapshot | `SnapshotBegin`, `SnapshotLevel`, `SnapshotEnd` |

The frame header and application message header are identical on all three ports. A single decoder implementation handles all three. Concrete port assignments are out of scope for this spec; each deployment publishes its port mapping out of band.

A subscriber bootstrapping from a cold start MUST bind all three ports. A subscriber with an out-of-band snapshot mechanism (e.g., proprietary replay or a historical database) MAY bind only `mktdata` + `refdata` and skip `snapshot`; in that case it forfeits the in-band recovery mechanism.

The snapshot stream has a fundamentally different traffic shape from the delta stream — continuous and steady versus bursty and event-driven — and separating them lets subscribers opt out, lets operators rate-limit the two streams independently at the network layer, and keeps per-port sequence-number diagnostics clean.

### Frame Header (24 bytes)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     Magic (0x4442)            |  Schema Ver   |  Channel ID   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Sequence Number (u64)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|        Send Timestamp (ts_ns, u64)                            |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|    Msg Count  | Reset Count   |        Frame Length (u16)     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Magic | `u16` | `0x4442`. Frame delimiter. Distinct from the top-of-book feed's `0x445A`, the market-by-order feed's `0x4444`, the midpoint feed's `0x4D44`, the order-intent feed's `0x494F`, and the perp-stats feed's `0x4450` to prevent cross-protocol misrouting. A consumer MUST validate that a received frame's `Magic` equals the value for the feed it subscribed to and discard any frame that does not match. |
| 2 | Schema Version | `u8` | Wire format generation, equal to this spec's MAJOR version. `3` for all `3.x.y` releases. A subscriber MUST discard frames whose version it does not implement. |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. |
| 4 | Sequence Number | `u64` | Monotonically increasing **per channel per port**, starting from 0. Resets to 0 when `Reset Count` changes. Used for per-port gap detection. The `mktdata`, `refdata`, and `snapshot` ports each have an independent `Sequence Number` series; see [Sequence Numbers and Recovery](#sequence-numbers-and-recovery) for how the series relate. |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value. The `Reset Count` is shared across all three ports of the channel. |
| 22 | Frame Length | `u16` | Total frame length in bytes, including this header. |

---

## Application Message Header (4 bytes)

Every application message begins with:

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Message Type | `u8` | See Message Types table. |
| 1 | Message Length | `u8` | Total message length including this header. Max 255. |
| 2 | Flags | `u16` | Bit 0: snapshot (1) vs. incremental (0). Bits 1–15: reserved. |

A publisher MUST set bit 0 on `SnapshotBegin`, `SnapshotLevel` and `SnapshotEnd`, and MUST clear it on every message carried on `mktdata` and `refdata`. This feed is the second sibling with a dedicated snapshot stream, and the bit has until now been defined but never assigned a normative setting anywhere in the family; pinning it here makes it verifiable from a capture. A subscriber MUST NOT rely on it to route a message — the Type ID and the port already determine that — and SHOULD count messages whose bit disagrees with the port they arrived on, as a publisher defect.

No message type on this feed exceeds 130 bytes, so the 255-byte cap is not a binding constraint here.

A subscriber MUST bounds-check `Message Length` before using it to advance the frame walk. A value below `4`, or exceeding the bytes remaining in the frame, or inconsistent with `Frame Length`, makes the frame malformed: stop parsing that frame, count it, and keep the channel — do not fail or reset. Without the `< 4` floor a `Message Length` of `0` advances the walk by zero bytes and spins forever on a single malformed or misrouted datagram.

---

## Identity Model

### Instrument IDs

The unique key for an instrument in this feed is the tuple **`(channel_id, instrument_id)`**. `instrument_id` is a `u32` scoped to its channel; it need not be globally unique across channels. Subscribers consuming multiple channels MUST key their internal instrument map by the tuple.

Operators running multiple publisher instances that share a single `source_id` (as defined in the [Source ID Registry](../sources/spec.md)) MAY assign globally unique `instrument_id`s as an operational convenience, in which case the `channel_id` component of the key becomes informational only. The spec does not require this.

### Price Levels

Within an instrument, a price level is identified by the tuple **`(Side, Price)`**. There is no positional level index in the addressing model: a level is never identified by its rank, and a subscriber MUST NOT key book state on rank. `Price` is interpreted using the instrument's `Price Exponent` from reference data.

A `(channel_id, instrument_id)` identifies **one book from one source**. A publisher MUST NOT emit level updates for a single `(channel, instrument)` under more than one `Source ID`; an instrument observed at two venues is two instruments with two `InstrumentDefinition` entries and two `instrument_id` values, not one `instrument_id` with two sources.

### Channel Sharding

Sharding the published instrument set across multiple publisher instances — each on its own channel — is supported natively via `Channel ID` in the frame header. Each channel is an independent state machine with its own `Reset Count`, `Sequence Number` series per port, `Manifest Seq`, and snapshot cycle. Grouping criteria (by asset class, by liquidity tier, by source venue) and discovery mechanisms are deployment-level concerns and out of scope for this spec.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal. Inherited; identical to siblings. |
| `0x02` | InstrumentDefinition | 130 | refdata | Reference data for an instrument. Inherited from the top-of-book feed. |
| `0x03` | *(reserved)* | — | — | Quote in the top-of-book feed, Midpoint in the midpoint feed. Intentionally unused here to prevent accidental cross-decoding if a frame is misrouted. |
| `0x04` | Trade | 52 | mktdata | Venue-level trade summary. **Identical byte-for-byte to the top-of-book feed's Trade**, carried here as a convenience for consumers who want a trade tape alongside the book. |
| `0x05` | *(reserved)* | — | — | |
| `0x06` | EndOfSession | 12 | mktdata | Inherited. No more data for this session. |
| `0x07` | ManifestSummary | 24 | refdata | Published instrument set summary. Inherited; see the [Reference Data Distribution supplement](../reference-data/spec.md). |
| `0x08` | Liquidation | 48 | mktdata | Trade-companion annotation. **Identical byte-for-byte to the top-of-book feed's Liquidation.** Emitted in the same frame as its `Trade`. |
| `0x13` | BatchBoundary | 16 | mktdata | Atomic-batch delimiter. **Byte-for-byte identical to the market-by-order feed's `0x13`.** Required of batching publishers, absent on non-batching channels. |
| `0x14` | InstrumentReset | 28 | mktdata | Per-instrument surgical resync signal. **Byte-for-byte identical to the market-by-order feed's `0x14`.** |
| `0x20` | SnapshotBegin | 40 | snapshot | Start of a per-instrument snapshot group. Prefix-superset of the market-by-order feed's 36-byte `0x20`; see below. |
| `0x22` | SnapshotEnd | 20 | snapshot | End of a per-instrument snapshot group. **Byte-for-byte identical to the market-by-order feed's `0x22`.** |
| `0x40` | LevelUpdate | 48 | mktdata | A price level's aggregate quantity changed. The core message. |
| `0x41` | BookClear | 36 | mktdata | Bulk removal of levels from one or both sides. |
| `0x42` | SnapshotLevel | 32 | snapshot | One price level in a snapshot. |

A decoder encountering an unknown type MUST skip the message using its `Message Length` field and continue parsing the frame.

### Cross-Spec Type ID Policy

A message Type ID that appears in more than one sibling feed MUST carry the same semantic meaning in each. The shared Type IDs at this writing are `0x01` (Heartbeat), `0x02` (InstrumentDefinition), `0x04` (Trade), `0x06` (EndOfSession), `0x07` (ManifestSummary), and `0x08` (Liquidation). Heartbeat, EndOfSession, and ManifestSummary are byte-for-byte identical across every sibling that carries them. Trade and Liquidation are byte-for-byte identical between the top-of-book feed, the market-by-order feed, and this feed. InstrumentDefinition shares the Type ID but each sibling defines its own layout — this feed, market-by-order, top-of-book, order-intent, and perp-stats share the 130-byte layout; the midpoint feed carries a slimmed 64-byte variant.

Four payloads are shared with the market-by-order feed at its own Type IDs rather than renumbered into this feed's range, because they are the same payload and reassignment is what the policy forbids: `BatchBoundary` (`0x13`), `InstrumentReset` (`0x14`) and `SnapshotEnd` (`0x22`) are byte-for-byte identical, and `SnapshotBegin` (`0x20`) is a prefix-superset — its first 36 bytes are the market-by-order layout, with `Depth Bound` appended at offset 36. `InstrumentDefinition` is the precedent for one Type ID carrying different lengths across siblings (130 bytes here and in top-of-book, 64 in midpoint).

Renumbering these would have gained nothing: the misroute-rejection rationale that motivates a distinct range does not apply to an identical payload, since decoding a market-by-order `0x13` under this feed's dispatch yields a correct `BatchBoundary`. `Magic` is what rejects a misrouted frame.

This feed's genuinely new payloads — `LevelUpdate` (`0x40`), `BookClear` (`0x41`) and `SnapshotLevel` (`0x42`) — take fresh Type IDs in the **`0x40`–`0x4F` range**, which does not overlap any sibling. `SnapshotLevel` cannot reuse the market-by-order feed's `0x21` (`SnapshotOrder`): the payloads differ, and that is exactly the reassignment the policy prohibits. A Type ID used by one sibling for a given payload MUST NOT be reassigned to a different payload here; where a sibling does not carry that payload, the slot is reserved.

The **`0x50`–`0x5F` range is reserved** for a future positional-index addressing mode (see [Versioning and Forward Compatibility](#versioning-and-forward-compatibility)). Because such a mode would occupy its own Type IDs rather than reinterpreting these, a subscriber implementing only the price-keyed messages defined here skips index-keyed messages by `Message Length` exactly as it skips any other unknown type. There is no addressing-mode negotiation and no mode field.

---

## Message Definitions

### 0x01 Heartbeat (16 bytes)

Inherited from the top-of-book feed; reproduced here for convenience. Sent every N seconds on the `mktdata` port when there is no other traffic. Receivers use this for stale-connection detection.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x01`, Length=16 |
| 4 | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5 | Reserved | 3B | Padding |
| 8 | Timestamp | `ts_ns` | Current time |

The `snapshot` port's continuous round-robin stream and the `refdata` port's `ManifestSummary` cadence are their own liveness signals; `Heartbeat` is emitted on `mktdata` only.

### 0x02 InstrumentDefinition (130 bytes)

Inherited from the top-of-book feed verbatim. Reproduced in full below for standalone readability.

Maps a numeric Instrument ID to human-readable metadata. Carried on the `refdata` port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x02`, Length=130 |
| 4 | Instrument ID | `u32` | Unique numeric ID for this instrument |
| 8 | Source ID | `u16` | Originating venue, as assigned by the [Source ID Registry](../sources/spec.md). |
| 10 | Symbol | `char[64]` | Human-readable label, left-justified and null-padded (e.g., `"BTC-USDT"`). Truncate only if the venue's symbol exceeds 64 bytes. |
| 74 | Leg1 | `char[8]` | First leg/component. Context-dependent: base currency, underlying, outcome name. |
| 82 | Leg2 | `char[8]` | Second leg/component. Context-dependent: quote/settlement currency. |
| 90 | Asset Class | `u8` | See Asset Class table. |
| 91 | Price Exponent | `i8` | Implied decimal exponent for price fields. e.g., `-2` means divide raw value by 100. |
| 92 | Qty Exponent | `i8` | Implied decimal exponent for quantity fields. |
| 93 | Market Model | `u8` | See Market Model table. |
| 94 | Tick Size | `price` | Minimum price increment (interpreted via Price Exponent). |
| 102 | Lot Size | `qty` | Minimum quantity increment (interpreted via Qty Exponent). |
| 110 | Contract Value | `u64` | Notional per contract. 0 if not applicable (e.g., spot). |
| 118 | Expiry | `ts_ns` | Expiration timestamp. 0 for non-expiring. |
| 126 | Settle Type | `u8` | 0=N/A, 1=Cash, 2=Physical |
| 127 | Price Bound | `u8` | 0=Unbounded, 1=Bounded [0,1] (binary outcomes), 2=Non-negative only |
| 128 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. See supplement. |

`Tick Size` and `Price Bound` together bound the number of distinct price levels an instrument can carry, which is the only structural bound on book width this feed provides. Subscribers sizing buffers from it should treat it as a ceiling, not a forecast: populated depth is an empirical property of the venue.

#### Asset Class Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | Crypto Spot |
| 2 | Prediction Binary |
| 3 | Prediction Scalar |
| 4 | Prediction Categorical |
| 5 | Perpetual Future |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

#### Market Model Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | CLOB |
| 2 | AMM |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown). For an AMM instrument the publisher discretizes the curve into synthetic price levels; see the `AMM-synthetic` level flag in [0x40 LevelUpdate](#0x40-levelupdate-48-bytes).

### 0x04 Trade (52 bytes)

Identical to the top-of-book feed's Trade message, byte-for-byte. Carried on the `mktdata` port as a venue-level summary of a single trade.

Trades are independent of the book stream and are not required to reconstruct book state. A trade's effect on the book arrives as one or more `LevelUpdate` messages with `Reason = 1` (Trade).

A future shared supplement is anticipated to factor `Trade` out of the sibling specs and carry it independently; until then the message is duplicated.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x04`, Length=52 |
| 4 | Instrument ID | `u32` | Instrument traded |
| 8 | Source ID | `u16` | Originating source |
| 10 | Aggressor Side | `u8` | 1=Buy, 2=Sell, 0=Unknown |
| 11 | Trade Flags | `u8` | Bit 0: block, bit 1: sweep, bit 2: cross. Set to 0 if not applicable. |
| 12 | Source Timestamp | `ts_ns` | Venue timestamp of execution |
| 20 | Trade Price | `price` | Execution price |
| 28 | Trade Quantity | `qty` | Execution size |
| 36 | Trade ID | `u64` | Venue-assigned trade ID. 0 if unavailable. |
| 44 | Cumulative Volume | `qty` | Session cumulative volume. 0 if unavailable. |

### 0x06 EndOfSession (12 bytes)

Inherited. No more data on this channel for the current session.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x06`, Length=12 |
| 4 | Timestamp | `ts_ns` | |

### 0x07 ManifestSummary (24 bytes)

Inherited. Periodic summary of the published instrument set on this channel. Carried on the `refdata` port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); reproduced here for convenience.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x07`, Length=24 |
| 4  | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5  | Valid | `u8` | `1` when the channel has an established instrument set; `0` when the publisher is uninitialized or the channel is inactive. See supplement. |
| 6  | Reserved | 2B | Padding |
| 8  | Manifest Seq | `u16` | Increments every time the published instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the published set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

### 0x08 Liquidation (48 bytes)

Inherited from the top-of-book feed verbatim. Annotates a forced (liquidation or ADL) `Trade`, keyed on `Trade ID`, and is emitted in the same frame as the `Trade` it annotates.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x08`, Length=48 |
| 4 | Instrument ID | `u32` | |
| 8 | Source ID | `u16` | |
| 10 | Liquidation Flags | `u8` | Bit 0: liquidated side (0 = long liquidated, 1 = short liquidated). Bit 1: ADL. |
| 11 | Method | `u8` | 0 = market, 1 = backstop, 0xFF = unknown. |
| 12 | Trade ID | `u64` | Venue trade ID of the paired `Trade` |
| 20 | Mark Price | `price` | Mark price at liquidation |
| 28 | Liquidated User | 20B | Liquidated account address |

### 0x40 LevelUpdate (48 bytes)

The core message. The aggregate resting quantity at one price level changed.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x40)  |  Length (48)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |     Side      |    Action     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Per-Instrument Seq (u32)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Price (price, i64)                      |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Quantity (qty, u64)                      |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Timestamp (ts_ns)                        |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|       Order Count (u16)       |       Level Index (u16)       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Update Reason |  Level Flags  |          Reserved             |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x40`, Length=48 |
| 4  | Instrument ID | `u32` | Instrument this level belongs to |
| 8  | Source ID | `u16` | Originating source. |
| 10 | Side | `u8` | `0`=Bid/Buy, `1`=Ask/Sell |
| 11 | Action | `u8` | See Action table. **Informational only**; see [Absolute Apply Semantics](#absolute-apply-semantics). |
| 12 | Per-Instrument Seq | `u32` | See [Per-Instrument Delta Sequence](#per-instrument-delta-sequence). Same type and offset as the market-by-order feed's. |
| 16 | Price | `price` | The level's price. Uses instrument's Price Exponent. **This is the level's key.** |
| 24 | Quantity | `qty` | **Absolute** aggregate resting quantity at this price after the change, not a delta. `0` removes the level. |
| 32 | Timestamp | `ts_ns` | Venue time of the change |
| 40 | Order Count | `u16` | Number of resting orders aggregated at this price. `0xFFFF` when the venue does not expose it or the true count exceeds `0xFFFE`. `0` is a real value. |
| 42 | Level Index | `u16` | Rank of this level on its side at emission time, `0` = inside market. **Informational only**; `0xFFFF` when the publisher does not provide it or the true rank is `0xFFFF` or deeper. See [Level Index](#level-index). |
| 44 | Update Reason | `u8` | See Update Reason table. |
| 45 | Level Flags | `u8` | See Level Flags table. |
| 46 | Reserved | 2B | Padding to 48 bytes. |

#### Action

| Value | Name | Meaning |
|-------|------|---------|
| 0 | Unknown | Publisher could not determine which transition this is |
| 1 | New | No quantity rested at this price before the change |
| 2 | Change | Quantity rested at this price and its total changed |
| 3 | Delete | The level is being removed |
| 255 | Other | Venue-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknowns as `0` (Unknown). New values MAY be defined in future schema versions without a Schema Version bump. `0` exists because this field is informational: a publisher whose upstream does not distinguish an insert from an update states that rather than guessing.

**`Unknown` covers the New-versus-Change ambiguity only.** A removal is always determinable — the publisher knows the level's quantity reached zero — so `Action = 3` (Delete) is required for it, and a publisher that reports `Unknown` for insertions and updates still reports `Delete` for removals. Without this, a publisher using `Unknown` would have no conformant way to express a removal at all.

A publisher MUST set `Quantity = 0` when `Action = 3` (Delete), and MUST NOT emit `Quantity = 0` with any other `Action`, including `Unknown`.

#### Absolute Apply Semantics

A subscriber applies a `LevelUpdate` as:

```
if Quantity == 0:  remove (Side, Price) from the book
else:              set (Side, Price) to Quantity
```

`Action` MUST NOT gate this. Every `LevelUpdate` states the complete resulting state of one level, so a subscriber that applies it by quantity alone always produces the correct level regardless of what `Action` claims.

`Action` exists so that a subscriber can detect divergence between the publisher's view and its own. A subscriber SHOULD count, without altering the applied result:

- `Action = 1` (New) for a `(Side, Price)` already present in its book,
- `Action = 2` (Change) for a `(Side, Price)` absent from its book,
- `Action = 3` (Delete) carrying a non-zero `Quantity`,
- `Quantity = 0` carried with any `Action` other than `3`, which the publisher rule above forbids.

Each is a publisher defect or an undetected loss, and each is worth surfacing. None is a reason to take a different code path: an `Action` byte that is wrong must never be able to corrupt a book.

#### Level Index

`Level Index` is the level's rank on its side at the moment the publisher emitted the message, with `0` being the inside market. It exists to preserve rank where an upstream supplies it — a venue whose native feed identifies levels positionally loses that information entirely under price keying — and to hold the concept open should the reserved positional-addressing mode of `0x50`–`0x5F` ever be defined.

It is deliberately not justified as a filtering aid. A subscriber maintains a price-ordered book to consume this feed at all, and bootstraps from a snapshot where rank is not carried, so rank is an O(1) lookup it already has; a wire field would be redundant with the container and staler than it.

It is **not** part of the addressing model and carries no guarantees. A subscriber MUST NOT key book state on it, MUST NOT use it to locate a level, and MUST NOT treat it as valid after any subsequent update to the same side — inserting a level changes the rank of every level beneath it, and this feed does not re-emit those levels. Publishers that cannot supply a rank MUST set `0xFFFF`.

#### u16 Sentinels

`Level Index` and `Order Count` share one convention: `0xFFFF` means *not provided, or beyond what this field can express*, and both saturate at it rather than wrapping. A subscriber MUST NOT read `0xFFFF` in either field as a magnitude — it is neither a rank of 65,535 nor a count of 65,535. Genuine values above `0xFFFE` are reported as `0xFFFF`, so the two cases are deliberately not distinguished: a consumer that needs an exact count or rank that deep is not served by a `u16`, and conflating "absent" with "too large to say" is safer than silently truncating either into a plausible-looking number.

`Order Count = 0` is a real value, not a sentinel. It cannot occur at a level carried in a snapshot, where `Quantity` is non-zero by rule, but it is well-defined on a `LevelUpdate`.

#### Update Reason

This is a distinct field from the market-by-order feed's `Reason` on `OrderCancel`, with its own value space: aggregation collapses that feed's six cancel reasons into a single `Cancel` here, because a level shrinking by cancellation does not carry which order's cancellation caused it. Do not map the two enums onto each other.

| Value | Name | Meaning |
|-------|------|---------|
| 0 | Unknown | No reason available |
| 1 | Trade | Quantity was consumed by an execution |
| 2 | Cancel | Quantity was withdrawn by order cancellation, for any reason |
| 3 | NewOrder | Quantity was added by a newly resting order |
| 4 | Amend | Quantity changed by in-place modification of a resting order |
| 5 | VenueAction | Change forced by the venue (expiry, risk action, self-match prevention) |
| 255 | Other | Venue-specific; documented out of band |

`Update Reason` is what survives of order-level causality after price aggregation. It lets a consumer distinguish a level shrinking because it traded from a level shrinking because it was cancelled, which price and quantity alone cannot express.

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknowns as `0` (Unknown). New values MAY be defined in future schema versions without a Schema Version bump.

#### Level Flags

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | implied | Level consists of implied liquidity derived from related instruments |
| 1 | AMM-synthetic | Level is a discretized point on a continuous curve, not a real resting order |
| 2–7 | reserved | Publishers MUST set to 0; receivers MUST ignore |

### 0x41 BookClear (36 bytes)

Bulk removal of levels. Used where a venue removes many levels at once and enumerating them as individual `LevelUpdate` messages would be wasteful or would not correspond to any real per-level event: a trading halt, a session boundary, a settled instrument, or an upstream range-delete.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x41`, Length=36 |
| 4  | Instrument ID | `u32` | |
| 8  | Source ID | `u16` | |
| 10 | Clear Side | `u8` | `0`=Bid, `1`=Ask, `2`=Both. Deliberately **not** named `Side`: it extends the shared `Side` enum with a value no other message in this feed or any sibling accepts, so it carries a distinct name rather than making the shared encoding message-local. |
| 11 | Scope | `u8` | `0` = clear the entire side(s), `From Price` ignored. `1` = clear from `From Price` outward to the far end of the side. |
| 12 | Per-Instrument Seq | `u32` | Ordered in the same series as `LevelUpdate` |
| 16 | From Price | `price` | Inclusive bound when `Scope = 1`. For bids, clears every level at or below it; for asks, every level at or above it. |
| 24 | Timestamp | `ts_ns` | Venue time of the clear |
| 32 | Clear Reason | `u8` | See Clear Reason table. |
| 33 | Reserved | 3B | Padding |

`Scope = 1` requires `Clear Side` to be `0` or `1`. A message with `Scope = 1` and `Clear Side = 2` is malformed, because one price cannot bound both sides; a subscriber MUST discard and count it.

`BookClear` is not a resynchronization signal. It asserts that the named levels are gone, and a subscriber that applies it stays `ready`. A publisher that has lost confidence in its own book state MUST use [0x14 InstrumentReset](#0x14-instrumentreset-28-bytes) instead.

#### Clear Reason

| Value | Name | Meaning |
|-------|------|---------|
| 0 | Unspecified | No reason given |
| 1 | Halt | Instrument halted; book withdrawn |
| 2 | SessionEnd | Trading session closed |
| 3 | VenueReset | Venue cleared the book |
| 4 | Settled | Instrument settled or expired; book is permanently gone |
| 255 | Other | Venue-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value.

### 0x13 BatchBoundary (16 bytes)

Delimiter marking an atomic batch of updates. Publishers whose upstream has natural batch semantics (blockchain blocks, matching-engine rounds, exchange message-group boundaries) MUST emit this message after every batch of deltas. Publishers whose upstream streams changes one at a time MAY omit it entirely. Subscribers MUST tolerate its absence, since it is legitimately absent on non-batching channels.

Semantics: *all `mktdata` deltas arriving between the previous `BatchBoundary` (or the start of the channel) and this `BatchBoundary` apply atomically. Book states observed between the previous and this boundary are not guaranteed to be consistency points; the state at the boundary is.*

Subscribers with strict atomicity requirements MAY buffer deltas between boundaries and apply them as a group.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x13`, Length=16 |
| 4 | Batch ID | `u32` | Publisher-defined, monotonically increasing within the current `Reset Count` era. For blockchain sources, SHOULD be the block number truncated to `u32`. |
| 8 | Batch Time | `ts_ns` | Venue time of the batch |

`BatchBoundary` is informational for book reconstruction — a subscriber that ignores it MUST still produce a correct book state — but it does govern when the comparison in [Crossed-Book Monitoring](#crossed-book-monitoring) applies. Publishers whose upstream batches MUST emit it; see [Publisher Behavior](#delta-stream).

### 0x14 InstrumentReset (28 bytes)

Publisher signal that one instrument's on-wire state is being discarded and re-bootstrapped. Used when the publisher detects that its internal book state for a single instrument has diverged from the upstream source (e.g., a periodic consistency check against a re-read of the upstream book, or a detected gap in the upstream event stream) and wants to force subscribers to re-bootstrap that instrument only, without tearing down the channel.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x14`, Length=28 |
| 4  | Instrument ID | `u32` | |
| 8  | Reason | `u8` | See Reset Reason table. |
| 9  | Reserved | 3B | Padding |
| 12 | New Anchor Seq | `u64` | The `mktdata`-port `Sequence Number` from which the next snapshot for this instrument will be valid. The publisher MUST emit a snapshot for this instrument on the `snapshot` port with `Anchor Seq` equal to this value before resuming delta emission. |
| 20 | Timestamp | `ts_ns` | |

**Subscriber behaviour:** on receipt of `InstrumentReset(I, new_anchor_seq=S')`:
1. Discard all level state for instrument `I`, including any in-flight snapshot for `I`.
2. Discard any buffered deltas for `I` with `mktdata_seq ≤ S'`.
3. Mark `I` as awaiting a snapshot, recording `S'` as the required anchor: any snapshot for `I` with an older `Anchor Seq` MUST be discarded. See [Instrument Reset](#instrument-reset) for the full rule.
4. Buffer further deltas for `I` with `mktdata_seq > S'` until the recovery snapshot arrives, then apply per [Cold Start](#cold-start) steps 4–6.

**Publisher behaviour during active snapshot:** if the publisher issues `InstrumentReset(I)` while a snapshot for `I` is in flight on the `snapshot` port, it MUST either (a) complete and invalidate the in-flight snapshot (subscribers will detect the `Anchor Seq` mismatch and discard), or (b) abort the in-flight snapshot on the publisher side. Publishers MUST then emit a fresh snapshot with the new `Anchor Seq`. The choice between (a) and (b) is publisher-defined.

#### Reset Reason

| Value | Name | Meaning |
|-------|------|---------|
| 0 | Unspecified | No reason given |
| 1 | PublisherInconsistency | Publisher-side integrity check detected divergence |
| 2 | VenueResync | Upstream venue reset or resync'd this instrument |
| 3 | UpstreamGap | Publisher detected a gap in its upstream event stream |
| 255 | Other | Publisher-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value.

### 0x20 SnapshotBegin (40 bytes)

Opens a per-instrument snapshot group on the `snapshot` port.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x20`, Length=40 |
| 4  | Instrument ID | `u32` | |
| 8  | Anchor Seq | `u64` | The `mktdata`-port `Sequence Number` at the moment the publisher captured the book state for this snapshot. See [Snapshot Anchor Seq](#snapshot-anchor-seq) for the precise semantics. |
| 16 | Total Levels | `u32` | Number of `SnapshotLevel` messages that will follow before the matching `SnapshotEnd`, across both sides. MAY be `0` for an instrument with an empty book at capture time. |
| 20 | Snapshot ID | `u32` | Monotonically increasing per `(channel_id, instrument_id)` within the current `Reset Count` era. Identifies this snapshot instance, so that subscribers can associate `SnapshotLevel` messages with the correct `SnapshotBegin` and detect stale or out-of-order snapshot fragments. |
| 24 | Last Instrument Seq | `u32` | The `Per-Instrument Seq` of the last delta applied to this instrument at or before `Anchor Seq`. Subscribers MUST initialise their `last_applied_instrument_seq` tracker to this value after applying the snapshot. `0` if no deltas have been applied for this instrument in the current `Reset Count` era. |
| 28 | Timestamp | `ts_ns` | Publisher wall-clock at capture. |
| 36 | Depth Bound | `u32` | `0` when this snapshot carries the complete book. Otherwise the number of levels per side the publisher carries, beyond which level state is **unknown rather than empty**. See below. |

**Bytes 0–35 are byte-for-byte the market-by-order feed's 36-byte `0x20`**, with `Total Orders` reading as `Total Levels` — the same field at the same offset, counting the records that follow. `Depth Bound` is appended at offset 36, so a decoder written against either spec reads the shared prefix correctly and one written against the shorter layout skips the tail via `Message Length`, exactly as the forward-compatibility rules intend.

`Timestamp` sits at offset 28 and is therefore not 8-byte aligned within the message. **This is deliberate and inherited, not a cost of the prefix-superset**: the sibling's layout already places it there, and `InstrumentReset` — byte-identical across both feeds — likewise carries `New Anchor Seq` at 12 and `Timestamp` at 20. Every message this feed defines from scratch is 8-aligned throughout. Realigning would fork the layout from the sibling for no practical gain, because message-relative alignment does not produce buffer-relative alignment anyway: messages pack at arbitrary offsets behind the 24-byte frame header and at sizes that are not all multiples of 8, so a field's address depends on what preceded it in the frame. Both target architectures handle unaligned 8-byte loads in hardware at negligible cost.

Publishers MUST NOT interleave two snapshot groups for different instruments **within a channel**. A `SnapshotBegin` for instrument A is always followed by exactly `Total Levels` `SnapshotLevel` messages for A and then a `SnapshotEnd` for A, before any `SnapshotBegin` for a different instrument on that channel.

**Scoped to the channel, not to the port.** A channel is an independent state machine with its own snapshot cycle, and the frame header carries `Channel ID` precisely so a deployment may carry more than one on a `snapshot` port. Two channels whose groups alternate there are each conformant, and a subscriber tracks the open group per `Channel ID` — reading this as a port-wide constraint makes a conformant sharded publisher look like an interleaving one.

An instrument with an empty book at capture time is represented by `SnapshotBegin(total_levels=0)` immediately followed by `SnapshotEnd` with no intervening `SnapshotLevel` messages.

**`Depth Bound` exists because a consumer cannot otherwise distinguish "the book ends here" from "the publisher stopped here."** Conflating the two silently under-states available liquidity, which is precisely the failure mode a full-depth feed exists to avoid. Publishers carrying the complete book — the expected case — MUST set `0`. A publisher structurally unable to observe the whole book MUST declare the bound rather than present a truncated book as complete, and subscribers MUST treat levels beyond a declared bound as unknown, excluding them from any calculation that assumes an exhaustive book.

**A subscriber's depth bound for an instrument is unknown until a `SnapshotBegin` for it establishes one, and MUST default to unknown rather than to `0`.** Defaulting to `0` would make a never-snapshotted instrument assert completeness — the exact failure this field exists to prevent, arrived at through the subscriber's own initialisation rather than through anything the publisher sent. The distinction is between the wire value `0`, which is a positive claim of completeness a publisher made, and the absence of any claim.

The same applies to a subscriber that binds only `mktdata` + `refdata` and forgoes the in-band snapshot stream, as [Three-Port Channel Model](#three-port-channel-model) permits: it never receives a `SnapshotBegin`, so it never learns the bound, and it MUST treat depth as unknown for every instrument on the channel for as long as that holds.

### 0x42 SnapshotLevel (32 bytes)

One price level in a snapshot. The Instrument ID is implied by the containing `SnapshotBegin`; it is not repeated on each level.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x42`, Length=32 |
| 4  | Snapshot ID | `u32` | MUST match the containing `SnapshotBegin`'s Snapshot ID. Subscribers MUST discard any `SnapshotLevel` whose `Snapshot ID` does not match the currently-open `SnapshotBegin`. |
| 8  | Price | `price` | The level's price |
| 16 | Quantity | `qty` | Aggregate resting quantity at capture. MUST be non-zero; an empty level is represented by its absence. |
| 24 | Order Count | `u16` | Number of resting orders aggregated at this price. `0xFFFF` when the venue does not expose it or the true count exceeds `0xFFFE`, matching `LevelUpdate`. |
| 26 | Side | `u8` | `0`=Bid, `1`=Ask |
| 27 | Level Flags | `u8` | Same semantics as `LevelUpdate`'s Level Flags field |
| 28 | Reserved | 4B | Padding |

Publishers SHOULD emit a snapshot's levels best-to-worst within each side, but subscribers MUST NOT depend on ordering: the levels of a snapshot group are a set, and the subscriber's own sorted container establishes rank.

### 0x22 SnapshotEnd (20 bytes)

Closes a per-instrument snapshot group.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x22`, Length=20 |
| 4  | Instrument ID | `u32` | MUST match the opening `SnapshotBegin` |
| 8  | Anchor Seq | `u64` | MUST match the opening `SnapshotBegin` |
| 16 | Snapshot ID | `u32` | MUST match the opening `SnapshotBegin` |

If a subscriber receives a `SnapshotEnd` whose `Instrument ID`, `Anchor Seq`, or `Snapshot ID` does not match the currently-open `SnapshotBegin`, or whose number of intervening `SnapshotLevel` messages does not equal `Total Levels`, the subscriber MUST discard the partial book and await a fresh snapshot for the instrument.

---

## Sequence Numbers and Recovery

The market-by-price feed carries three independent sequence-number series plus a derived anchor, together defining the snapshot/delta composition rules.

### Per-Port Channel Sequence

Each of the three ports — `mktdata`, `refdata`, `snapshot` — carries its own `Sequence Number` in the frame header. The three series are independent of each other and all reset to 0 when the channel's `Reset Count` changes. Semantically:

- The `mktdata` channel seq detects gaps in the delta stream. Any gap invalidates the `mktdata`-path state only.
- The `refdata` channel seq detects gaps in the reference-data stream; gap handling is specified in the [Reference Data Distribution supplement](../reference-data/spec.md).
- The `snapshot` channel seq detects gaps within a snapshot group. A gap mid-snapshot invalidates that specific snapshot instance; the subscriber discards the partial book and awaits a fresh `SnapshotBegin` for the instrument.

### Per-Instrument Delta Sequence

`LevelUpdate` and `BookClear` each carry a `u32` `Per-Instrument Seq`, monotonically increasing per `(channel_id, instrument_id)` within the current `Reset Count` era. The first delta for an instrument after a channel reset carries `Per-Instrument Seq = 1`; each subsequent delta for that instrument increments by exactly 1. Both message types share one series, because both mutate the book and their relative order is significant.

**The `Per-Instrument Seq` MUST NOT be reset at snapshot boundaries.** It restarts at 1 only on `Reset Count` change. Publishers MUST emit per-instrument sequence numbers densely — no skips — so that subscribers can detect gaps unambiguously.

The purpose of `Per-Instrument Seq` is to narrow the blast radius of a `mktdata` channel gap. A channel-level gap tells the subscriber a frame was lost but not which instruments' deltas were in the lost frame. On the next delta arriving for each instrument, the subscriber compares the `Per-Instrument Seq` to its `last_applied_instrument_seq` for that instrument: continuity confirms the instrument is clean; a skip reveals an instrument that needs re-snapshotting.

If `Per-Instrument Seq` reset at snapshots, a subscriber that missed a snapshot but then saw a delta with `Per-Instrument Seq = 1` would be unable to distinguish "fresh post-snapshot delta" from "late duplicate of an old delta". Keeping the counter monotonic within the reset-count era makes `Per-Instrument Seq ≤ last_applied` unambiguously mean *duplicate* and `Per-Instrument Seq > last_applied + 1` unambiguously mean *gap*.

### Snapshot Anchor Seq

`SnapshotBegin` carries an `Anchor Seq` (`u64`) that MUST equal the `mktdata`-port `Sequence Number` at the moment the publisher captured the book state for the snapshot. The meaning is:

> *This snapshot is the exact state of the instrument after every delta that appeared in `mktdata` frames with `Sequence Number ≤ Anchor Seq` has been applied, and before any delta in frames with `Sequence Number > Anchor Seq`.*

`SnapshotEnd` carries the same `Anchor Seq` as its matching `SnapshotBegin`.

`Anchor Seq` is **always a `mktdata`-port sequence number**. The `snapshot` port's own frame-level seq is unrelated.

A subscriber applying a snapshot for instrument `I` with `Anchor Seq = S` and `Last Instrument Seq = K` initialises its per-instrument tracking state as:

- `last_applied_mktdata_seq[I] = S`
- `last_applied_instrument_seq[I] = K`

It then replays any buffered deltas for `I` whose `mktdata_seq > S`, incrementing the trackers as each delta applies.

---

## Subscriber Algorithm

A subscriber adopting this feed maintains the following state per channel:

### Channel State

```
channel_state = {
  reset_count:        u8    = 0,
  mktdata_seq_last:   u64   = null,
  refdata_seq_last:   u64   = null,
  snapshot_seq_last:  u64   = null,
  refdata:            <reference-data supplement state>,
  instruments:        map<instrument_id, instrument_state>,
  delta_buffer:       ordered list of (mktdata_seq, delta_message)
}

instrument_state = {
  status: "awaiting-refdata" | "awaiting-snapshot" | "building-snapshot" | "ready" | "gap",
  book: { bids: sorted map<price, level_state> descending by price,
          asks: sorted map<price, level_state> ascending by price },
  depth_bound:                 null | u32 = null,   // null = unknown; 0 = complete book;
                                                    // N = bounded at N levels per side
  last_applied_mktdata_seq:    u64 = null,
  last_applied_instrument_seq: u32 = null,
  required_anchor_seq:         u64 = null,   // set by InstrumentReset; snapshots older than
                                             // this MUST be discarded. Cleared when any
                                             // snapshot at or after it completes.
  open_snapshot:               null | { snapshot_id, anchor_seq, received_levels, total_levels,
                                        last_instrument_seq, depth_bound }
}

level_state = { quantity: u64, order_count: u16, level_flags: u8 }
```

The per-side containers MUST be ordered by price, because rank is derived from the subscriber's own book rather than carried authoritatively on the wire. Bids order descending and asks ascending, so that the first element of each is the inside market.

### Frame Parsing

Before any of the state machine below runs, for each received frame:

1. Validate `Magic` against this feed's value and discard non-matching frames.
2. Validate `Schema Version` and discard frames whose version is not implemented.
3. Walk the frame's application messages using each `Message Length`, skipping unknown Type IDs by length, subject to the bounds checks in [Application Message Header](#application-message-header-4-bytes).

### Cold Start

A subscriber bootstrapping from scratch MUST:

1. Bind all three ports. On the first frame received from any port, record `reset_count` from the frame header and initialise the per-port `seq_last` trackers.
2. Build reference-data state per the [Reference Data Distribution supplement](../reference-data/spec.md). As each `InstrumentDefinition` arrives under the current `Manifest Seq`, the corresponding `instrument_state` moves from `awaiting-refdata` to `awaiting-snapshot`. Instruments not yet in the manifest are ignored.
3. Buffer every `mktdata` delta message (`LevelUpdate`, `BookClear`, `InstrumentReset`) tagged with its frame `mktdata_seq`. Discard deltas for instruments not present in the current manifest.
4. On receipt of `SnapshotBegin(I, anchor_seq=S, snapshot_id=N, total_levels=T, last_instrument_seq=K, depth_bound=D)`:
   - If `I.status == "ready"`, see [Snapshot while ready](#snapshot-while-ready); the rest of step 4 does not apply.
   - If `I.required_anchor_seq` is set and `S < I.required_anchor_seq`, discard this snapshot and keep awaiting; see [Instrument Reset](#instrument-reset). Otherwise clear `I.required_anchor_seq` when this snapshot completes at step 6.
   - Otherwise (`I` is `awaiting-snapshot`, `gap`, or `building-snapshot`):
     - If `I` was already in `building-snapshot` (a previous snapshot for `I` was in flight), discard the partial book from the previous snapshot.
     - Move `I` to `building-snapshot`.
     - Set `I.open_snapshot = {snapshot_id: N, anchor_seq: S, received_levels: 0, total_levels: T, last_instrument_seq: K, depth_bound: D}`.
5. On receipt of `SnapshotLevel(snapshot_id=N, ...)` for `I`:
   - If `I.open_snapshot.snapshot_id != N`, discard.
   - Otherwise insert the level into `I.book` on the side given by its `Side` field, keyed by its `Price`; increment `I.open_snapshot.received_levels`.
6. On receipt of `SnapshotEnd(I, anchor_seq=S, snapshot_id=N)`:
   - If `N` or `S` does not match `I.open_snapshot`, or `received_levels != total_levels`, discard the partial book and revert `I` to `awaiting-snapshot`.
   - Otherwise:
     - Set `I.last_applied_mktdata_seq = S`, `I.last_applied_instrument_seq = I.open_snapshot.last_instrument_seq`, and `I.depth_bound = I.open_snapshot.depth_bound`.
     - Discard buffered deltas for `I` with `mktdata_seq ≤ S`.
     - Replay buffered deltas for `I` with `mktdata_seq > S` in ascending `mktdata_seq` order, applying the same classification as [Steady State](#steady-state): apply when `Per-Instrument Seq == last_applied + 1`, discard silently when `≤ last_applied` (a duplicated frame during bootstrap must not cost a re-bootstrap), and on a genuine forward gap discard the book and revert `I` to `awaiting-snapshot`. After each successful apply, advance `I.last_applied_mktdata_seq` and `I.last_applied_instrument_seq` to the values carried by the applied delta (the same tracker update as Steady State).
     - Mark `I` as `ready`.
7. Once every instrument in the current manifest is `ready`, the channel is fully bootstrapped. Application-level readiness policies — whether individual `ready` instruments may be consumed before channel-wide readiness — are not specified by this document.

#### Snapshot while ready

A subscriber with `I` in `ready` status can receive a periodic round-robin snapshot for `I` even though its delta stream has been continuous. Two cases:

**The discriminator is `Last Instrument Seq`, not `Anchor Seq`.** Let `K` be the snapshot's `Last Instrument Seq`:

- If `K > I.last_applied_instrument_seq`, the subscriber is genuinely behind: the snapshot was captured after deltas this subscriber never applied. It MUST re-bootstrap `I` by processing the current `SnapshotBegin` as if `I.status` were `awaiting-snapshot` (the short-circuit at the top of [Cold Start](#cold-start) step 4 does not apply) and then continuing with steps 5–6 on the subsequent `SnapshotLevel` and `SnapshotEnd` messages. The resulting book replaces the current one.
- If `K ≤ I.last_applied_instrument_seq`, the subscriber is current or has advanced past the snapshot, which is the ordinary case — deltas routinely arrive between the publisher's capture and the snapshot's delivery. The snapshot MAY be ignored, or — when `K` equals the tracker exactly — MAY be compared directly against the current book as a consistency check. Reconstructing an earlier state by rewinding is not available: a `Quantity = 0` delete carries no pre-image, so rewinding would require journaling every level's prior quantity. The spec does not mandate consistency checking.

`Anchor Seq` MUST NOT be used for this comparison. It is a **channel-wide** `mktdata` sequence, while `last_applied_mktdata_seq[I]` advances only on `I`'s own deltas — every frame for every other instrument, and every `Heartbeat`, moves one and not the other. Comparing them makes `anchor_seq > last_applied_mktdata_seq[I]` true for nearly every instrument on nearly every cycle, so a subscriber would discard and rebuild a perfectly good book for every instrument on every rotation, and the "subscriber is behind" branch would never be false. `Anchor Seq` remains the composition point for buffered-delta replay ([Snapshot Anchor Seq](#snapshot-anchor-seq)); the per-instrument counter is what decides whether an instrument is stale.

### Steady State

For each live `mktdata` delta arriving for instrument `I`:

- If `I.status == "ready"` and the delta's `Per-Instrument Seq == I.last_applied_instrument_seq + 1`, apply the delta to the book per [Absolute Apply Semantics](#absolute-apply-semantics) (for `LevelUpdate`) or the clear rules of [0x41 BookClear](#0x41-bookclear-36-bytes); update `I.last_applied_mktdata_seq` and `I.last_applied_instrument_seq`; then apply [Crossed-Book Monitoring](#crossed-book-monitoring).
- If `Per-Instrument Seq > I.last_applied_instrument_seq + 1`, a per-instrument gap has occurred on `I`. Mark `I` as `gap`, buffer further deltas for `I`, and await the next `SnapshotBegin` for `I` (or an `InstrumentReset` followed by a recovery snapshot).
- If `Per-Instrument Seq ≤ I.last_applied_instrument_seq`, the message is a duplicate or late; discard.

`InstrumentReset` carries no `Per-Instrument Seq` and is outside this series; it is processed per [Instrument Reset](#instrument-reset) regardless of sequence state.

On a `mktdata` channel-level seq gap (detected via the frame-header `Sequence Number`), the subscriber need not proactively mark any instrument as `gap`. The per-instrument seq check on the next arrival for each instrument is what reveals which instruments were in the lost frame. A subscriber MAY mark all instruments at-risk and require per-instrument seq continuity on the next delta before trusting the instrument again; the spec neither requires nor forbids this.

### Crossed-Book Monitoring

Because this feed's deltas compose into long-lived state, a subscriber can hold a book that is wrong without any sequence gap having been observed — from a publisher defect, or from loss the per-instrument sequence did not localise. A crossed inside market is cheap evidence of that condition: one comparison against the front of each side.

A subscriber SHOULD compare the inside market at each consistency point and increment a counter when it is crossed:

```
if bids is non-empty and asks is non-empty and bids.first().price > asks.first().price:
    increment crossed_book counter for I and surface it
```

**On a channel where `BatchBoundary` is observed, the consistency point is the boundary**, and the comparison runs on its receipt across the instruments touched since the previous boundary. On a channel with no boundaries, every delta is a consistency point and the comparison runs after each applied delta. `BatchBoundary` carries no `Instrument ID` and applies to the whole channel.

**This is observability, not control flow.** It MUST NOT change the instrument's status, discard the book, or trigger a re-bootstrap. An instrument holding corrupt state is repaired by the next round-robin snapshot for it, on exactly the schedule it would have been repaired anyway; reacting to a cross buys earlier knowledge, not faster recovery. A subscriber MAY withhold or annotate a crossed book for its own consumers, which is a local policy decision the spec does not constrain.

Two limits are worth stating plainly, so the counter is not mistaken for an integrity guarantee. It detects only corruption that happens to invert the inside market: a missed delete deep in the book crosses nothing and remains undetected until the next snapshot. And a publisher whose upstream applies changes atomically but which omits `BatchBoundary` produces transient crosses a subscriber cannot distinguish from defects, which is why [Publisher Behavior](#delta-stream) requires the boundary in that case rather than merely recommending it.

Evaluating only at boundaries is what makes the counter meaningful on a batching channel: intermediate states within a batch are explicitly not consistency points, and a transient cross there is legal rather than a defect.

A locked book (`bids.first().price == asks.first().price`) is routine on some venues, so the comparison above is strict `>` and does not count locking as crossed. A subscriber on a venue that never locks MAY use `>=` instead.

### Delta Buffer Sizing

`delta_buffer` holds deltas for instruments that are not yet `ready` — every instrument at cold start, and any instrument in `gap` — until the next snapshot for each arrives. Its worst case is therefore one full snapshot cycle of channel traffic, which this feed sizes larger than its siblings do: [Wire Efficiency and Bandwidth](#wire-efficiency-and-bandwidth) recommends stretching `T` for deep books, and buffer footprint scales with it. At the figures used there — 500 instruments, ~1,000 level updates/s, `T = 60 s` — a cold start can accumulate on the order of 30 M messages, roughly 1.4 GB at 48 bytes each. Operators tuning `T` upward for bandwidth are also tuning subscriber memory upward, and the two knobs are the same knob.

A subscriber MUST therefore bound the buffer, by message count or by bytes, and MUST define an overflow policy. The recommended policy is to drop the buffered deltas for the instrument holding the most buffered data, mark that instrument `gap`, and continue; it recovers on its next snapshot exactly as any other `gap` instrument does. A subscriber MUST NOT let buffer growth take down the channel, and MUST count overflow events — sustained overflow means `T` is too long for the deployment's memory budget, and that is a tuning signal an operator needs.

### Gap Recovery

An instrument in `gap` status is recovered by the next `SnapshotBegin` for it, via the same flow as [Cold Start](#cold-start) steps 4–6. Worst-case recovery time equals one snapshot cycle period. The spec provides no in-band mechanism to request an expedited snapshot.

### Instrument Reset

On receipt of `InstrumentReset(I, new_anchor_seq=S', reason=R)`:
1. Discard `I`'s level state and any open snapshot for `I`.
2. Discard buffered deltas for `I` with `mktdata_seq ≤ S'`.
3. Mark `I` as `awaiting-snapshot`, recording `S'` as the required anchor. While an instrument has a required anchor, the subscriber **MUST discard any `SnapshotBegin` for `I` whose `Anchor Seq` is older than `S'`** and continue awaiting. Without this, a snapshot captured before the reset but delivered after it — the two travel on separate ports, so the skew is ordinary — is accepted, and because the publisher pauses deltas for `I` until the recovery snapshot is emitted, the subsequent replay is sequence-continuous and passes every other check. The instrument would end `ready` holding exactly the diverged book the reset existed to discard, with no gap and no counter. The required anchor is cleared once **any** accepted snapshot for `I` completes — that is, any whose `Anchor Seq` is `S'` or newer — not only one matching `S'` exactly. The publisher is required to emit a snapshot at exactly `S'`, but that snapshot can itself be lost, in which case the next round-robin snapshot carries `Anchor Seq > S'` and is a perfectly good recovery. Clearing only on an exact match would leave the required anchor set permanently in that case.
4. Continue buffering deltas for `I` with `mktdata_seq > S'` until the recovery snapshot arrives, then apply per cold-start steps 4–6.

### Channel Reset

On `Reset Count` change observed on any port, the subscriber MUST discard all channel state — reference data, instruments, delta buffer, sequence trackers — and restart from the [Cold Start](#cold-start) procedure.

### Manifest Seq Change

Handled per the [Reference Data Distribution supplement](../reference-data/spec.md). When `Manifest Seq` bumps on the `refdata` port:
- Reference-data state is reinitialised per the supplement.
- `instrument_state` entries for instruments that are no longer in the manifest are discarded.
- New instruments enter `awaiting-snapshot` and are bootstrapped on the next snapshot cycle.
- Existing `ready` instruments that remain in the manifest retain their state.

---

## Publisher Behavior

### Delta Stream

A publisher operating the `mktdata` port MUST:

1. Emit every book-affecting event as a `LevelUpdate` or a `BookClear`.
2. Set `Quantity` on every `LevelUpdate` to the absolute aggregate resting quantity at that price after the change, never to a signed difference, and to `0` when the level is removed.
3. On each delta for instrument `I`, set `Per-Instrument Seq` to exactly one greater than the last `Per-Instrument Seq` emitted for `I` in the current `Reset Count` era. `Per-Instrument Seq` starts at 1 after each `Reset Count` change and is NOT reset at snapshot boundaries.
4. Set `Action` to reflect the publisher's own view of the change — `New` where no quantity previously rested at that price, `Change` where some did, `Delete` where the level is being removed, `Unknown` where the upstream does not distinguish them — accepting that subscribers apply by `Quantity` regardless. An inaccurate `Action` is a defect that will surface in subscriber divergence counters.
5. Set `Level Index` to the level's rank at emission where the publisher can determine it, and to `0xFFFF` otherwise. A publisher MUST NOT re-emit unchanged levels solely to correct their rank after an insertion.
6. Not emit a settled crossed book: where the publisher's own state shows `best_bid ≥ best_ask` outside a batch, it MUST resolve the condition before emitting rather than propagate it.
7. Pack multiple messages into a single frame where the total does not exceed the MTU.
8. Emit `Heartbeat` every N seconds when the `mktdata` path is otherwise idle, where N is operator-defined (recommended 1 s).
9. Emit `Trade` on `mktdata` when the upstream has a venue-level trade concept. `Trade` is not required for subscribers to reconstruct book state.
10. Emit `BatchBoundary` on `mktdata` where the upstream applies multiple level changes atomically. This is a MUST for such publishers, not a recommendation: without the boundary a subscriber cannot distinguish a legal intermediate state from a corrupt book, and the transient crosses it observes are indistinguishable from defects. A publisher whose upstream streams changes one at a time MAY omit it entirely.

### Snapshot Stream

A publisher operating the `snapshot` port MUST:

1. Maintain an ordered list of active instruments (matching the manifest on `refdata`) and emit snapshots round-robin across them.
2. For each instrument `I` in the rotation:
   - Capture the current level state of `I` and the current `mktdata` `Sequence Number` atomically — the publisher MUST NOT allow new deltas to apply to `I`'s book state while reading it for a snapshot.
   - Increment the local `Snapshot ID` counter for `I`.
   - Emit `SnapshotBegin(I, anchor_seq=S, snapshot_id=N, total_levels=T, last_instrument_seq=K, depth_bound=D, timestamp=now)` on the `snapshot` port, where `K` is the most recent `Per-Instrument Seq` emitted for `I` at or before `anchor_seq`.
   - Emit `T` `SnapshotLevel` messages, packed into frames.
   - Emit `SnapshotEnd(I, S, N)`.
3. NOT interleave two snapshot groups for different instruments within a channel. All frames on the `snapshot` port carrying levels for one instrument on a given `Channel ID` MUST precede the first frame carrying levels for another instrument on that channel.
4. Complete one full round-robin cycle (one snapshot per active instrument) within the configured **snapshot cycle period**.
5. Include an instrument with an empty book at capture time as `SnapshotBegin(total_levels=0) → SnapshotEnd`, with no intervening `SnapshotLevel` messages. An empty book is a valid snapshot.
6. Set application-header `Flags` bit 0 on every message emitted on this port, and clear it on every `mktdata` and `refdata` message.
7. Set `Depth Bound` to `0` where it carries the complete book, and otherwise to the number of levels per side it does carry. A publisher declaring a non-zero `Depth Bound` MUST NOT emit `Quantity = 0` for a level that merely fell out of its depth window: `Quantity = 0` asserts the level is empty, whereas a level pushed past the window is unknown. It emits nothing for such a level, leaving the subscriber to classify anything at or beyond rank `Depth Bound` as unknown by counting from the inside market.

Because a snapshot enumerates every level rather than every order, its size scales with book width rather than order count, and the two diverge sharply between venues. The snapshot cycle period is therefore an operator parameter that MUST be tuned to the channel's aggregate level count rather than copied from a sibling feed; see [Wire Efficiency and Bandwidth](#wire-efficiency-and-bandwidth). It is not advertised in-band in this version of the spec. Subscribers MUST NOT assume a specific cycle period; worst-case gap recovery time is whatever the deployment operates at.

### Inconsistency Detection and Per-Instrument Reset

If the publisher detects that its internal book state has diverged from the upstream source for one or more instruments, it MUST:

1. Emit `InstrumentReset(I, new_anchor_seq=S', reason=R)` on `mktdata`, where `S'` is the `mktdata` `Sequence Number` of the frame carrying this `InstrumentReset` message (i.e., the reset takes effect immediately; no delta with `mktdata_seq ≤ S'` for `I` applies to the post-reset state). The `InstrumentReset` message itself lives on the frame with seq `S'`; the subscriber's `discard deltas with mktdata_seq ≤ S'` rule therefore discards it from any replay buffer after the reset semantic has been captured, which is the intended behaviour.
2. Pause emission of further deltas for `I` until an out-of-cycle snapshot for `I` with `Anchor Seq = S'` has been emitted.
3. Emit the recovery snapshot for `I` on the `snapshot` port. If another snapshot for a different instrument is currently in flight on the `snapshot` port, the publisher MUST let it complete before beginning the recovery snapshot.
4. Resume delta emission for `I` after `SnapshotEnd` is emitted.

For channel-wide inconsistency (not localised to one instrument), the publisher MUST bump `Reset Count` and restart the session rather than emit many `InstrumentReset` messages.

Publishers whose upstream provides no retransmission or replay carry the whole recovery burden themselves: an upstream gap is not recoverable from the source, so the publisher MUST detect it, re-read the upstream book, and issue `InstrumentReset` for the affected instruments. `InstrumentReset` with `Reason = 3` (UpstreamGap) exists for exactly this case.

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0 on each of the three ports.
2. Begins emitting `InstrumentDefinition` on the `refdata` port, paced evenly across the definition cycle period (recommended 30 s per the [Reference Data Distribution supplement](../reference-data/spec.md)).
3. Begins emitting `ManifestSummary` with `Valid = 1` on the `refdata` port at the manifest cadence (recommended 1 s).
4. Begins emitting `SnapshotBegin` / `SnapshotLevel` / `SnapshotEnd` on the `snapshot` port, round-robin across active instruments, at the configured snapshot cycle period.
5. Begins emitting `LevelUpdate`, `BookClear`, `Trade`, and (optionally) `BatchBoundary` on the `mktdata` port as venue events arrive. Emits `Heartbeat` on `mktdata` when idle.
6. When the published instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, emits an updated `ManifestSummary`, and ensures the next snapshot cycle includes the new set.
7. On shutdown → emits `EndOfSession` on the `mktdata` port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency and Bandwidth

Per-message wire costs:

| Message | Size | Per-frame packing |
|---------|-----:|-------------------|
| LevelUpdate | 48 B | ~25 per 1,232-byte frame |
| BookClear | 36 B | ~33 per frame |
| BatchBoundary | 16 B | ~75 per frame |
| InstrumentReset | 28 B | ~43 per frame |
| Trade | 52 B | ~23 per frame |
| SnapshotBegin | 40 B | Negligible |
| SnapshotLevel | 32 B | 37 per frame |
| SnapshotEnd | 20 B | Negligible |

### Snapshot Stream

For a channel with `N` active instruments, total price-level count `L` across those instruments, snapshot cycle period `T` seconds:

- Cycle **payload** volume ≈ `L × 32 B + N × (40 B + 20 B)` (levels plus SnapshotBegin/SnapshotEnd overhead per instrument).
- Continuous snapshot payload bandwidth ≈ cycle payload volume / `T`, before per-datagram overhead.

Worked example at moderate scale (`N = 500`, `L = 250,000`, `T = 15 s`):

- Cycle payload volume ≈ `250,000 × 32 + 500 × 60 ≈ 8.0 MB`.
- Continuous payload bandwidth ≈ `8.0 MB / 15 s ≈ 535 KB/s ≈ 4.3 Mbps`, or about `4.5 Mbps` on the wire.

**Every figure in this section is application payload only.** A full frame carries 37 `SnapshotLevel` messages — 1,184 bytes — under a 24-byte frame header, and each datagram then adds IP and UDP headers plus the GRE encapsulation the 1,232-byte MTU exists to accommodate. That is roughly 76 bytes of per-datagram overhead against 1,184 bytes of payload, about **6%**. Apply that factor when planning capacity; it matters most where the numbers are already large.

**`T` must be sized against `L`, not adopted from a sibling feed.** Aggregate level count is the dominant term and varies by more than an order of magnitude across venue types. The same 500-instrument channel carrying deep books with long dust tails (`L = 2,500,000`) costs `80 MB` per cycle — `42.7 Mbps` of payload at `T = 15 s` (about `45 Mbps` on the wire), and `10.7 Mbps` at `T = 60 s`. A channel of instruments on a coarse price grid, where tick size and price bounds cap a book at a few dozen levels, may run under `1 Mbps` at `T = 15 s`. Shorter cycle periods trade bandwidth for worst-case gap recovery time; longer cycle periods do the reverse. Channel sharding (splitting `L` across multiple channels) compounds favorably because per-channel cycle bandwidth falls and per-channel gap recovery time falls, at the cost of aggregate bandwidth going up modestly.

Instrument-level depth is an empirical property of the venue and its participants. `Tick Size` and `Price Bound` from `InstrumentDefinition` bound it structurally, but usefully so only for instruments on a bounded price domain; for unbounded instruments the structural ceiling is far above realised depth and should not be used for capacity planning.

### Delta Stream

Delta-stream bandwidth depends on venue activity and is not bounded by this spec. It also inherits whatever conflation the publisher's upstream applies: a publisher fed by a periodically-batched upstream emits at that cadence, while one fed by an event-per-change upstream emits every transition. For an actively-updating instrument at ~1,000 level updates per second, `LevelUpdate` traffic is approximately `48 KB/s ≈ 0.38 Mbps`.

Aggregation is what makes this feed cheaper than order-level distribution of the same book: multiple resting orders at one price collapse to a single level, so both the snapshot and the delta stream carry materially less than the equivalent market-by-order stream.

The format is fixed-size and binary; parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Relationship to Sibling Feeds

The DoubleZero Market-by-Price Feed is a sibling of the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Midpoint Feed](../midpoint/spec.md), and the [Market-by-Order Feed](../market-by-order/spec.md). Sibling feeds share:

- The 24-byte frame header layout (except for the `Magic` value).
- The 4-byte application message header.
- The [Reference Data Distribution supplement](../reference-data/spec.md) conformance, including `InstrumentDefinition` (0x02) and `ManifestSummary` (0x07).
- The cross-spec message Type IDs `0x01` (Heartbeat), `0x04` (Trade), `0x06` (EndOfSession), `0x08` (Liquidation) byte-for-byte.
- The session-lifecycle and `Reset Count` patterns.
- The forward-compatibility rules.

Distinctions of the market-by-price feed:
- `Magic` is `0x4442` (vs. `0x445A` top-of-book, `0x4444` market-by-order, `0x4D44` midpoint, `0x494F` order-intent, `0x4450` perp-stats).
- Three-port channel model (vs. two), shared with the market-by-order feed.
- New market-by-price payloads occupy `0x40`–`0x4F`, with `0x50`–`0x5F` reserved. `BatchBoundary` (`0x13`), `InstrumentReset` (`0x14`), `SnapshotBegin` (`0x20`) and `SnapshotEnd` (`0x22`) are shared with the market-by-order feed at its Type IDs rather than renumbered, because they are the same payload; `SnapshotBegin` is a prefix-superset with `Depth Bound` appended at offset 36.
- `Per-Instrument Seq` is a `u32` at offset 12, identical in type, placement, and comparison semantics to the market-by-order feed's.

Divergences that change subscriber or publisher code, beyond the message set itself:
- `BatchBoundary` is required of batching publishers here, where the market-by-order feed makes it optional.
- [Crossed-Book Monitoring](#crossed-book-monitoring) has no market-by-order counterpart.
- `Depth Bound` and its publisher obligations have no market-by-order counterpart.

The relationship to the market-by-order feed is one of projection, not layering: this feed carries the price-aggregated view of the same book that feed carries order-by-order. A consumer needing order identity, queue position, or per-order lifecycle uses market-by-order; a consumer needing aggregate depth uses this feed at materially lower cost. A publisher MAY operate any subset of the sibling feeds for the same instruments simultaneously. Subscribers MAY consume any subset independently.

---

## Versioning and Forward Compatibility

This document is version **3.0.0**, versioned independently of the sibling specs. The Schema Version byte in the frame header is `3` and equals this spec's MAJOR version, so it stays `3` for every `3.x.y` release and changes only on a breaking wire change. See the [Versioning Policy](../VERSIONING.md) for the full rule, the change classification, and the tag scheme.

Future `3.x` versions of this specification MAY, without a Schema Version bump:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Update Reason, Clear Reason, Reset Reason, and Level Flags. Decoders MUST accept any `u8` value.
- Define a positional-index addressing mode in the reserved `0x50`–`0x5F` range, for venues whose upstream identifies levels by rank rather than price. Such a mode would add message types rather than reinterpret the ones defined here, so existing subscribers skip it by `Message Length` and no addressing-mode negotiation is introduced. It is not defined in this version and MUST NOT be added speculatively.
- Promote `Trade` to a shared cross-spec supplement. This is editorial if the layout is unchanged, but requires a coordinated release of this spec and the top-of-book feed.

Existing field layouts and semantics will not change within the `3.x` line. A change that moves or resizes a field, alters a message length, or redefines existing semantics requires a MAJOR release and a Schema Version bump, which old decoders MUST reject rather than parse.

### Changes

**3.0.0** — added `Source ID` (`u16`) after `Instrument ID` in `InstrumentDefinition`. `Symbol` and every later field move two bytes, and the message grows from 128 to 130 bytes. This is a breaking change: the Schema Version byte is now `3`, and a decoder built for `2.x` MUST reject these frames rather than parse them at the old offsets. The midpoint feed remains unchanged at Schema Version `1`.

**2.0.0** — widened the `InstrumentDefinition` `Symbol` field from `char[16]` to `char[64]`. Every field after `Symbol` moves and the message grows from 80 to 128 bytes, so this is a breaking change: the Schema Version byte is now `2`, and a decoder built for `1.x` MUST reject these frames rather than parse them at the old offsets. Nothing else on the wire changed. The midpoint feed keeps its 64-byte variant and stays at Schema Version `1`.

**1.0.0** — first stable release. Promoted from draft with no wire change; Schema Version was `1` before and after.
