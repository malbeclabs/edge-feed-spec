# DoubleZero Midpoint Feed

The DoubleZero Midpoint Feed is a wire format for single-value derived prices delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing one price per instrument, derived by the publisher from a matching engine's order book or trades.

This is a sibling protocol to the DoubleZero Top-of-Book & Trades Feed, not a layer on top. Where the top-of-book feed carries two-sided BBO data and trades, the midpoint feed carries a single derived price plus the provenance needed to interpret it. A publisher MAY operate both feeds for the same set of instruments; subscribers MAY consume one without the other.

This document specifies version **3.0.0**: the frame header, application message header, and the message types sufficient to operate a working midpoint publisher and subscriber.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price message carries a `u16` Source ID. It names the matching engine whose order book or trades the price is derived from.
7. **Single-value, not single-sided.** A midpoint message is one price. There is no degenerate "bid = ask" encoding; consumers that want a two-sided book should use the top-of-book feed.
8. **Derivation-explicit.** Every price declares *how* it was computed and carries the timestamps needed to localize latency. A derived price without provenance is a number without a contract.

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
| 2 | Schema Version | `u8` | Wire format generation, equal to this spec's MAJOR version. `3` for all `3.x.y` releases. A subscriber MUST discard frames whose version it does not implement. |
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

## Identity Model

### Instrument IDs

The unique key for an instrument in this feed is the tuple **`(channel_id, instrument_id)`**. `instrument_id` is a `u32` scoped to its channel; it need not be globally unique across channels. Subscribers consuming multiple channels MUST key their internal instrument map by the tuple.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal |
| `0x02` | InstrumentDefinition | 130 | refdata | Reference data for an instrument |
| `0x03` | Midpoint | 48 | mktdata | Single-value derived price (the core message) |
| `0x06` | EndOfSession | 12 | mktdata | No more data for this session |
| `0x07` | ManifestSummary | 24 | refdata | Published instrument set summary (see supplement) |

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

### 0x02 InstrumentDefinition (130 bytes)

Maps a numeric Instrument ID to human-readable metadata. Carried on the reference data port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

The layout is the same as the top-of-book feed's `InstrumentDefinition`. The Description column gives the rules for this feed.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x02`, Length=130 |
| 4 | Instrument ID | `u32` | Unique numeric ID for this instrument. SHOULD match the top-of-book feed's ID for the same instrument when both feeds are operated by the same publisher. |
| 8 | Source ID | `u16` | The matching engine whose order book or trades the price is derived from, as assigned by the [Source ID Registry](../sources/spec.md). |
| 10 | Symbol | `char[64]` | Human-readable label, left-justified and null-padded (e.g., `"BTC-USDT"`). Truncate only if the venue's symbol exceeds 64 bytes. |
| 74 | Leg1 | `char[8]` | First leg/component (e.g., base currency). |
| 82 | Leg2 | `char[8]` | Second leg/component (e.g., quote currency). |
| 90 | Asset Class | `u8` | See Asset Class table. |
| 91 | Price Exponent | `i8` | Implied decimal exponent for `Price`. e.g., `-2` means divide raw value by 100. MAY be finer than `Tick Size`. |
| 92 | Qty Exponent | `i8` | Implied decimal exponent for `Volume`. |
| 93 | Market Model | `u8` | Market model of the matching engine. See Market Model table. |
| 94 | Tick Size | `price` | Minimum price increment of the matching engine (interpreted via Price Exponent). `0` if not available. |
| 102 | Lot Size | `qty` | No field in this feed uses it. `0`. |
| 110 | Contract Value | `u64` | `0`. |
| 118 | Expiry | `ts_ns` | Expiration timestamp. 0 for non-expiring. |
| 126 | Settle Type | `u8` | `0`. |
| 127 | Price Bound | `u8` | 0=Unbounded, 1=Bounded [0,1] (binary outcomes), 2=Non-negative only |
| 128 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. See supplement. |

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

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

### 0x03 Midpoint (48 bytes)

The core message. A single derived price with explicit provenance.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x03)  |  Length (48)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |    Method     | Quality Flags |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Input Timestamp (ts_ns)                     |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                  Compute Timestamp (ts_ns)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Price (price, i64)                     |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         Window (u32)                          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Volume (qty, u64)                       |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x03`, Length=48 |
| 4  | Instrument ID | `u32` | Instrument this price applies to |
| 8  | Source ID | `u16` | The matching engine whose order book or trades the price is derived from. MUST equal the `Source ID` in the instrument's `InstrumentDefinition`. |
| 10 | Method | `u8` | How the price was computed. See Method table. MUST NOT be `0`. |
| 11 | Quality Flags | `u8` | Bit 0: stale, bit 1: one-sided input, bit 2: book crossed/locked, bit 3: synthetic. Bits 4–7: reserved. |
| 12 | Input Timestamp | `ts_ns` | Venue timestamp. Book method: the book state. Method `4`: the trade. Method `5`: the end of the window. |
| 20 | Compute Timestamp | `ts_ns` | When the publisher computed the price. `Compute Timestamp − Input Timestamp` is publisher-side processing lag. |
| 28 | Price | `price` | The derived price, in the instrument's Price Exponent |
| 36 | Window | `u32` | Length of the trade window in milliseconds. See *Window*. |
| 40 | Volume | `qty` | Sum of `trade_qty` over the trades in the window, in the instrument's Qty Exponent. See *Window*. |

#### Method Values

| Value | Name | Input | Definition |
|-------|------|-------|------------|
| 0 | Unknown | — | Publishers MUST NOT send this value. |
| 1 | BBO Mid | book | `(best_bid + best_ask) / 2` from the venue's top of book |
| 2 | Micro-Price | book | `(best_ask × bid_qty + best_bid × ask_qty) / (bid_qty + ask_qty)` |
| 3 | Weighted Mid | book | Depth-weighted across multiple price levels; the depth and weighting are publisher-defined |
| 4 | Last-Trade Fallback | trade | The most recent trade price, used when the book is one-sided or empty |
| 5 | VWAP | trade | `Σ(trade_price × trade_qty) / Σ trade_qty` over the trades in the window |
| 255 | Custom | — | Publisher-specific; documented out of band |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown non-zero values as `255` (Custom).

#### Window

For method `5`:

- `Window` MUST NOT be `0`.
- The window is the interval `(Input Timestamp − W, Input Timestamp]`, where `W` is `Window × 1,000,000` nanoseconds.
- A trade is in the window if its venue timestamp is in this interval. The start is exclusive. The end is inclusive.
- `Volume` is the sum of `trade_qty` over the trades in the window.
- If the window has no trades, `Volume` is `0`, the publisher carries over the last `Price`, and sets the `synthetic` bit.

For all other methods, `Window` and `Volume` MUST be `0`.

#### Quality Flag Semantics

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | stale | The inputs have not updated within the publisher's freshness window. The price is the best available value but should be treated with reduced confidence. |
| 1 | one-sided | Book methods only. The underlying book had liquidity on only one side at compute time. |
| 2 | crossed-or-locked | Book methods only. The underlying book had `bid >= ask` at compute time. The price is still well-defined for most methods but the book state was anomalous. |
| 3 | synthetic | The price was not derived from the current inputs (e.g., last-trade fallback for a book method, carry-over from a previous tick, or any other rule that does not read the current inputs). |

For a trade method, bits 1 and 2 MUST be `0`.

Bits 1 and 3 are independent: a one-sided book may still produce a non-synthetic price if the publisher's method has a defined behavior for one-sided books. A synthetic price may be set on a two-sided book if the publisher chose not to use it (e.g., during a known data-quality incident).

A `Midpoint` with `Quality Flags == 0` indicates a clean, fresh computation.

#### Three Timestamps

A subscriber consuming this feed has three timestamps available per price and can localize latency between any two:

1. **Input Timestamp** (in `Midpoint`): venue's timestamp on the latest input, or the end of the window for method `5`.
2. **Compute Timestamp** (in `Midpoint`): when the publisher finished computing this price.
3. **Send Timestamp** (in the frame header): when the publisher handed the frame to the network.

`Compute − Input` is publisher-side processing lag. `Send − Compute` is publisher egress lag. `(receiver_now) − Send` is wire + receive lag.

### 0x06 EndOfSession (12 bytes)

No more data on this channel for the current session.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x06`, Length=12 |
| 4 | Timestamp | `ts_ns` | |

### 0x07 ManifestSummary (24 bytes)

Periodic summary of the published instrument set on this channel. Carried on the reference data port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); the layout is reproduced here for convenience.

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

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0.
2. Begins emitting **InstrumentDefinition** for every active instrument on the reference data port, paced evenly across the definition cycle period (recommended 30 s). Definitions are retransmitted continuously, not just at startup.
3. Begins emitting **ManifestSummary** with `Valid = 1` on the reference data port at the manifest cadence (recommended 1 s).
4. Begins sending **Midpoint** messages on the market data port as the inputs update. Multiple messages MAY be batched into a single frame.
5. When the market data path is idle → sends **Heartbeat** every N seconds on the market data port.
6. When the published instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, and emits an updated `ManifestSummary` within the manifest cadence interval.
7. On shutdown → sends **EndOfSession** on the market data port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency

A single Midpoint is **48 bytes** of application payload, or **72 bytes** including the frame header for a single-message frame. Multiple midpoints may be packed into one frame up to the 1,232-byte maximum; for example, 25 midpoints plus the frame header is 1,224 bytes.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Versioning and Forward Compatibility

This document is version **3.0.0**, versioned independently of the other feed specs. The Schema Version byte in the frame header is `3` and equals this spec's MAJOR version, so it stays `3` for every `3.x.y` release and changes only on a breaking wire change. See the [Versioning Policy](../VERSIONING.md) for the full rule, the change classification, and the tag scheme.

Future `3.x` versions of this specification MAY, without a Schema Version bump:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Asset Class and Method (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the `3.x` line. A change that moves or resizes a field, alters a message length, or redefines existing semantics requires a MAJOR release and a Schema Version bump, which old decoders MUST reject rather than parse.

### Changes

**3.0.0** — breaking. Schema Version is now `3`. A decoder built for `1.x` MUST reject these frames. There is no `2.x` release: `3` is the Schema Version of the other feeds that use the 130-byte `InstrumentDefinition`.

- `InstrumentDefinition` uses the shared 130-byte layout of the top-of-book feed. It was 64 bytes. It adds `Source ID`, `Qty Exponent`, `Market Model`, `Lot Size`, `Contract Value` and `Settle Type`, and widens `Symbol` to `char[64]`. `Default Method` is removed.
- `Midpoint` Method `0` is now Unknown, and publishers MUST NOT send it. It meant "use `Default Method`".
- Added Method `5` (VWAP). Each Method value is now classified as a book method or a trade method.
- `Midpoint` offset 36 is now `Window` (`u32`, milliseconds). It was reserved.
- `Midpoint` grows from 40 to 48 bytes. Offset 40 is `Volume` (`qty`).
- Renamed `Book Timestamp` to `Input Timestamp` and `Mid Price` to `Price`. Offsets do not change.
- Quality Flags bits 1 and 2 apply to book methods only.
- Added an *Identity Model* section.

**1.1.0** — added Asset Class value `5` (Perpetual Future) to the value table, which this spec omitted while top-of-book and market-by-price carried it. Additive: receivers already MUST accept any `u8` and treat unknown values as `0`. No wire-layout change, and the 64-byte `InstrumentDefinition` variant is untouched; Schema Version remains `1`.

**1.0.1** — editorial. Removed the *Relationship to Sibling Feeds* enumeration, relocating its `Magic` distinctness requirement verbatim to the [Versioning Policy](../VERSIONING.md) so it binds every feed rather than only the ones that happened to restate it, and adopted the glossary's "published set". No wire change.

**1.0.0** — first stable release. Promoted from the `0.1.0` draft with no wire change; Schema Version was `1` before and after.
