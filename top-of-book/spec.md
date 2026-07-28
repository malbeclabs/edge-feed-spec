# DoubleZero Top-of-Book & Trades Feed

The DoubleZero Top-of-Book & Trades Feed is a wire format for L1 price feeds delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing two-sided market data (best bid / best ask quotes and trades) from any venue with an order book.

This document specifies the frame header, application message header, and the initial set of message types sufficient to operate a working publisher and subscriber. It is intended to be stable enough to build against and to share with prospective data publishers for feedback.

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
| 2 | Schema Version | `u8` | Protocol version, denoting the application message header layout. `1` is common framing 0.1.x; **`2` is common framing 0.2.0 and is what publishers of this feed MUST declare**. A subscriber MUST discard frames whose version it does not implement; see [Application Message Header](#application-message-header-4-bytes). |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. |
| 4 | Sequence Number | `u64` | Monotonically increasing per channel, starting from 0. Resets to 0 when `Reset Count` changes. Used for gap detection. |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value. |
| 22 | Frame Length | `u16` | Total frame length in bytes, including this header. |

---

## Application Message Header (4 bytes)

This section is the canonical definition of the **common application message header** shared by every feed in this family. It is at **common framing version 0.2.0**; the sibling specs restate it and MUST NOT diverge from it.

Every application message begins with:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Message Type  | Msg Length (12b) + Rsvd (4b)  |     Flags     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Message Type | `u8` | See Message Types table. |
| 1 | Message Length | `u16` | Little-endian 16-bit word at offset 1. **Low 12 bits**: total message length in bytes including this header. **Top 4 bits** (mask `0xF000`): reserved, MUST be zero on emission and MUST be masked off on receipt. |
| 3 | Flags | `u8` | Bit 0: snapshot (1) vs. incremental (0). Bits 1–7: reserved, MUST be zero on emission and MUST be ignored on receipt. |

`Message Length` is a single **contiguous 12-bit field**, read as:

```
Message Length = read_u16_le(byte[1..2]) & 0x0FFF      // 0 .. 4095
```

A publisher MUST set `Message Length` to at least `4`. A subscriber MUST bounds-check it before using it to advance the frame walk: a value below `4` or exceeding the bytes remaining in the frame makes the frame malformed. The 12-bit field admits 4,095 bytes, but the binding limit is the frame: at the 1,232-byte maximum frame size a single message cannot exceed **1,208 bytes**.

The 16-bit word sits at an odd offset, so reading it is an unaligned load. That is native and cheap on x86-64 and ARM64, and byte-oriented streaming and FPGA parsers are unaffected. The header remains 4 bytes, so the 28-byte frame-plus-message prefix and the 4-byte field grid the family relies on are unchanged.

### Relationship to Common Framing 0.1.x

**0.2.0 is a breaking change to the header layout, not a compatible extension.** 0.1.x defined offset 1 as a `u8` `Message Length` capped at 255 and offsets 2–3 as a `u16` `Flags` whose bits 1–15 were reserved. Keeping the widened length contiguous means `Flags` moves: it is now a `u8` at offset 3, where 0.1.x read it as a `u16` at offset 2.

The failure mode when a 0.1.x decoder is pointed at a 0.2.0 frame is **silent, not loud**, which is why the version gate below is mandatory rather than advisory:

- The **message walk still succeeds** for messages of 255 bytes or fewer. The length's low byte remains at offset 1, so a decoder reading byte 1 alone advances correctly. Nothing desynchronizes and nothing looks wrong.
- But **`Flags` is misread**. A 0.1.x decoder computing `byte[2] | byte[3]<<8` finds the length's reserved and high bits in positions 0–7 and the real `Flags` in positions 8–15, so bit 0 — snapshot vs. incremental — reads as clear. On the [Market-by-Order Feed](../market-by-order/spec.md), where that bit distinguishes a snapshot message from a delta, a snapshot would be applied as an incremental update with no error raised.
- A message **longer than 255 bytes** desynchronizes the walk as well, since a 0.1.x decoder cannot see the length's high bits at all.

`Schema Version` in the frame header is therefore the gate, and it now denotes the header layout itself:

- `Schema Version = 1` — application message headers on this channel are common framing 0.1.x: `u8` length at offset 1, `u16` `Flags` at offset 2.
- `Schema Version = 2` — application message headers are common framing 0.2.0: contiguous 12-bit length at offset 1, `u8` `Flags` at offset 3.

Every feed in the family adopts 0.2.0, so **every publisher MUST declare `Schema Version = 2`** on every frame and on every port of the channel — the schema is a property of the channel, not of one port. A subscriber MUST validate `Schema Version` before walking a frame, and MUST discard (and count) any frame whose version it does not implement: a 0.1.x-only subscriber MUST NOT walk a version-2 frame, and a 0.2.0 subscriber MUST NOT walk a version-1 frame.

Because the layout change is not backward compatible, adopting it is a coordinated upgrade: for a given channel, publishers and subscribers move together. `Schema Version` is what makes a half-upgraded deployment fail visibly — frames discarded and counted — rather than silently mis-decoding `Flags`.

What the break buys is that `Message Length` is one contiguous field, read with a single 16-bit load and a mask, with no split-field reassembly and no reserved bits stranded in an unrelated byte.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal |
| `0x02` | InstrumentDefinition | 80 | refdata | Reference data for an instrument |
| `0x03` | Quote | 60 | mktdata | Two-sided BBO update (the core L1 message) |
| `0x04` | Trade | 52 | mktdata | Last trade report |
| `0x06` | EndOfSession | 12 | mktdata | No more data for this session |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary (see supplement) |
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

### 0x02 InstrumentDefinition (80 bytes)

Maps a numeric Instrument ID to human-readable metadata. Carried on the reference data port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

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
| 8 | Source ID | `u16` | Originating source. Publishers operating a single source MAY use a fixed value (e.g., `1`). |
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
6. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, and emits an updated `ManifestSummary` within the manifest cadence interval.
7. On shutdown → sends **EndOfSession** on the market data port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency

For a single two-sided BBO update, a Quote is **60 bytes** of application payload, or **84 bytes** including the frame header for a single-message frame. Multiple quotes may be packed into one frame up to the 1,232-byte maximum; for example, 20 quotes plus the frame header is 1,224 bytes.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Versioning and Forward Compatibility

The Schema Version byte in the frame header is `2` for this release, denoting common framing 0.2.0 (see [Application Message Header](#application-message-header-4-bytes)). Future versions of the specification MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Asset Class and Market Model (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.

`0x08 Liquidation` was added as a shared trade-companion type under Schema Version `1`, without a bump, because old decoders skip it via Message Length.

Asset Class value `5` (Perpetual Future) was added under Schema Version `1`, without a bump, because it is a new enumerated value and decoders already MUST accept any `u8` and treat unknown values as `0` (Unknown).

The common application message header was widened to a contiguous 12-bit `Message Length` (**common framing 0.2.0**, defined in [Application Message Header](#application-message-header-4-bytes)) to admit messages longer than 255 bytes on feeds that need them. Keeping the field contiguous moved `Flags` from a `u16` at offset 2 to a `u8` at offset 3, so **Schema Version becomes `2`** and the change is breaking for every feed in the family, including this one: no message type here exceeds 80 bytes, but the header layout still changed. Publishers and subscribers of a channel must be upgraded together, and `Schema Version` is what makes a half-upgraded channel discard frames instead of mis-decoding `Flags`.
