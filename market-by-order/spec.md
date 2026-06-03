# DoubleZero Market-by-Order Feed

The DoubleZero Market-by-Order Feed is a wire format for market-by-order (MBO) book data delivered over the DoubleZero Edge service. It defines a fixed-size, multicast-native binary protocol for publishing individual resting orders and their lifecycle events as a continuous snapshot + delta stream, from any venue with an order book that exposes order-level state to the publisher.

This is a sibling protocol to the DoubleZero Top-of-Book & Trades Feed and the DoubleZero Midpoint Feed, not a layer on top. Where the top-of-book feed carries two-sided BBO data and trades and the midpoint feed carries a single derived price per instrument, this feed carries the full resting-order population of each instrument, plus a continuous in-band snapshot mechanism that lets subscribers bootstrap and recover from packet loss over multicast alone.

This document specifies the frame header, application message header, the message types sufficient to operate a working publisher and subscriber, and the sequence-number-anchored snapshot/delta recovery model that is the core of the design.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups. Simple to parse in any language.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram. The protocol defines application messages only; transport, addressing, and group membership are out of scope.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price-carrying message carries a `u16` source ID. With a single publisher this is redundant; with many it is essential.
7. **Domain-agnostic.** Anything with a two-sided book of resting limit orders — crypto spot, equities, futures, FX, prediction markets — is a valid instrument.
8. **In-band recovery only.** Subscribers bootstrap and recover from packet loss via a continuous publisher-driven snapshot stream. No TCP replay, no out-of-band snapshot service, no subscriber-initiated requests.
9. **Recovery blast radius minimised.** A single lost multicast packet invalidates only the specific instruments whose deltas were in the lost frame, not the whole channel. A per-instrument sequence number carried on every delta message lets subscribers localise the loss.

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
| mktdata | `OrderAdd`, `OrderCancel`, `OrderExecute`, `Trade`, `BatchBoundary`, `InstrumentReset`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |
| snapshot | `SnapshotBegin`, `SnapshotOrder`, `SnapshotEnd` |

The frame header and application message header are identical on all three ports. A single decoder implementation handles all three. Concrete port assignments are out of scope for this spec; each deployment publishes its port mapping out of band.

A subscriber bootstrapping from a cold start MUST bind all three ports. A subscriber with an out-of-band snapshot mechanism (e.g., proprietary replay or a historical database) MAY bind only `mktdata` + `refdata` and skip `snapshot`; in that case it forfeits the in-band recovery mechanism.

The snapshot stream has a fundamentally different traffic shape from the delta stream — continuous and steady versus bursty and event-driven — and separating them lets subscribers opt out, lets operators rate-limit the two streams independently at the network layer, and keeps per-port sequence-number diagnostics clean.

### Frame Header (24 bytes)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     Magic (0x4444)            |  Schema Ver   |  Channel ID   |
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
| 0 | Magic | `u16` | `0x4444`. Frame delimiter. Distinct from the top-of-book feed's `0x445A` and the midpoint feed's `0x4D44` to prevent cross-protocol misrouting. |
| 2 | Schema Version | `u8` | Protocol version. Starts at `1`. |
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

---

## Identity Model

### Instrument IDs

The unique key for an instrument in this feed is the tuple **`(channel_id, instrument_id)`**. `instrument_id` is a `u32` scoped to its channel; it need not be globally unique across channels. Subscribers consuming multiple channels MUST key their internal instrument map by the tuple.

Operators running multiple publisher instances that share a single `source_id` (as defined in the [Source ID Registry](../sources/spec.md)) MAY assign globally unique `instrument_id`s as an operational convenience, in which case the `channel_id` component of the key becomes informational only. The spec does not require this.

### Channel Sharding

Sharding the active instrument set across multiple publisher instances — each on its own channel — is supported natively via `Channel ID` in the frame header. Each channel is an independent state machine with its own `Reset Count`, `Sequence Number` series per port, `Manifest Seq`, and snapshot cycle. Grouping criteria (by asset class, by liquidity tier, by source venue) and discovery mechanisms are deployment-level concerns and out of scope for this spec.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal. Inherited; identical to siblings. |
| `0x02` | InstrumentDefinition | 80 | refdata | Reference data for an instrument. Inherited from the top-of-book feed. |
| `0x03` | *(reserved)* | — | — | Quote in the top-of-book feed, Midpoint in the midpoint feed. Intentionally unused here to prevent accidental cross-decoding if a frame is misrouted. |
| `0x04` | Trade | 52 | mktdata | Venue-level trade summary. **Identical byte-for-byte to the top-of-book feed's Trade**, carried here as a convenience for consumers who want a trade-tape view without re-aggregating from `OrderExecute`. |
| `0x05` | *(reserved)* | — | — | |
| `0x06` | EndOfSession | 12 | mktdata | Inherited. No more data for this session. |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary. Inherited; see the [Reference Data Distribution supplement](../reference-data/spec.md). |
| `0x10` | OrderAdd | 52 | mktdata | A new resting order entered the book. |
| `0x11` | OrderCancel | 32 | mktdata | An order left the book without further execution. |
| `0x12` | OrderExecute | 56 | mktdata | A resting order was hit (partially or fully) by an aggressor. |
| `0x13` | BatchBoundary | 16 | mktdata | Optional atomic-batch delimiter. |
| `0x14` | InstrumentReset | 28 | mktdata | Per-instrument surgical resync signal. |
| `0x20` | SnapshotBegin | 36 | snapshot | Start of a per-instrument snapshot group. |
| `0x21` | SnapshotOrder | 44 | snapshot | One resting order in a snapshot. |
| `0x22` | SnapshotEnd | 20 | snapshot | End of a per-instrument snapshot group. |

A decoder encountering an unknown type MUST skip the message using its `Message Length` field and continue parsing the frame.

### Cross-Spec Type ID Policy

A message Type ID that appears in more than one sibling feed MUST carry the same semantic meaning in each. The shared Type IDs at this writing are `0x01` (Heartbeat), `0x02` (InstrumentDefinition), `0x04` (Trade), `0x06` (EndOfSession), and `0x07` (ManifestSummary). Heartbeat, EndOfSession, and ManifestSummary are byte-for-byte identical across every sibling that carries them. Trade is byte-for-byte identical between the top-of-book feed and this feed (the midpoint feed leaves `0x04` reserved). InstrumentDefinition shares the Type ID but each sibling defines its own layout — market-by-order and top-of-book share the 80-byte layout; the midpoint feed carries a slimmed 64-byte variant. Feed-specific payloads live in `0x10` and above. A Type ID used by one sibling for a given payload MUST NOT be reassigned to a different payload in another sibling; where a sibling does not carry that payload, the slot is reserved.

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

### 0x02 InstrumentDefinition (80 bytes)

Inherited from the top-of-book feed verbatim. Reproduced in full below for standalone readability.

Maps a numeric Instrument ID to human-readable metadata. Carried on the `refdata` port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x02`, Length=80 |
| 4 | Instrument ID | `u32` | Unique numeric ID for this instrument |
| 8 | Symbol | `char[16]` | Human-readable label. Truncate if needed (e.g., `"BTC-USDT"`). |
| 24 | Leg1 | `char[8]` | First leg/component. Context-dependent: base currency, underlying, outcome name. |
| 32 | Leg2 | `char[8]` | Second leg/component. Context-dependent: quote/settlement currency. |
| 40 | Asset Class | `u8` | See Asset Class table. |
| 41 | Price Exponent | `i8` | Implied decimal exponent for price fields. e.g., `-2` means divide raw value by 100. |
| 42 | Qty Exponent | `i8` | Implied decimal exponent for quantity fields. |
| 43 | Market Model | `u8` | See Market Model table. |
| 44 | Tick Size | `price` | Minimum price increment (interpreted via Price Exponent). |
| 52 | Lot Size | `qty` | Minimum quantity increment (interpreted via Qty Exponent). |
| 60 | Contract Value | `u64` | Notional per contract. 0 if not applicable (e.g., spot). |
| 68 | Expiry | `ts_ns` | Expiration timestamp. 0 for non-expiring. |
| 76 | Settle Type | `u8` | 0=N/A, 1=Cash, 2=Physical |
| 77 | Price Bound | `u8` | 0=Unbounded, 1=Bounded [0,1] (binary outcomes), 2=Non-negative only |
| 78 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. See supplement. |

#### Asset Class Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | Crypto Spot |
| 2 | Prediction Binary |
| 3 | Prediction Scalar |
| 4 | Prediction Categorical |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

#### Market Model Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | CLOB |
| 2 | AMM |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

### 0x04 Trade (52 bytes)

Identical to the top-of-book feed's Trade message, byte-for-byte. Carried on the `mktdata` port as a venue-level summary of a single trade. For consumers that also need the per-resting-order impact of a trade, each `Trade` is accompanied by one or more `OrderExecute` messages sharing the same `Trade ID`.

A future shared supplement is anticipated to factor `Trade` out of both specs and carry it independently; until then the message is duplicated.

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

Inherited. Periodic summary of the active instrument set on this channel. Carried on the `refdata` port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); reproduced here for convenience.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x07`, Length=24 |
| 4  | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5  | Valid | `u8` | `1` when the channel has an established instrument set; `0` when the publisher is uninitialized or the channel is inactive. See supplement. |
| 6  | Reserved | 2B | Padding |
| 8  | Manifest Seq | `u16` | Increments every time the active instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the active set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

### 0x10 OrderAdd (52 bytes)

A new resting order entered the book.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x10)  |  Length (52)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |     Side      |  Order Flags  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Per-Instrument Seq (u32)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Order ID (u64)                          |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Enter Timestamp (ts_ns)                     |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Price (price, i64)                      |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Quantity (qty, u64)                      |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                          Reserved                             |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x10`, Length=52 |
| 4  | Instrument ID | `u32` | Instrument this order applies to |
| 8  | Source ID | `u16` | Originating source. |
| 10 | Side | `u8` | `0`=Bid/Buy, `1`=Ask/Sell |
| 11 | Order Flags | `u8` | See Order Flags table. |
| 12 | Per-Instrument Seq | `u32` | See [Per-Instrument Delta Sequence](#per-instrument-delta-sequence). |
| 16 | Order ID | `u64` | Venue-assigned order identifier. MUST be unique per `(channel_id, instrument_id)` within the current `Reset Count` era. |
| 24 | Enter Timestamp | `ts_ns` | Venue time when the order entered the book. Subscribers MAY use this for queue-position modelling. |
| 32 | Price | `price` | Order price. Uses instrument's Price Exponent. |
| 40 | Quantity | `qty` | Resting quantity at entry. For non-IOC/FOK orders this equals the original submitted quantity. For orders that partially-filled on entry and rested the remainder, this is the remainder (the fills are reported via `OrderExecute` with the same `Order ID`). |
| 48 | Reserved | 4B | Padding to 52 bytes. |

#### Order Flags

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | post-only | Order is post-only / add-liquidity-only (rejected on cross) |
| 1 | reduce-only | Order is reduce-only (will not increase position) |
| 2 | hidden | Order is hidden / iceberg; `Quantity` reports only the visible tip |
| 3 | stop | Order was triggered by a stop / trigger condition |
| 4 | twap-child | Order is a child of a TWAP or other scheduled parent |
| 5–7 | reserved | Publishers MUST set to 0; receivers MUST ignore |

Publishers SHOULD set the flags accurately based on venue metadata; receivers MUST treat unknown-bit-pattern flags as informational only and not use them to filter the book state.

### 0x11 OrderCancel (32 bytes)

A resting order left the book without further execution.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x11)  |  Length (32)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |    Reason     |   Reserved    |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Per-Instrument Seq (u32)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Order ID (u64)                          |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Timestamp (ts_ns)                        |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x11`, Length=32 |
| 4  | Instrument ID | `u32` | |
| 8  | Source ID | `u16` | |
| 10 | Reason | `u8` | See Cancel Reason table. |
| 11 | Reserved | `u8` | Padding |
| 12 | Per-Instrument Seq | `u32` | |
| 16 | Order ID | `u64` | Identifies the cancelled resting order |
| 24 | Timestamp | `ts_ns` | Venue time of cancellation |

A subscriber MAY receive `OrderCancel` for an Order ID it does not have in its book (e.g., after a per-instrument gap prior to the cancel, before recovery). Such cancels SHOULD be discarded silently.

#### Cancel Reason

| Value | Name | Meaning |
|-------|------|---------|
| 0 | Unknown | No reason available |
| 1 | UserCancel | Explicit user cancel action |
| 2 | VenueExpire | Venue-enforced expiration (e.g., post-only rejected on cross, TTL, IOC unfilled remainder) |
| 3 | SelfTrade | Cancelled by venue-side self-trade prevention |
| 4 | Margin | Cancelled by venue-side margin enforcement |
| 5 | RiskLimit | Cancelled by venue-side risk-limit enforcement (e.g., open-interest cap) |
| 6 | SiblingFilled | Cancelled because a linked child/sibling order filled |
| 255 | Other | Venue-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknowns as `0` (Unknown). New values MAY be defined in future schema versions without a Schema Version bump.

### 0x12 OrderExecute (56 bytes)

A single resting order was hit (partially or fully) by an aggressor. For a trade that hits N resting orders, the publisher emits N `OrderExecute` messages, all sharing the same `Trade ID`. A venue-level `Trade` summary MAY accompany them.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x12)  |  Length (56)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |Aggressor Side |  Exec Flags   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Per-Instrument Seq (u32)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Order ID (u64)                          |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Trade ID (u64)                          |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Timestamp (ts_ns)                        |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Exec Price (price, i64)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Exec Quantity (qty, u64)                   |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x12`, Length=56 |
| 4  | Instrument ID | `u32` | |
| 8  | Source ID | `u16` | |
| 10 | Aggressor Side | `u8` | `1`=Buy aggressor hit the ask; `2`=Sell aggressor hit the bid; `0`=Unknown |
| 11 | Exec Flags | `u8` | See Exec Flags table. |
| 12 | Per-Instrument Seq | `u32` | |
| 16 | Order ID | `u64` | The resting order impacted by this execution |
| 24 | Trade ID | `u64` | Matches the accompanying `Trade` message's Trade ID when grouped; `0` if this publisher does not group executions into trades |
| 32 | Timestamp | `ts_ns` | Venue time of the execution |
| 40 | Exec Price | `price` | Price at which the execution occurred. Not guaranteed to equal the resting order's price; some venue models allow price improvement. |
| 48 | Exec Quantity | `qty` | Quantity filled in this execution |

Subscribers maintaining book state MUST update the resting order's remaining quantity by subtracting `Exec Quantity`. The order is removed from the book if `Exec Flags` bit 0 is set. Subscribers MAY also remove the order when remaining quantity reaches zero even if the flag is not set, as a consistency guard.

#### Exec Flags

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | full-fill | The resting order is fully consumed by this execution and removed from the book |
| 1 | self-match | Aggressor and maker have the same account (venue allowed the cross; not self-trade-prevented) |
| 2–7 | reserved | Publishers MUST set to 0; receivers MUST ignore |

### 0x13 BatchBoundary (16 bytes)

Optional delimiter marking an atomic batch of updates. Publishers whose upstream has natural batch semantics (blockchain blocks, matching-engine rounds, exchange message-group boundaries) MAY emit this message after every batch of deltas. Publishers whose upstream streams continuously MAY omit it entirely. Subscribers MUST tolerate its absence.

Semantics: *all `mktdata` deltas arriving between the previous `BatchBoundary` (or the start of the channel) and this `BatchBoundary` apply atomically. Book states observed between the previous and this boundary are not guaranteed to be consistency points; the state at the boundary is.*

Subscribers with strict atomicity requirements MAY buffer deltas between boundaries and apply them as a group.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x13`, Length=16 |
| 4 | Batch ID | `u32` | Publisher-defined, monotonically increasing within the current `Reset Count` era. For blockchain sources, SHOULD be the block number truncated to `u32`. |
| 8 | Batch Time | `ts_ns` | Venue time of the batch |

`BatchBoundary` is informational; a subscriber that ignores it MUST still produce a correct book state.

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
1. Discard all resting-order state for instrument `I`, including any in-flight snapshot for `I`.
2. Discard any buffered deltas for `I` with `mktdata_seq ≤ S'`.
3. Mark `I` as awaiting a snapshot with `Anchor Seq == S'`.
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

### 0x20 SnapshotBegin (36 bytes)

Opens a per-instrument snapshot group on the `snapshot` port.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x20`, Length=36 |
| 4  | Instrument ID | `u32` | |
| 8  | Anchor Seq | `u64` | The `mktdata`-port `Sequence Number` at the moment the publisher captured the book state for this snapshot. See [Snapshot Anchor Seq](#snapshot-anchor-seq) for the precise semantics. |
| 16 | Total Orders | `u32` | Number of `SnapshotOrder` messages that will follow before the matching `SnapshotEnd`. MAY be `0` for an instrument with no resting orders at capture time. |
| 20 | Snapshot ID | `u32` | Monotonically increasing per `(channel_id, instrument_id)` within the current `Reset Count` era. Identifies this snapshot instance, so that subscribers can associate `SnapshotOrder` messages with the correct `SnapshotBegin` and detect stale or out-of-order snapshot fragments. |
| 24 | Last Instrument Seq | `u32` | The `Per-Instrument Seq` of the last `OrderAdd`/`OrderCancel`/`OrderExecute` that was applied to this instrument at or before `Anchor Seq`. Subscribers MUST initialise their `last_applied_instrument_seq` tracker to this value after applying the snapshot. `0` if no deltas have been applied for this instrument in the current `Reset Count` era. |
| 28 | Timestamp | `ts_ns` | Publisher wall-clock at capture. |

Publishers MUST NOT interleave two snapshot groups for different instruments on the `snapshot` port. A `SnapshotBegin` for instrument A is always followed by exactly `Total Orders` `SnapshotOrder` messages for A and then a `SnapshotEnd` for A, before any `SnapshotBegin` for a different instrument.

An instrument with no resting orders at capture time is represented by `SnapshotBegin(total_orders=0)` immediately followed by `SnapshotEnd` with no intervening `SnapshotOrder` messages.

### 0x21 SnapshotOrder (44 bytes)

One resting order in a snapshot. The Instrument ID is implied by the containing `SnapshotBegin`; it is not repeated on each order.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x21`, Length=44 |
| 4  | Snapshot ID | `u32` | MUST match the containing `SnapshotBegin`'s Snapshot ID. Subscribers MUST discard any `SnapshotOrder` whose `Snapshot ID` does not match the currently-open `SnapshotBegin`. |
| 8  | Order ID | `u64` | Venue-assigned order identifier |
| 16 | Side | `u8` | `0`=Bid, `1`=Ask |
| 17 | Order Flags | `u8` | Same semantics as `OrderAdd`'s Order Flags field |
| 18 | Reserved | 2B | Padding |
| 20 | Enter Timestamp | `ts_ns` | Venue time when the order entered the book |
| 28 | Price | `price` | |
| 36 | Quantity | `qty` | Remaining quantity at capture |

### 0x22 SnapshotEnd (20 bytes)

Closes a per-instrument snapshot group.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x22`, Length=20 |
| 4  | Instrument ID | `u32` | MUST match the opening `SnapshotBegin` |
| 8  | Anchor Seq | `u64` | MUST match the opening `SnapshotBegin` |
| 16 | Snapshot ID | `u32` | MUST match the opening `SnapshotBegin` |

If a subscriber receives a `SnapshotEnd` whose `Instrument ID`, `Anchor Seq`, or `Snapshot ID` does not match the currently-open `SnapshotBegin`, or whose number of intervening `SnapshotOrder` messages does not equal `Total Orders`, the subscriber MUST discard the partial book and await a fresh snapshot for the instrument.

---

## Sequence Numbers and Recovery

The market-by-order feed carries three independent sequence-number series plus a derived anchor, together defining the snapshot/delta composition rules.

### Per-Port Channel Sequence

Each of the three ports — `mktdata`, `refdata`, `snapshot` — carries its own `Sequence Number` in the frame header. The three series are independent of each other and all reset to 0 when the channel's `Reset Count` changes. Semantically:

- The `mktdata` channel seq detects gaps in the delta stream. Any gap invalidates the `mktdata`-path state only.
- The `refdata` channel seq detects gaps in the reference-data stream; gap handling is specified in the [Reference Data Distribution supplement](../reference-data/spec.md).
- The `snapshot` channel seq detects gaps within a snapshot group. A gap mid-snapshot invalidates that specific snapshot instance; the subscriber discards the partial book and awaits a fresh `SnapshotBegin` for the instrument.

### Per-Instrument Delta Sequence

`OrderAdd`, `OrderCancel`, and `OrderExecute` each carry a `u32` `Per-Instrument Seq`, monotonically increasing per `(channel_id, instrument_id)` within the current `Reset Count` era. The first delta for an instrument after a channel reset carries `Per-Instrument Seq = 1`; each subsequent delta for that instrument increments by exactly 1.

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
  book: { bids: map<order_id, RestingOrder>, asks: map<order_id, RestingOrder> },
  last_applied_mktdata_seq:    u64 = null,
  last_applied_instrument_seq: u32 = null,
  open_snapshot:               null | { snapshot_id, anchor_seq, received_orders, total_orders,
                                        last_instrument_seq }
}
```

### Cold Start

A subscriber bootstrapping from scratch MUST:

1. Bind all three ports. On the first frame received from any port, record `reset_count` from the frame header and initialise the per-port `seq_last` trackers.
2. Build reference-data state per the [Reference Data Distribution supplement](../reference-data/spec.md). As each `InstrumentDefinition` arrives under the current `Manifest Seq`, the corresponding `instrument_state` moves from `awaiting-refdata` to `awaiting-snapshot`. Instruments not yet in the manifest are ignored.
3. Buffer every `mktdata` delta message (`OrderAdd`, `OrderCancel`, `OrderExecute`, `InstrumentReset`) tagged with its frame `mktdata_seq`. Discard deltas for instruments not present in the current manifest.
4. On receipt of `SnapshotBegin(I, anchor_seq=S, snapshot_id=N, total_orders=T, last_instrument_seq=K)`:
   - If `I.status == "ready"`, see [Snapshot while ready](#snapshot-while-ready); the rest of step 4 does not apply.
   - Otherwise (`I` is `awaiting-snapshot`, `gap`, or `building-snapshot`):
     - If `I` was already in `building-snapshot` (a previous snapshot for `I` was in flight), discard the partial book from the previous snapshot.
     - Move `I` to `building-snapshot`.
     - Set `I.open_snapshot = {snapshot_id: N, anchor_seq: S, received_orders: 0, total_orders: T, last_instrument_seq: K}`.
5. On receipt of `SnapshotOrder(snapshot_id=N, ...)` for `I`:
   - If `I.open_snapshot.snapshot_id != N`, discard.
   - Otherwise insert the order into `I.book` on the correct side; increment `I.open_snapshot.received_orders`.
6. On receipt of `SnapshotEnd(I, anchor_seq=S, snapshot_id=N)`:
   - If `N` or `S` does not match `I.open_snapshot`, or `received_orders != total_orders`, discard the partial book and revert `I` to `awaiting-snapshot`.
   - Otherwise:
     - Set `I.last_applied_mktdata_seq = S` and `I.last_applied_instrument_seq = I.open_snapshot.last_instrument_seq`.
     - Discard buffered deltas for `I` with `mktdata_seq ≤ S`.
     - Replay buffered deltas for `I` with `mktdata_seq > S` in ascending `mktdata_seq` order. Each delta's `Per-Instrument Seq` MUST equal `I.last_applied_instrument_seq + 1` on replay; otherwise the subscriber MUST discard the book and revert `I` to `awaiting-snapshot`. After each successful apply, advance `I.last_applied_mktdata_seq` and `I.last_applied_instrument_seq` to the values carried by the applied delta (the same tracker update as Steady State).
     - Mark `I` as `ready`.
7. Once every instrument in the current manifest is `ready`, the channel is fully bootstrapped. Application-level readiness policies — whether individual `ready` instruments may be consumed before channel-wide readiness — are not specified by this document.

#### Snapshot while ready

A subscriber with `I` in `ready` status can receive a periodic round-robin snapshot for `I` even though its delta stream has been continuous. Two cases:

- If `anchor_seq > I.last_applied_mktdata_seq`, the subscriber is behind (possibly an undetected gap). The subscriber MUST re-bootstrap `I` by processing the current `SnapshotBegin` as if `I.status` were `awaiting-snapshot` (the short-circuit at the top of [Cold Start](#cold-start) step 4 does not apply) and then continuing with steps 5–6 on the subsequent `SnapshotOrder` and `SnapshotEnd` messages. The resulting book replaces the current one.
- If `anchor_seq ≤ I.last_applied_mktdata_seq`, the subscriber has advanced past the snapshot. The snapshot MAY be ignored, or MAY be used as a consistency check (reconstruct `I`'s book as of `anchor_seq` by rewinding applied deltas and compare). The spec does not mandate consistency checking.

### Steady State

For each live `mktdata` delta arriving for instrument `I`:

- If `I.status == "ready"` and the delta's `Per-Instrument Seq == I.last_applied_instrument_seq + 1`, apply the delta to the book; update `I.last_applied_mktdata_seq` and `I.last_applied_instrument_seq`.
- If `Per-Instrument Seq > I.last_applied_instrument_seq + 1`, a per-instrument gap has occurred on `I`. Mark `I` as `gap`, buffer further deltas for `I`, and await the next `SnapshotBegin` for `I` (or an `InstrumentReset` followed by a recovery snapshot).
- If `Per-Instrument Seq ≤ I.last_applied_instrument_seq`, the message is a duplicate or late; discard.

On a `mktdata` channel-level seq gap (detected via the frame-header `Sequence Number`), the subscriber need not proactively mark any instrument as `gap`. The per-instrument seq check on the next arrival for each instrument is what reveals which instruments were in the lost frame. A subscriber MAY mark all instruments at-risk and require per-instrument seq continuity on the next delta before trusting the instrument again; the spec neither requires nor forbids this.

### Gap Recovery

An instrument in `gap` status is recovered by the next `SnapshotBegin` for it, via the same flow as [Cold Start](#cold-start) steps 4–6. Worst-case recovery time equals one snapshot cycle period. The spec provides no in-band mechanism to request an expedited snapshot.

### Instrument Reset

On receipt of `InstrumentReset(I, new_anchor_seq=S', reason=R)`:
1. Discard `I`'s resting-order state and any open snapshot for `I`.
2. Discard buffered deltas for `I` with `mktdata_seq ≤ S'`.
3. Mark `I` as `awaiting-snapshot`, expecting the next snapshot for `I` to carry `Anchor Seq == S'`.
4. Continue buffering deltas for `I` with `mktdata_seq > S'` until the recovery snapshot arrives, then apply per cold-start steps 4–6.

### Channel Reset

On `Reset Count` change observed on any port, the subscriber MUST discard all channel state — reference data, instruments, delta buffer, sequence trackers — and restart from the [Cold Start](#cold-start) procedure.

### Manifest Seq Change

Handled per the [Reference Data Distribution supplement](../reference-data/spec.md). When `Manifest Seq` bumps on the `refdata` port:
- Reference-data state is reinitialised per the supplement.
- Market-by-order `instrument_state` entries for instruments that are no longer in the manifest are discarded.
- New instruments enter `awaiting-snapshot` and are bootstrapped on the next snapshot cycle.
- Existing `ready` instruments that remain in the manifest retain their state.

---

## Publisher Behavior

### Delta Stream

A publisher operating the `mktdata` port MUST:

1. Emit every book-affecting event as one of `OrderAdd`, `OrderCancel`, or `OrderExecute`.
2. On each such message for instrument `I`, set `Per-Instrument Seq` to exactly one greater than the last `Per-Instrument Seq` emitted for `I` in the current `Reset Count` era. `Per-Instrument Seq` starts at 1 after each `Reset Count` change and is NOT reset at snapshot boundaries.
3. Pack multiple messages into a single frame where the total does not exceed the MTU.
4. Emit `Heartbeat` every N seconds when the `mktdata` path is otherwise idle, where N is operator-defined (recommended 1 s).
5. Emit `Trade` on `mktdata` in addition to `OrderExecute` when the upstream has a venue-level trade concept. `Trade` is not strictly required for subscribers to reconstruct book state.
6. `BatchBoundary` MAY be emitted on `mktdata` when the upstream has natural batch semantics, as described in [0x13 BatchBoundary](#0x13-batchboundary-16-bytes).

### Snapshot Stream

A publisher operating the `snapshot` port MUST:

1. Maintain an ordered list of active instruments (matching the manifest on `refdata`) and emit snapshots round-robin across them.
2. For each instrument `I` in the rotation:
   - Capture the current resting-order state of `I` and the current `mktdata` `Sequence Number` atomically — the publisher MUST NOT allow new deltas to apply to `I`'s book state while reading it for a snapshot.
   - Increment the local `Snapshot ID` counter for `I`.
   - Emit `SnapshotBegin(I, anchor_seq=S, snapshot_id=N, total_orders=T, last_instrument_seq=K, timestamp=now)` on the `snapshot` port, where `K` is the most recent `Per-Instrument Seq` emitted for `I` at or before `anchor_seq`.
   - Emit `T` `SnapshotOrder` messages, packed into frames.
   - Emit `SnapshotEnd(I, S, N)`.
3. NOT interleave two snapshot groups for different instruments. All frames on the `snapshot` port carrying orders for one instrument MUST precede the first frame carrying orders for another instrument.
4. Complete one full round-robin cycle (one snapshot per active instrument) within the configured **snapshot cycle period**. Recommended cycle period: 15 s for channels carrying up to ~100k resting orders; the value is operator-tunable and deployment-specific.
5. Include an instrument with no resting orders at capture time as `SnapshotBegin(total_orders=0) → SnapshotEnd`, with no intervening `SnapshotOrder` messages. An empty book is a valid snapshot.

The snapshot cycle period is not advertised in-band in this version of the spec. Subscribers MUST NOT assume a specific cycle period; worst-case gap recovery time is whatever the deployment operates at.

### Inconsistency Detection and Per-Instrument Reset

If the publisher detects that its internal book state has diverged from the upstream source for one or more instruments, it MUST:

1. Emit `InstrumentReset(I, new_anchor_seq=S', reason=R)` on `mktdata`, where `S'` is the `mktdata` `Sequence Number` of the frame carrying this `InstrumentReset` message (i.e., the reset takes effect immediately; no delta with `mktdata_seq ≤ S'` for `I` applies to the post-reset state). The `InstrumentReset` message itself lives on the frame with seq `S'`; the subscriber's `discard deltas with mktdata_seq ≤ S'` rule therefore discards it from any replay buffer after the reset semantic has been captured, which is the intended behaviour.
2. Pause emission of further deltas for `I` until an out-of-cycle snapshot for `I` with `Anchor Seq = S'` has been emitted.
3. Emit the recovery snapshot for `I` on the `snapshot` port. If another snapshot for a different instrument is currently in flight on the `snapshot` port, the publisher MUST let it complete before beginning the recovery snapshot.
4. Resume delta emission for `I` after `SnapshotEnd` is emitted.

For channel-wide inconsistency (not localised to one instrument), the publisher MUST bump `Reset Count` and restart the session rather than emit many `InstrumentReset` messages.

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0 on each of the three ports.
2. Begins emitting `InstrumentDefinition` on the `refdata` port, paced evenly across the definition cycle period (recommended 30 s per the [Reference Data Distribution supplement](../reference-data/spec.md)).
3. Begins emitting `ManifestSummary` with `Valid = 1` on the `refdata` port at the manifest cadence (recommended 1 s).
4. Begins emitting `SnapshotBegin` / `SnapshotOrder` / `SnapshotEnd` on the `snapshot` port, round-robin across active instruments, at the configured snapshot cycle period.
5. Begins emitting `OrderAdd`, `OrderCancel`, `OrderExecute`, `Trade`, and (optionally) `BatchBoundary` on the `mktdata` port as venue events arrive. Emits `Heartbeat` on `mktdata` when idle.
6. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, emits an updated `ManifestSummary`, and ensures the next snapshot cycle includes the new set.
7. On shutdown → emits `EndOfSession` on the `mktdata` port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency and Bandwidth

Per-message wire costs:

| Message | Size | Per-frame packing |
|---------|-----:|-------------------|
| OrderAdd | 52 B | ~23 per 1,232-byte frame |
| OrderCancel | 32 B | ~37 per frame |
| OrderExecute | 56 B | ~21 per frame |
| Trade | 52 B | ~23 per frame |
| BatchBoundary | 16 B | ~75 per frame |
| InstrumentReset | 28 B | ~43 per frame |
| SnapshotBegin | 36 B | Negligible |
| SnapshotOrder | 44 B | 27 per frame |
| SnapshotEnd | 20 B | Negligible |

### Snapshot Stream

For a channel with `N` active instruments, total resting-order count `R` across those instruments, snapshot cycle period `T` seconds:

- Cycle wire volume ≈ `R × 44 B + N × (36 B + 20 B)` (orders plus SnapshotBegin/SnapshotEnd overhead per instrument).
- Continuous snapshot bandwidth ≈ cycle wire volume / `T`.

Worked example at reference scale (`N = 350`, `R = 100,000`, `T = 15 s`):

- Cycle wire volume ≈ `100,000 × 44 + 350 × (36 + 20) ≈ 4.4 MB`.
- Continuous bandwidth ≈ `4.4 MB / 15 s ≈ 293 KB/s ≈ 2.3 Mbps`.

Shorter cycle periods trade bandwidth for worst-case gap recovery time; longer cycle periods do the reverse. Channel sharding (splitting `R` across multiple channels) compounds favorably because per-channel cycle bandwidth falls and per-channel gap recovery time falls, at the cost of aggregate bandwidth going up modestly.

### Delta Stream

Delta-stream bandwidth depends on venue activity and is not bounded by this spec. For a block-batched venue emitting ~2 Hz with ~1,000–2,000 book-affecting events per block, converting to per-message primitives averaging ~50 B each yields approximately 1–2 Mbps.

The format is fixed-size and binary; parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Relationship to Sibling Feeds

The DoubleZero Market-by-Order Feed is a sibling of the [Top-of-Book & Trades Feed](../top-of-book/spec.md) and the [Midpoint Feed](../midpoint/spec.md). Sibling feeds share:

- The 24-byte frame header layout (except for the `Magic` value).
- The 4-byte application message header.
- The [Reference Data Distribution supplement](../reference-data/spec.md) conformance, including `InstrumentDefinition` (0x02) and `ManifestSummary` (0x07).
- The cross-spec message Type IDs `0x01` (Heartbeat), `0x04` (Trade), `0x06` (EndOfSession) byte-for-byte.
- The session-lifecycle and `Reset Count` patterns.
- The forward-compatibility rules.

Distinctions of the market-by-order feed:
- `Magic` is `0x4444` (vs. `0x445A` top-of-book, `0x4D44` midpoint).
- Three-port channel model (vs. two).
- Market-by-order-specific payload Type IDs live in `0x10` and above.

A publisher MAY operate any subset of the sibling feeds for the same instruments simultaneously. Subscribers MAY consume any subset independently.

---

## Versioning and Forward Compatibility

The Schema Version byte in the frame header is `1` for this release. Future versions of this specification MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Cancel Reason, Reset Reason, Order Flags, and Exec Flags. Decoders MUST accept any `u8` value.
- Promote `Trade` to a shared cross-spec supplement (requires coordinated version bump of this spec and the top-of-book feed).
- Widen `Per-Instrument Seq` to `u64` if `u32` wraparound becomes a practical concern within a single `Reset Count` era (requires Schema Version bump).
- Introduce an optional `OrderModify` message type for venues with true in-place modification semantics.

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.
