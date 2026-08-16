# DoubleZero Top-of-Book & Trades Feed

The DoubleZero Top-of-Book & Trades Feed is a wire format for L1 price feeds delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing two-sided market data (best bid / best ask quotes and trades) from any venue with an order book.

This document specifies version **3.0.0**: the frame header, application message header, and the set of message types sufficient to operate a working publisher and subscriber.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups. Simple to parse in any language.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram. The protocol defines application messages only; transport, addressing, and group membership are out of scope.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price message carries a `u16` source ID. With a single publisher this is redundant; with many it is essential. Cheap to carry now, expensive to retrofit later.
7. **Domain-agnostic.** Anything with a two-sided book — bids and asks at prices with quantities — is a valid instrument: crypto spot, equities, futures, FX, prediction markets, or anything else.

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
| mktdata | `Quote`, `Trade`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |

The frame header and application message header are identical on both ports. A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port.

### Frame Header (24 bytes)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     Magic (0x445A)            |  Schema Ver   |  Channel ID   |
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
| 0 | Magic | `u16` | `0x445A` ("DZ"). Frame delimiter. |
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
| `0x03` | Quote | 60 | mktdata | Two-sided BBO update (the core L1 message) |
| `0x04` | Trade | 52 | mktdata | Last trade report |
| `0x06` | EndOfSession | 12 | mktdata | No more data for this session |
| `0x07` | ManifestSummary | 24 | refdata | Published instrument set summary (see supplement) |
| `0x08` | Liquidation | 48 | mktdata | Annotation for a forced (liquidation/ADL) `Trade`, keyed on `Trade ID` |

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

`5` (Perpetual Future) identifies a perpetual-futures instrument — no expiry, funding-based convergence to an index. Perpetual Future instruments' derived state (funding, mark/oracle price, open interest) is carried on the sibling [Perp Stats Feed](../perp-stats/spec.md); the top-of-book feed still carries their `Quote`/`Trade` and this `InstrumentDefinition`.

#### Market Model Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | CLOB |
| 2 | AMM |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

### 0x03 Quote (60 bytes)

The core message. A single, fixed-size, two-sided BBO update.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x03)  |  Length (60)  |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |  Update Flags |   Reserved    |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Source Timestamp (ts_ns)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Bid Price (price, i64)                   |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Bid Quantity (qty, u64)                  |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Ask Price (price, i64)                   |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Ask Quantity (qty, u64)                  |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x03`, Length=60 |
| 4 | Instrument ID | `u32` | Instrument this quote applies to |
| 8 | Source ID | `u16` | Originating `source_id`, as assigned by the [Source ID Registry](../sources/spec.md). Publishers operating a single Source ID MAY use a fixed value (e.g., `1`). |
| 10 | Update Flags | `u8` | Bit 0: bid updated, bit 1: ask updated, bit 2: bid gone, bit 3: ask gone |
| 11 | Reserved | `u8` | Padding |
| 12 | Source Timestamp | `ts_ns` | Timestamp from the originating venue |
| 20 | Bid Price | `price` | Best bid. Uses instrument's Price Exponent. 0 if bid gone. |
| 28 | Bid Quantity | `qty` | Size at best bid. Uses instrument's Qty Exponent. |
| 36 | Ask Price | `price` | Best ask. Uses instrument's Price Exponent. 0 if ask gone. |
| 44 | Ask Quantity | `qty` | Size at best ask. Uses instrument's Qty Exponent. |
| 52 | Bid Source Count | `u16` | Orders/sources at best bid. 0 if unavailable. |
| 54 | Ask Source Count | `u16` | Orders/sources at best ask. 0 if unavailable. |
| 56 | Reserved | 4B | Padding to 60 bytes. |

### 0x04 Trade (52 bytes)

Reports a single trade execution.

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

### 0x08 Liquidation (48 bytes)

Annotates a `Trade` that resulted from a forced liquidation or auto-deleveraging (ADL). It carries no size or price of its own — those live on the paired `Trade` — so subscribers computing volume from the tape are not double-counted. A publisher that emits a `Liquidation` MUST emit it in the **same frame** as the `Trade` it annotates; subscribers join the two on `Trade ID`.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x08`, Length=48 |
| 4 | Instrument ID | `u32` | Instrument liquidated |
| 8 | Source ID | `u16` | Upstream venue (see Source ID Registry) |
| 10 | Liquidation Flags | `u8` | Bit 0: liquidated side (0 = long liquidated, 1 = short liquidated). Bit 1: ADL. |
| 11 | Method | `u8` | Liquidation mechanism. 0 = market, 1 = backstop, 0xFF = unknown. |
| 12 | Trade ID | `u64` | Venue trade ID of the paired `Trade` |
| 20 | Mark Price | `price` | Mark price at liquidation |
| 28 | Liquidated User | 20B | Liquidated account address |

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0.
2. Begins emitting **InstrumentDefinition** for every active instrument on the reference data port, paced evenly across the definition cycle period (recommended 30 s). Definitions are retransmitted continuously, not just at startup.
3. Begins emitting **ManifestSummary** with `Valid = 1` on the reference data port at the manifest cadence (recommended 1 s).
4. Begins sending **Quote** (and optionally **Trade**) messages on the market data port as market data arrives. Multiple messages MAY be batched into a single frame.
5. When the market data path is idle → sends **Heartbeat** every N seconds on the market data port.
6. When the published instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, and emits an updated `ManifestSummary` within the manifest cadence interval.
7. On shutdown → sends **EndOfSession** on the market data port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency

For a single two-sided BBO update, a Quote is **60 bytes** of application payload, or **84 bytes** including the frame header for a single-message frame. Multiple quotes may be packed into one frame up to the 1,232-byte maximum; for example, 20 quotes plus the frame header is 1,224 bytes.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Versioning and Forward Compatibility

This document is version **3.0.0**, versioned independently of the sibling specs. The Schema Version byte in the frame header is `3` and equals this spec's MAJOR version, so it stays `3` for every `3.x.y` release and changes only on a breaking wire change. See the [Versioning Policy](../VERSIONING.md) for the full rule, the change classification, and the tag scheme.

Future `3.x` versions of this specification MAY, without a Schema Version bump:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Asset Class and Market Model (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the `3.x` line. A change that moves or resizes a field, alters a message length, or redefines existing semantics requires a MAJOR release and a Schema Version bump, which old decoders MUST reject rather than parse.

### Changes

**3.0.0** — added `Source ID` (`u16`) after `Instrument ID` in `InstrumentDefinition`. `Symbol` and every later field move two bytes, and the message grows from 128 to 130 bytes. This is a breaking change: the Schema Version byte is now `3`, and a decoder built for `2.x` MUST reject these frames rather than parse them at the old offsets. The midpoint feed remains unchanged at Schema Version `1`.

**2.0.0** — widened the `InstrumentDefinition` `Symbol` field from `char[16]` to `char[64]`. Every field after `Symbol` moves and the message grows from 80 to 128 bytes, so this is a breaking change: the Schema Version byte is now `2`, and a decoder built for `1.x` MUST reject these frames rather than parse them at the old offsets. Nothing else on the wire changed. The midpoint feed keeps its 64-byte variant and stays at Schema Version `1`.

**1.0.0** — first stable release. Promoted from the `0.1.0` draft with no wire change; Schema Version was `1` before and after. Includes two additive changes made during the draft period, both of which left Schema Version at `1`: `0x08 Liquidation` was added as a shared trade-companion type (old decoders skip it via Message Length), and Asset Class value `5` (Perpetual Future) was added (decoders already MUST accept any `u8` and treat unknown values as `0` (Unknown)).
