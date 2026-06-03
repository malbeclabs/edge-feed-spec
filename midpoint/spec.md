# DoubleZero Midpoint Feed

The DoubleZero Midpoint Feed is a wire format for single-value mid prices delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing one mid price per instrument, derived by the publisher from a venue's order book.

This is a sibling protocol to the DoubleZero Top-of-Book & Trades Feed, not a layer on top. Where the top-of-book feed carries two-sided BBO data and trades, the midpoint feed carries a single derived price plus the provenance needed to interpret it. A publisher MAY operate both feeds for the same set of instruments; subscribers MAY consume one without the other.

This document specifies version **0.1.0**: the frame header, application message header, and the message types sufficient to operate a working midpoint publisher and subscriber.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price message carries a `u16` source ID identifying the venue whose book the mid was computed from.
7. **Single-value, not single-sided.** A midpoint message is one price. There is no degenerate "bid = ask" encoding; consumers that want a two-sided book should use the top-of-book feed.
8. **Derivation-explicit.** Every midpoint declares *how* it was computed and carries the timestamps needed to localize latency. A derived price without provenance is a number without a contract.

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

---

## Transport Framing

One UDP datagram = one frame. Frames do not span packet boundaries. Multiple application messages may be packed into a single frame. The maximum frame size is **1,232 bytes** to leave room for GRE encapsulation headers used by the DoubleZero network's last-mile delivery.

### Two-Port Channel Model

Each channel is delivered to **one multicast group on two destination ports**, per the [Reference Data Distribution supplement](../reference-data/spec.md):

| Port | Carries |
|------|---------|
| mktdata | `Midpoint`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |

The frame header and application message header are identical on both ports. A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port.

### Frame Header (24 bytes)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     Magic (0x4D44)            |  Schema Ver   |  Channel ID   |
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
| 0 | Magic | `u16` | `0x4D44` ("DM"). Frame delimiter. Distinct from the top-of-book feed's magic to prevent cross-protocol misrouting. |
| 2 | Schema Version | `u8` | Protocol version. Starts at `1`. |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. |
| 4 | Sequence Number | `u64` | Monotonically increasing per channel, starting from 0. Resets to 0 when `Reset Count` changes. Used for gap detection. |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value. |
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

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal |
| `0x02` | InstrumentDefinition | 64 | refdata | Reference data for an instrument |
| `0x03` | Midpoint | 40 | mktdata | Single-value mid price (the core message) |
| `0x06` | EndOfSession | 12 | mktdata | No more data for this session |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary (see supplement) |

Type ID `0x04` is intentionally unused. It is the `Trade` message in the top-of-book feed; leaving the slot vacant in this protocol prevents accidental cross-decoding if a frame is misrouted between feeds. Type IDs `0x08`–`0xFF` are reserved.

A decoder encountering an unknown type MUST skip the message using its Message Length field and continue parsing the frame.

---

## Message Definitions

### 0x01 Heartbeat (16 bytes)

Sent every N seconds when there is no other traffic. Receivers use this for stale-connection detection.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x01`, Length=16 |
| 4 | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5 | Reserved | 3B | Padding |
| 8 | Timestamp | `ts_ns` | Current time |

### 0x02 InstrumentDefinition (64 bytes)

Maps a numeric Instrument ID to human-readable metadata. Carried on the reference data port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

This definition is slimmed relative to the top-of-book feed: it omits Lot Size, Contract Value, and Settle Type, none of which apply to a derived value that does not trade. It adds a `Default Method` byte so that subscribers know how mids are computed when a `Midpoint` message uses method `0`.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x02`, Length=64 |
| 4  | Instrument ID | `u32` | Unique numeric ID for this instrument. SHOULD match the top-of-book feed's ID for the same instrument when both feeds are operated by the same publisher. |
| 8  | Symbol | `char[16]` | Human-readable label, e.g. `"BTC-USDT"`. |
| 24 | Leg1 | `char[8]` | First leg/component (e.g., base currency). |
| 32 | Leg2 | `char[8]` | Second leg/component (e.g., quote currency). |
| 40 | Asset Class | `u8` | See Asset Class table. |
| 41 | Price Exponent | `i8` | Implied decimal exponent for price fields. e.g., `-2` means divide raw value by 100. |
| 42 | Default Method | `u8` | Default computation method for `Midpoint` messages on this instrument. See Method table. |
| 43 | Price Bound | `u8` | 0=Unbounded, 1=Bounded [0,1] (binary outcomes), 2=Non-negative only |
| 44 | Tick Size | `price` | Minimum price increment (interpreted via Price Exponent). |
| 52 | Expiry | `ts_ns` | Expiration timestamp. 0 for non-expiring. |
| 60 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. See supplement. |
| 62 | Reserved | 2B | Padding |

#### Asset Class Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | Crypto Spot |
| 2 | Prediction Binary |
| 3 | Prediction Scalar |
| 4 | Prediction Categorical |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

### 0x03 Midpoint (40 bytes)

The core message. A single derived mid price with explicit provenance.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x03)  |  Length (40)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |    Method     | Quality Flags |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Book Timestamp (ts_ns)                     |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                  Compute Timestamp (ts_ns)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Mid Price (price, i64)                   |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                           Reserved                            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x03`, Length=40 |
| 4  | Instrument ID | `u32` | Instrument this midpoint applies to |
| 8  | Source ID | `u16` | The venue whose order book this mid was computed from |
| 10 | Method | `u8` | How the mid was computed. `0` means use the instrument's `Default Method`. See Method table. |
| 11 | Quality Flags | `u8` | Bit 0: stale, bit 1: one-sided input, bit 2: book crossed/locked, bit 3: synthetic. Bits 4–7: reserved. |
| 12 | Book Timestamp | `ts_ns` | Venue timestamp of the underlying book state used for this mid |
| 20 | Compute Timestamp | `ts_ns` | When the publisher computed the mid. `Compute Timestamp − Book Timestamp` is publisher-side processing lag. |
| 28 | Mid Price | `price` | The midpoint, in the instrument's Price Exponent |
| 36 | Reserved | 4B | Padding to 40 bytes; reserved for future appended fields |

#### Method Values

| Value | Name | Definition |
|-------|------|------------|
| 0 | Default | Use the instrument's `Default Method` from its `InstrumentDefinition`. A `Midpoint` MUST NOT use method `0` if no `InstrumentDefinition` with a non-zero `Default Method` has been published for the instrument. |
| 1 | BBO Mid | `(best_bid + best_ask) / 2` from the venue's top of book |
| 2 | Micro-Price | `(best_ask × bid_qty + best_bid × ask_qty) / (bid_qty + ask_qty)` |
| 3 | Weighted Mid | Depth-weighted across multiple price levels; the depth and weighting are publisher-defined |
| 4 | Last-Trade Fallback | The most recent trade price, used when the book is one-sided or empty |
| 255 | Custom | Publisher-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown non-zero values as `255` (Custom).

#### Quality Flag Semantics

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | stale | The underlying book has not updated within the publisher's freshness window. The mid is the best available value but should be treated with reduced confidence. |
| 1 | one-sided | The underlying book had liquidity on only one side at compute time. |
| 2 | crossed-or-locked | The underlying book had `bid >= ask` at compute time. The mid is still well-defined for most methods but the book state was anomalous. |
| 3 | synthetic | The mid value was not derived from the live book at compute time (e.g., last-trade fallback, carry-over from a previous tick, or any other rule that does not read the current book). |

Bits 1 and 3 are independent: a one-sided book may still produce a non-synthetic mid if the publisher's method has a defined behavior for one-sided books. A synthetic mid may be set on a two-sided book if the publisher chose not to use it (e.g., during a known data-quality incident).

A `Midpoint` with `Quality Flags == 0` indicates a clean, two-sided, fresh computation.

#### Three Timestamps

A subscriber consuming this feed has three timestamps available per midpoint and can localize latency between any two:

1. **Book Timestamp** (in `Midpoint`): venue's timestamp on the underlying book state.
2. **Compute Timestamp** (in `Midpoint`): when the publisher finished computing this mid.
3. **Send Timestamp** (in the frame header): when the publisher handed the frame to the network.

`Compute − Book` is publisher-side processing lag. `Send − Compute` is publisher egress lag. `(receiver_now) − Send` is wire + receive lag.

### 0x06 EndOfSession (12 bytes)

No more data on this channel for the current session.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x06`, Length=12 |
| 4 | Timestamp | `ts_ns` | |

### 0x07 ManifestSummary (24 bytes)

Periodic summary of the active instrument set on this channel. Carried on the reference data port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); the layout is reproduced here for convenience.

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

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0.
2. Begins emitting **InstrumentDefinition** for every active instrument on the reference data port, paced evenly across the definition cycle period (recommended 30 s). Definitions are retransmitted continuously, not just at startup.
3. Begins emitting **ManifestSummary** with `Valid = 1` on the reference data port at the manifest cadence (recommended 1 s).
4. Begins sending **Midpoint** messages on the market data port as the underlying books update. Multiple messages MAY be batched into a single frame.
5. When the market data path is idle → sends **Heartbeat** every N seconds on the market data port.
6. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, and emits an updated `ManifestSummary` within the manifest cadence interval.
7. On shutdown → sends **EndOfSession** on the market data port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency

A single Midpoint is **40 bytes** of application payload, or **64 bytes** including the frame header for a single-message frame. Multiple midpoints may be packed into one frame up to the 1,232-byte maximum; for example, 30 midpoints plus the frame header is 1,224 bytes.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Versioning and Forward Compatibility

The Schema Version byte in the frame header is `1` for this release. Future versions of the specification MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Asset Class and Method (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.

---

## Relationship to Sibling Feeds

The DoubleZero Midpoint Feed is one of a family of sibling protocols that share framing conventions but differ in payload:

- The **Top-of-Book & Trades Feed** carries two-sided BBO quotes and trade reports from a venue.
- The **Midpoint Feed** (this document) carries a single derived mid price per instrument.
- The **[Market-by-Order Feed](../market-by-order/spec.md)** carries the full resting-order population per instrument as a market-by-order stream of order events, anchored by a continuous in-band snapshot stream for recovery.

Sibling feeds share the data type table, the 24-byte frame header layout, the 4-byte application message header, the session lifecycle, and the forward-compatibility rules. They differ in magic value, message type table, and message payloads. A subscriber MAY consume any subset of these feeds independently. Sibling feeds MUST use distinct Magic values and SHOULD use distinct multicast groups.
