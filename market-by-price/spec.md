# DoubleZero Market-by-Price Feed

The DoubleZero Market-by-Price Feed is a wire format for L2 aggregated-depth book data delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing the top *N* price levels of each side of an instrument's book, aggregated by price, from any venue with an order book.

This is a sibling protocol to the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md), not a layer on top. Where the top-of-book feed carries a single best bid/ask level per instrument and the market-by-order feed carries the full resting-order population, this feed carries a fixed-depth, price-aggregated view: the best *N* levels per side, each level a `(price, aggregate size, order count)` tuple.

The central design decision, and the thing that makes this the *easy* tier, is that **every message is a complete, self-contained refresh of the top *N* levels**. There is no incremental delta stream, no snapshot port, no per-instrument sequence bootstrap, and no book state for the subscriber to maintain. A subscriber reads the latest `BookDepth` for an instrument and has the whole top-*N* book. This is a deliberate departure from the market-by-order feed, which is stateful by necessity because order-level state cannot fit in a single datagram. A price-aggregated top-*N* book does fit, so the entire snapshot/delta recovery apparatus becomes unnecessary here. The subscriber still keeps a small per-instrument watermark (last frame sequence, last source timestamp, source ID, last arrival time) to reject reordered and malformed messages and to detect staleness, but that is input validation, not book reconstruction: losing it costs nothing beyond one refresh.

This document specifies version **0.1.0**: the frame header, application message header, and the message types sufficient to operate a working publisher and subscriber.

> **Modelling note for the implementer.** At the field-semantics level, each price level mirrors the CME MDP3 market-by-price book entry (`price`, aggregate quantity, number of orders at the level). At the *wire-architecture* level, this feed is the Top-of-Book `Quote` message widened from one level to *N*, not the MDP3 incremental book. We are intentionally **not** copying MDP3's or ICE iMpact's incremental+recovery model, because the Market-by-Order feed already occupies the stateful niche and the whole point of L2 is to hand the subscriber a ready-to-use book with no state machine.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. The depth array is a fixed *N*-element array, not a variable-length repeating group. Simple to parse in any language.
3. **Self-contained full refresh.** Every `BookDepth` message carries the complete top-*N* state of both sides. Messages are independent of one another; there is no delta chain to keep intact.
4. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
5. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram. The protocol defines application messages only; transport, addressing, and group membership are out of scope.
6. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
7. **Source-attributed.** Every price-carrying message carries a `u16` source ID. With a single publisher this is redundant; with many it is essential.
8. **Conflation is safe and encouraged.** Because each message is a full refresh, a publisher MAY drop intermediate book states and emit only the latest per instrument, rate-limited to an operator-defined cap. Subscribers stay correct, just marginally behind. This is impossible in an incremental feed and is one of the main reasons to run L2 as a full-refresh protocol.
9. **Domain-agnostic.** Anything with a two-sided book of prices and sizes — crypto spot, equities, futures, FX, prediction markets — is a valid instrument.

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

## Depth Parameter

The number of price levels carried per side, *N*, is fixed by this specification at **`N = 10`**. `BookDepth` is therefore a constant-length message of `24 + 40N` = **424 bytes**, carrying both sides in one atomic refresh.

424 bytes does not fit the 8-bit `Message Length` of common framing 0.1.x, which capped a message at 255 bytes and so capped this feed at *N* = 5. Ten levels per side is the requirement, so the length field was widened family-wide instead: **common framing 0.2.0** makes `Message Length` a contiguous 12-bit field at offset 1, moving `Flags` to a `u8` at offset 3 (see [Application Message Header](#application-message-header-4-bytes)). This feed is the only consumer of the extra length bits, but the layout change is breaking for the whole family, so every feed now declares frame `Schema Version = 2`.

Under 0.2.0 framing the binding constraint on *N* is the datagram budget, not the length field. At the 1,232-byte maximum frame size a single-message frame leaves 1,208 bytes for the payload, so `24 + 40N ≤ 1208` admits *N* ≤ 29. *N* = 10 sits well inside that ceiling and can be **raised** by a future version of this spec, up to 29, with a Schema Version bump; see [Versioning and Forward Compatibility](#versioning-and-forward-compatibility).

The cost of the extra depth is packing density and bandwidth, in that order of severity: at 424 bytes only **two** `BookDepth` messages fit in a frame (`24 + 2 × 424 = 872` bytes) where *N* = 5 fitted five, so a given instrument set needs about 2.5× the datagrams, and each refresh carries 1.9× the bytes. Correctness is unaffected — the message stays fixed-size, two-sided, and atomic. This is the trade weighed deliberately rather than settled by the length field, and it is quantified in [Wire Efficiency and Bandwidth](#wire-efficiency-and-bandwidth).

The number of levels actually populated on each side is carried per-message in the `Bid Levels` and `Ask Levels` fields (`0..N`); levels beyond the populated count are zero-filled. A shallow book (fewer than *N* levels resting) is represented by populating fewer levels, not by a different message.

A subscriber that needs more than *N* levels of depth is not an L2 consumer; that requirement is served by the [Market-by-Order Feed](../market-by-order/spec.md) with client-side aggregation. A deeper book does not need a new message type under 0.2.0 framing — a longer `0x40` is now expressible — so type ID `0x41` stays reserved and unused. Do not add depth speculatively.

Because `Bid Levels` and `Ask Levels` saturate at *N*, a count of `10` alone does not say whether the source book holds exactly ten levels or more than ten. `Book Flags` bits 3 and 4 carry that distinction where the publisher can determine it; see [0x40 BookDepth](#0x40-bookdepth-424-bytes).

---

## Transport Framing

One UDP datagram = one frame. Frames do not span packet boundaries. Multiple application messages may be packed into a single frame. The maximum frame size is **1,232 bytes** to leave room for GRE encapsulation headers used by the DoubleZero network's last-mile delivery. At `N = 10` a `BookDepth` message is 424 bytes, so up to two fit in one frame alongside the frame header, with room left over for a `Trade` or `Heartbeat`.

### Two-Port Channel Model

Each channel is delivered to **one multicast group on two destination ports**, per the [Reference Data Distribution supplement](../reference-data/spec.md):

| Port | Carries |
|------|---------|
| mktdata | `BookDepth`, `Trade`, `Liquidation`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |

There is **no snapshot port**. Unlike the market-by-order feed, this feed needs no in-band snapshot stream, because every `BookDepth` is itself a snapshot. The frame header and application message header are identical on both ports. A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port.

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
| 0 | Magic | `u16` | `0x4442` ("DB", wire bytes `[0x42, 0x44]`). Frame delimiter. Distinct from top-of-book `0x445A`, market-by-order `0x4444`, midpoint `0x4D44`, perp-stats `0x4450`, and order-intent `0x494F` to prevent cross-protocol misrouting. A consumer MUST validate that a received frame's `Magic` equals `0x4442` and discard any frame that does not match. |
| 2 | Schema Version | `u8` | Protocol version, denoting the application message header layout. A publisher of this feed MUST set it to `2` (common framing 0.2.0) on **both ports**. A subscriber MUST discard frames whose version it does not implement — for this feed a 0.1.x decoder would also mis-walk `BookDepth`, which is 424 bytes; see [Application Message Header](#application-message-header-4-bytes). |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. |
| 4 | Sequence Number | `u64` | Monotonically increasing **per channel per port**, starting from 0. Resets to 0 when `Reset Count` changes. Used for per-port gap detection **and, on `mktdata`, as the per-instrument ordering discriminant** ([Steady State](#steady-state) step 2). The `mktdata` and `refdata` ports each have an independent series. Exactly one publisher host emits a given `(channel, port)` series; see [Single Publisher Host per Channel](#single-publisher-host-per-channel). |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). A publisher MUST set it to the exact number of messages it packed. A subscriber MUST validate the message walk against it and discard a frame that disagrees; see [Frame Parsing](#frame-parsing) step 4. At 424 bytes per `BookDepth` a frame carries two of them, so in practice this field is small. |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value. Shared across both ports of the channel. |
| 22 | Frame Length | `u16` | Total frame length in bytes, including this header. A subscriber MUST validate it against the received datagram; see [Frame Parsing](#frame-parsing) step 1. |

### Single Publisher Host per Channel

Exactly **one publisher host** emits a given `(Channel ID, port)` sequence series. This is a normative deployment constraint of this feed, not a convention.

It is stricter than the sibling [Order-Intent Feed](../order-intent/spec.md), which tolerates several hosts of one venue publishing into the same multicast group and treats sequence gaps as a per-host health signal that never gates delivery. Here the frame `Sequence Number` is the ordering discriminant for book application ([Steady State](#steady-state) step 2), so two hosts interleaving independent series on one channel would make a subscriber silently discard legitimate refreshes from whichever host is momentarily behind — and, because a discarded refresh is never retried, leave that instrument wrong until the next re-emission.

Redundant A/B publication remains possible, but the two streams MUST be separated by channel, by multicast group, or by destination port, and arbitrated by the subscriber above this layer: per-instrument watermarks are keyed by transport origin (datagram source IP and destination port) so that each stream is only ever ordered against itself. A subscriber that folds two publisher hosts into one channel state is misconfigured, not merely lossy.

---

## Application Message Header (4 bytes)

Every application message begins with the common application message header at **common framing 0.2.0**, defined canonically in the [Top-of-Book & Trades Feed spec](../top-of-book/spec.md#application-message-header-4-bytes) and restated here:

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Message Type | `u8` | See Message Types table. |
| 1 | Message Length | `u16` | Little-endian 16-bit word at offset 1. **Low 12 bits**: total message length in bytes including this header. **Top 4 bits** (mask `0xF000`): reserved, MUST be zero on emission and MUST be masked off on receipt. |
| 3 | Flags | `u8` | Bit 0: snapshot (1) vs. incremental (0). Bits 1–7: reserved, MUST be zero on emission and MUST be ignored on receipt. Every `BookDepth` is a full refresh and there is no incremental variant, so a publisher MUST set bit 0 to `1` on every `BookDepth` (see [Publisher Behavior](#publisher-behavior) #6). A receiver MUST NOT treat a `BookDepth` with bit 0 clear as a delta: the message is still a full refresh, and the cleared bit is a publisher defect to count, not a different wire semantic. |

`Message Length` is the contiguous 12-bit total message length in bytes including this header:

```
Message Length = read_u16_le(byte[1..2]) & 0x0FFF      // minimum 4
```

This feed is the only one in the family that needs the extra length bits: `BookDepth` is 424 bytes, so bytes 1–2 carry `A8 01` and `Flags` sits at byte 3. Every other message type carried here is 80 bytes or fewer and leaves the length's high bits zero.

A publisher MUST declare frame `Schema Version = 2` on both ports of the channel — as every 0.2.0 feed does, since the version denotes the header layout — and a subscriber MUST discard frames whose `Schema Version` it does not implement. For this feed the stakes are higher than for the siblings: a 0.1.x decoder would not merely misread `Flags`, it would read a 424-byte `BookDepth` as 168 bytes and desynchronize the rest of the frame. See the canonical section for the version gate and the coordinated-upgrade rule.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal. Inherited; identical to siblings. |
| `0x02` | InstrumentDefinition | 80 | refdata | Reference data for an instrument. Inherited from the top-of-book feed. |
| `0x03` | *(reserved)* | — | — | Quote in the top-of-book feed, Midpoint in the midpoint feed. Intentionally unused here to prevent accidental cross-decoding if a frame is misrouted. |
| `0x04` | Trade | 52 | mktdata | Venue-level trade summary. **Identical byte-for-byte to the top-of-book feed's Trade**, carried here as a convenience for consumers who want a trade tape alongside depth. |
| `0x05` | *(reserved)* | — | — | |
| `0x06` | EndOfSession | 12 | mktdata | Inherited. No more data for this session. |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary. Inherited; see the [Reference Data Distribution supplement](../reference-data/spec.md). |
| `0x08` | Liquidation | 48 | mktdata | Trade-companion annotation. **Identical byte-for-byte to the top-of-book feed's Liquidation.** Emitted in the same frame as its `Trade`. |
| `0x40` | BookDepth | 424 | mktdata | Fixed-depth top-*N* aggregated book, both sides. The core L2 message. |
| `0x41` | *(reserved)* | — | — | Reserved, unused. A deeper book is a longer `0x40` under common framing 0.2.0, not a new type; see [Depth Parameter](#depth-parameter). |

A decoder encountering an unknown type MUST skip the message using its Message Length field and continue parsing the frame. `Message Length` MUST be bounds-checked before it is used to advance the walk; see [Frame Parsing](#frame-parsing).

### Cross-Spec Type ID Policy

A message Type ID that appears in more than one sibling feed MUST carry the same semantic meaning in each. The shared Type IDs are `0x01` (Heartbeat), `0x02` (InstrumentDefinition), `0x04` (Trade), `0x06` (EndOfSession), `0x07` (ManifestSummary), and `0x08` (Liquidation). Heartbeat, EndOfSession, and ManifestSummary are byte-for-byte identical across every sibling that carries them. Trade and Liquidation are byte-for-byte identical between the top-of-book feed, the market-by-order feed, and this feed. `InstrumentDefinition` shares the Type ID and the 80-byte layout with the top-of-book and market-by-order feeds.

This feed's own payload Type IDs live in the **`0x40`–`0x4F` range**, which does not overlap any sibling feed. This is a deliberate choice: `0x10`/`0x11` are the market-by-order feed's `OrderAdd`/`OrderCancel`, `0x20`–`0x22` are its snapshot messages, `0x30`–`0x3F` is claimed by the order-intent feed, and `0x30` is additionally used by the perp-stats feed for `PerpStats`. A Type ID used by one sibling for a given payload MUST NOT be reassigned to a different payload here; placing `BookDepth` at `0x40` keeps this feed strictly additive to the family's type-ID allocation.

> **Known family conflict.** `0x30` currently denotes `PerpStats` in the perp-stats feed and `OrderNew` in the order-intent feed — two different payloads under one Type ID, which the rule above forbids. Distinct `Magic` values keep the two decodable in practice, so the conflict is not load-bearing today, but it is unresolved and is tracked against those two specs, not this one. This feed's `0x40`–`0x4F` allocation is chosen to stay clear of it.

The sibling Type IDs this feed does not carry — `0x03` (Quote/Midpoint) and `0x05` — are reserved here, never reassigned, so a misrouted sibling frame is rejected rather than mis-decoded.

---

## Identity Model

The unique key for an instrument is the tuple **`(channel_id, instrument_id)`**. `instrument_id` is a `u32` scoped to its channel; it need not be globally unique across channels. Subscribers consuming multiple channels MUST key their internal instrument map by the tuple. Channel sharding across multiple publisher instances is supported natively via `Channel ID` in the frame header, exactly as in the sibling feeds; grouping and discovery are deployment concerns and out of scope.

### Single Source per Instrument

A `(channel_id, instrument_id)` identifies **one book from one source**. `Source ID` values are assigned by the canonical [Source ID Registry](../sources/spec.md): one stable id per venue, never reused. `0` is reserved by the registry and MUST NOT appear on the wire; a subscriber MUST reject a `BookDepth` carrying `Source ID = 0` ([Frame Parsing](#frame-parsing) step 6).

A publisher MUST NOT emit `BookDepth` for a single `(channel, instrument)` under more than one `Source ID`; an instrument observed at two venues is two instruments with two `InstrumentDefinition` entries and two `instrument_id` values, not one instrument_id with two sources.

This constraint is what lets subscriber state hold a single book per `(channel_id, instrument_id)` (see [Subscriber Algorithm](#subscriber-algorithm)) even though `Source ID` is, per Design Principle 7, essential in a multi-source deployment. Without it, two sources publishing the same key would alternately overwrite each other's book, and the ordering check would then permanently favour whichever source's frames arrive under the higher sequence — silently discarding the other's updates.

Publishers MUST NOT rely on subscribers reconciling two sources under one key: on observing a `BookDepth` whose `Source ID` differs from the one it has recorded for that instrument in the current `Reset Count` era, a subscriber MUST drop the message and SHOULD surface a counter, and MUST NOT interleave the two sources into one book ([Steady State](#steady-state) step 1).

---

## Message Definitions

### 0x01 Heartbeat (16 bytes)

Inherited from the top-of-book feed. Sent on the `mktdata` port at the operator-defined heartbeat interval (recommended 1 s) when there is no other traffic. Receivers use this for stale-connection detection.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x01`, Length=16 |
| 4 | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5 | Reserved | 3B | Padding |
| 8 | Timestamp | `ts_ns` | Current time |

### 0x02 InstrumentDefinition (80 bytes)

Inherited from the top-of-book feed verbatim; reproduced for standalone readability. Maps a numeric Instrument ID to human-readable metadata. Carried on the `refdata` port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x02`, Length=80 |
| 4 | Instrument ID | `u32` | Unique numeric ID for this instrument |
| 8 | Symbol | `char[16]` | Human-readable label (e.g., `"BTC-USDT"`). |
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
| 78 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. |

#### Asset Class Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | Crypto Spot |
| 2 | Prediction Binary |
| 3 | Prediction Scalar |
| 4 | Prediction Categorical |
| 5 | Perpetual Future |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown). Value `5` (Perpetual Future) identifies a perpetual-futures instrument whose derived state (funding, mark/oracle price, open interest) is carried on the sibling [Perp Stats Feed](../perp-stats/spec.md); this feed still carries its `BookDepth`, `Trade`, and `InstrumentDefinition`.

#### Market Model Values

| Value | Name |
|-------|------|
| 0 | Unknown |
| 1 | CLOB |
| 2 | AMM |

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown). For an AMM instrument, the publisher discretizes the curve into up to *N* synthetic price levels; see the `AMM-synthetic` level flag in [0x40 BookDepth](#0x40-bookdepth-424-bytes).

### 0x04 Trade (52 bytes)

Identical to the top-of-book feed's Trade message, byte-for-byte. Carried on the `mktdata` port as a venue-level summary of a single trade.

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
| 5  | Valid | `u8` | `1` when the channel has an established instrument set; `0` when uninitialized or inactive. |
| 6  | Reserved | 2B | Padding |
| 8  | Manifest Seq | `u16` | Increments every time the active instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the active set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

### 0x08 Liquidation (48 bytes)

Identical to the top-of-book feed's Liquidation message, byte-for-byte. Annotates a `Trade` that resulted from a forced liquidation or auto-deleveraging. Carries no size or price of its own; those live on the paired `Trade`. A publisher that emits a `Liquidation` MUST emit it in the **same frame** as the `Trade` it annotates; subscribers join the two on `Trade ID`.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x08`, Length=48 |
| 4 | Instrument ID | `u32` | Instrument liquidated |
| 8 | Source ID | `u16` | Upstream venue |
| 10 | Liquidation Flags | `u8` | Bit 0: liquidated side (0 = long liquidated, 1 = short liquidated). Bit 1: ADL. |
| 11 | Method | `u8` | 0 = market, 1 = backstop, 0xFF = unknown. |
| 12 | Trade ID | `u64` | Venue trade ID of the paired `Trade` |
| 20 | Mark Price | `price` | Mark price at liquidation |
| 28 | Liquidated User | 20B | Liquidated account address |

### 0x40 BookDepth (424 bytes)

The core message. A single, fixed-size, two-sided, top-*N* aggregated-depth refresh. Self-contained: it carries the entire top-*N* state of both sides at the source timestamp, and stands alone with no dependence on prior messages.

**Fixed prefix (24 bytes):**

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x40)  |  Msg Length = 424 (0x1A8)     |     Flags     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |   Book Flags  |  Bid Levels   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Ask Levels   |            Reserved (3 bytes)                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Source Timestamp (ts_ns)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   ... followed by Bids[0..9] then Asks[0..9], 20 bytes each ...
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x40`, `Message Length`=424 (bytes 1–2 = `A8 01` little-endian), `Flags` at byte 3 |
| 4 | Instrument ID | `u32` | Instrument this book applies to |
| 8 | Source ID | `u16` | Originating source, per the [Source ID Registry](../sources/spec.md). Publishers operating a single source MAY use a fixed value (e.g., `1`). MUST NOT be `0`. |
| 10 | Book Flags | `u8` | Bit 0: bid side changed. Bit 1: ask side changed. Bit 2: book crossed or locked at capture (informational; see [Crossed and Locked Books](#crossed-and-locked-books)). Bit 3: bid side truncated. Bit 4: ask side truncated (see [Depth Truncation](#depth-truncation)). Bits 5–7 reserved, MUST be zero on emission and MUST be ignored on receipt. Bits 0/1 are informational only; see [Change Flag Semantics](#change-flag-semantics). |
| 11 | Bid Levels | `u8` | Count of populated bid levels. A publisher MUST set this in `0..10`; a subscriber MUST reject a message where it exceeds `10`. Entries `[Bid Levels..9]` in the `Bids` array are zero-filled. |
| 12 | Ask Levels | `u8` | Count of populated ask levels. A publisher MUST set this in `0..10`; a subscriber MUST reject a message where it exceeds `10`. Entries `[Ask Levels..9]` in the `Asks` array are zero-filled. |
| 13 | Reserved | 3B | Padding. A publisher MUST zero these bytes; a receiver MUST ignore them. |
| 16 | Source Timestamp | `ts_ns` | Timestamp from the originating venue for this book state |
| 24 | Bids | `Level[10]` | Best-to-worst: index 0 is the best (highest) bid, descending price. |
| 224 | Asks | `Level[10]` | Best-to-worst: index 0 is the best (lowest) ask, ascending price. |

**Level struct (20 bytes):**

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Price | `price` (`i64`) | Level price. Uses instrument's Price Exponent. |
| 8 | Quantity | `qty` (`u64`) | Aggregate resting size at this price. Uses instrument's Qty Exponent. |
| 16 | Order Count | `u16` | Number of resting orders aggregated at this level. Saturates at `0xFFFF`. `0` means the venue does not expose the count: a populated level always holds at least one order, so `0` is never a legitimate order count and the value is unambiguous. |
| 18 | Level Flags | `u16` | Bit 0: implied level. Bit 1: AMM-synthetic level (discretized curve point, not a real resting order). Bits 2–15 reserved, MUST be zero on emission and MUST be ignored on receipt. |

Offset arithmetic: `24 (prefix) + 10 × 20 (bids) + 10 × 20 (asks) = 424` bytes.

**Rules:**

- A side with no liquidity sets its level count to `0`; its entire `Level[10]` array is zeroed. An empty book is a valid message with both counts `0`.
- Populated levels are dense from index 0: there are no gaps inside `[0..count)`.
- `Bid Levels` and `Ask Levels` MUST NOT exceed `10`. The 424-byte message size bounds the *buffer*, not the count: a subscriber MUST NOT index the `Bids`/`Asks` arrays beyond index 9 no matter what count the message declares, and MUST reject a message declaring a larger count per [Frame Parsing](#frame-parsing) step 5.
- Bids are ordered by **strictly** descending price, asks by strictly ascending price: a price MUST NOT repeat within a side, since a repeated price means the level was not aggregated. Index 0 of each side is the inside market. A subscriber MUST verify this ordering on every message and treat a side that violates it as unreliable, per [Steady State](#steady-state) step 5 — the publisher requirement is not a guarantee, for the same reason bit 2 is not (see [Crossed and Locked Books](#crossed-and-locked-books)).
- `Quantity` is the aggregate across all orders resting at that price, not a single order's size. This is the defining difference from the market-by-order feed.

### Depth Truncation

`Bid Levels` and `Ask Levels` saturate at *N*, so a count of `10` is ambiguous on its own: the source book may hold exactly ten levels on that side, or many more. `Book Flags` bit 3 (bid) and bit 4 (ask) resolve it. A publisher MUST set the bit for a side when it knows the source book holds at least one price level beyond the last one carried.

The bits are **asymmetric in strength**, and consumers must read them that way:

- **Set** is a positive assertion: there is more depth than this message carries. A consumer summing `Quantity` across a side knows the total is a lower bound.
- **Clear** means *not known to be truncated* — either the side is exhausted, or the publisher's upstream itself only exposes *N* levels and cannot tell. A publisher in that position MUST leave the bit clear and MUST document the limitation out of band. A consumer MUST NOT read a clear bit as a guarantee that the side is exhausted.

The bits are per-message state, not a change flag; they carry no dependence on any previous message.

### Change Flag Semantics

`Book Flags` bits 0 and 1 are pinned to the **immediately preceding `BookDepth` this publisher emitted** for the same `(instrument, Source ID)` within the current `Reset Count` era — emitted, not received, so conflation and packet loss do not change their meaning. On the first `BookDepth` for an instrument in an era (cold start, post-reset, or first admission to the manifest) a publisher MUST set both bits, because there is no predecessor to diff against.

These bits are **informational only**. A subscriber MUST NOT gate applying the refresh on them: every `BookDepth` is a complete replacement of both sides regardless of the flags, and a receiver that has lost frames will see bits that describe a state transition it never observed. Their intended use is publisher-side observability and cheap change detection by consumers that already trust the emitted sequence.

The field is named `Book Flags`, **not** `Update Flags`, and the distinct name is normative. The top-of-book feed's `Quote` carries an `Update Flags` `u8` at the same offset 10 with incompatible bit meanings — its bit 2 is "bid gone" and bit 3 is "ask gone", where here bit 2 is crossed-or-locked and bit 3 is bid-truncated. Reusing the name would have left a same-name, same-offset, same-type field with different semantics in two feeds, which is a trap for anyone sharing a wire package or generated code across the family; the rename removes it rather than documenting it. An empty side is expressed here by a zero level count, so this feed needs no side-gone bits at all.

### L1 Consistency

`Bids[0].Price` and `Asks[0].Price` are the same inside market the L1 `Quote` carries — but only when both feeds are published **by the same publisher from the same captured book state**, identifiable by an identical `Source ID` and `Source Timestamp`. L1 and L2 conflate independently under separate operator policy, so at any given wall-clock instant the two feeds will routinely show different inside markets simply because they sampled the book at different times. Subscribers MUST NOT raise a cross-feed mismatch alarm on inequality alone; compare only messages whose `Source ID` and `Source Timestamp` match.

### Crossed and Locked Books

A publisher MUST NOT emit a crossed book (`Bids[0].Price > Asks[0].Price`) as a settled state: transient crosses during upstream reordering MUST be resolved before emission (resolve-then-emit). If a publisher's upstream forces it to relay a crossed state it cannot resolve, it MUST set `Book Flags` bit 2 and SHOULD emit the flagged book rather than withhold it — suppressing emission until the book uncrosses freezes `Source Timestamp` in a way a subscriber cannot distinguish from a dying feed, which is the worse failure. A **locked** book (`Bids[0].Price == Asks[0].Price`) is routine on some venues and is not an error; a publisher MAY set bit 2 for it but MUST NOT suppress or delay a locked book.

Subscribers MUST tolerate receiving a book with bit 2 set and treat the inside market as unreliable for that message. Bit 2 is a publisher courtesy, not a guarantee: a subscriber MUST independently check `Bids[0].Price >= Asks[0].Price` on every message with both `Bid Levels` and `Ask Levels` non-zero and treat the inside market as unreliable when it holds, regardless of whether bit 2 is set. Depth beyond index 0 remains usable in both cases.

---

## Conflation

Because each `BookDepth` is a complete refresh, the publisher owns a rate/detail tradeoff that incremental feeds cannot offer:

- A publisher MAY coalesce multiple upstream book changes into a single `BookDepth` and emit at a capped rate per instrument (e.g., 50–100 Hz), dropping every intermediate state. Correctness is preserved: a subscriber that receives only the latest refresh has a valid book, merely slightly delayed.
- A publisher MAY emit on every upstream change for latency-sensitive channels, accepting the higher frame rate.
- The conflation cadence is an operator policy, not a wire field, and is not advertised in-band in this version. Subscribers MUST NOT assume a specific cadence.

Conflation sets the *upper* bound on emission rate; the re-emission period *T* ([Publisher Behavior](#publisher-behavior) #2) sets the *lower* bound. The two are independent knobs: conflation caps how often a busy instrument is published, re-emission guarantees that even a completely idle instrument is republished, so that a lost frame cannot leave a subscriber's book wrong indefinitely.

This is the mechanism by which an L2 consumer gets a bandwidth-bounded depth view without the publisher having to choose feed detail at design time.

---

## Sequence Numbers, Gaps, and Staleness

This feed carries **no per-instrument sequence number and no snapshot recovery**, by design. Gap and staleness handling reduce to two mechanisms already present:

1. **Per-port frame sequence.** The frame-header `Sequence Number` (per channel per port) detects lost frames. A gap on `mktdata` means one or more messages were lost. Because every `BookDepth` is a full refresh, recovery is automatic: the next `BookDepth` for an affected instrument fully restores its book. A subscriber never needs to request or await a snapshot. At most it is stale on some instruments for one refresh interval or one re-emission period, whichever is longer.

   Automatic recovery depends on that next `BookDepth` actually arriving, which is why the periodic re-emission requirement in [Publisher Behavior](#publisher-behavior) #2 is load-bearing rather than a nicety. A publisher that emitted only on change would leave a quiet instrument — a thin market, a paused venue, a stub after hours — permanently wrong after a single lost frame: the book never changes, so no corrective refresh is ever produced, and the channel keeps looking healthy throughout. Nor could the subscriber notice on its own. Without a re-emission floor there is no expected per-instrument cadence, so silence on an instrument is indistinguishable from a quiet market and an arrival-based staleness check has no threshold to test against; a `Source Timestamp`-based one never fires either, since the timestamp of the last book received simply stops advancing. The re-emission floor is what supplies that threshold, converting every loss into bounded staleness.
2. **Arrival cadence and channel liveness.** A subscriber detects per-instrument staleness from the **arrival time of the last applied `BookDepth`**, measured against a small multiple of the re-emission period *T* (which the subscriber MUST be configured with — see [Re-Emission Period *T* Is Subscriber Configuration](#re-emission-period-t-is-subscriber-configuration)), and detects channel-level silence from the arrival of any `mktdata` frame.

   Per-instrument staleness MUST NOT be derived from `Source Timestamp` advancing: a re-emission republishes the unchanged timestamp by design, so a quiet-but-healthy instrument's `Source Timestamp` legitimately stands still for as long as its book does. `Source Timestamp` measures the age of the *book state*; arrival cadence measures the health of the *feed*. They answer different questions and a subscriber generally wants both.

   Channel liveness likewise MUST NOT be pinned to `Heartbeat` specifically. `Heartbeat` is emitted only when `mktdata` is otherwise idle ([Publisher Behavior](#publisher-behavior) #12), and a channel carrying a re-emission floor over any non-trivial instrument set is almost never idle, so heartbeats may be rare or absent in normal operation. Any `mktdata` frame is the liveness signal.

Neither mechanism requires book state.

### Re-Emission Period *T* Is Subscriber Configuration

*T* is **not carried on the wire in this version**. It is **REQUIRED out-of-band subscriber configuration**: an operator MUST publish the re-emission period of each channel it runs (alongside the port mapping and the multicast group), and a subscriber MUST be configured with that value before it can perform the per-instrument staleness detection described above.

This is a normative requirement, not a recommendation, because *T* is the only threshold the staleness check has. A subscriber lacking *T*:

- MUST NOT invent one. A guessed threshold is worse than none: too low and every quiet instrument is falsely flagged stale, too high and a dead instrument stays "fresh" for as long as the guess allows.
- MUST treat per-instrument staleness detection as **unavailable**, and SHOULD surface that degraded state rather than reporting healthy instruments. Channel-level liveness (arrival of any `mktdata` frame) still works and is independent of *T*.

Everything else this feed asks of a subscriber — ordering, source checking, level-count validation, book application — works without *T*. Only staleness detection depends on it, and staleness detection is what makes the re-emission recovery guarantee observable to the party that relies on it, so a deployment that leaves *T* unconfigured has a recovery mechanism it cannot verify.

A future version MAY carry *T* in band — `ManifestSummary` has two reserved bytes at offset 10 that would hold a `u16` seconds value without changing its 24-byte length — but that is a change to the shared [Reference Data Distribution supplement](../reference-data/spec.md) and therefore to every feed that adopts it, not something this spec can do alone. Until then, configuration is the contract.

A subscriber tracks, per instrument, the frame `Sequence Number` of the last `BookDepth` it applied. This feed has no *per-instrument* sequence to bootstrap, but the per-port frame sequence is the ordering discriminant in [Steady State](#steady-state) and is also how loss is measured; see that section for why `Source Timestamp` cannot carry that role.

---

## Subscriber Algorithm

Configuration (out of band, per [Re-Emission Period *T* Is Subscriber Configuration](#re-emission-period-t-is-subscriber-configuration)):

```
channel_config = {
  re_emission_period_T: duration,      // the publisher's T; REQUIRED for staleness detection
  staleness_multiple:   number = 3,    // stale when now - last_applied_at > multiple x T
  future_skew_allowance: duration = 5s // Frame Parsing step 7
}
```

State per channel:

```
channel_state = {
  reset_count:      u8   = 0,
  mktdata_seq_last: u64  = null,
  refdata_seq_last: u64  = null,
  refdata:          <reference-data supplement state>,
  instruments:      map<instrument_id, instrument_state>
}

instrument_state = {
  status: "awaiting-refdata" | "ready",
  bids:   Level[10], bid_count: u8 = 0,  // slots [bid_count..9] unused
  asks:   Level[10], ask_count: u8 = 0,
  source_id:      u16   = null,  // the one Source ID seen for this instrument this era
  last_book_seq:  u64   = null,  // frame seq of the last applied BookDepth; the ordering discriminant
  last_source_ts: ts_ns = 0,     // book-state age and regression counting; NOT an ordering gate
  last_applied_at: ts_ns = 0     // local arrival time of the last applied BookDepth; drives staleness
}
```

### Frame Parsing

Before any message-level handling, for each received datagram:

1. **Bound every later step by the bytes actually received, not by a declared length.** Discard a datagram shorter than the 24-byte frame header before reading any field. Then validate `Frame Length`: it MUST be `>= 24` and MUST NOT exceed the received datagram length — a frame declaring more bytes than arrived is malformed (count it, discard it, keep the channel). A `Frame Length` *shorter* than the datagram is trailing padding: parse only the first `Frame Length` bytes and count the discrepancy. Every bound below is taken against this validated length. Without this step the frame-walk ceiling in step 3 rests on a number the sender controls.
2. Validate the frame header `Magic` equals `0x4442`; discard the datagram if it does not match. Then validate `Schema Version`: it MUST be `2`, and a subscriber MUST discard (and count) a frame declaring anything else rather than attempt the walk in step 3. A `Schema Version` of `1` on this feed is a publisher defect — under the 0.1.x layout `BookDepth` cannot be expressed at all, and `Flags` sits at a different offset.
3. Walk the frame's application messages using each `Message Length`, read as the contiguous 12-bit field of [Application Message Header](#application-message-header-4-bytes) (`read_u16_le(byte[1..2]) & 0x0FFF`) — **not** from byte 1 alone, which would read a 424-byte `BookDepth` as 168 bytes and desynchronize the rest of the frame. Skip unknown Type IDs by length. A `Message Length` that is `< 4`, exceeds the bytes remaining in the frame, or is inconsistent with `Frame Length` makes the frame malformed: stop parsing that frame, count it, and keep the channel — do not fail or reset. This guard is not optional. Without the `< 4` floor a `Message Length` of `0` advances the walk by zero bytes and spins forever on a single malformed or misrouted datagram; without the remaining-bytes ceiling an oversized length reads past the datagram. This matches the sibling [Order-Intent Feed](../order-intent/spec.md) parse rule, with the datagram-length validation of step 1 added and the widened length field.
4. **Validate `Message Count`.** The walk MUST yield exactly the frame header's `Message Count` messages and MUST end exactly at `Frame Length`. A mismatch in either direction makes the frame malformed: discard the whole frame, count it, and keep the channel. `Message Count` is not advisory — it is the frame's own statement of how many messages it contains, so a walk that yields a different number means the frame is not what its header says it is, and the messages already walked cannot be trusted individually. Reject the frame **before** applying any of its `BookDepth` messages, not after. A `Message Count` of `0` is invalid: every frame carries at least one message.
5. Reject (drop and count) any `BookDepth` whose `Bid Levels` or `Ask Levels` exceeds `10`, before touching the level arrays. The declared count indexes fixed 10-element arrays; the 424-byte message length constrains the buffer but not the count field, so a frame claiming `Bid Levels = 200` would drive a literal implementation past the end of `Bids`. A subscriber MUST NOT index either array beyond index 9 under any circumstances.
6. Reject (drop and count) any `BookDepth` whose `Source ID` is `0`, which the [Source ID Registry](../sources/spec.md) reserves and forbids on the wire.
7. Reject (drop and count) any `BookDepth` whose `Source Timestamp` exceeds the local receive time by more than a bounded skew allowance (operator-configured; recommended 5 s, sized to cover ordinary venue clock offset). A far-future timestamp — an upstream NTP step, or a replayed or hostile datagram — is not an ordering hazard (ordering runs on the frame `Sequence Number`; see [Steady State](#steady-state) step 2), but it does poison every timestamp-derived signal: it makes the instrument look permanently fresh to the per-instrument staleness check in [Sequence Numbers, Gaps, and Staleness](#sequence-numbers-gaps-and-staleness), so a genuinely dying feed stops tripping it, and it corrupts any publisher-to-subscriber latency measurement. It is also the only guard on the first `BookDepth` of an era, which has no predecessor sequence to be checked against. No lower bound is applied here; a stale-but-plausible timestamp is handled by the ordering rule and counted as a regression.

### Cold Start

1. Bind both ports. On the first frame from any port, record `reset_count` and initialise per-port `seq_last`.
2. Build reference-data state per the [Reference Data Distribution supplement](../reference-data/spec.md). As each `InstrumentDefinition` arrives under the current `Manifest Seq`, the corresponding instrument moves from `awaiting-refdata` to `ready`. Instruments not yet in the manifest are ignored.
3. Process each `BookDepth` for a `ready` instrument exactly as in [Steady State](#steady-state) — the source check, the ordering check, wholesale replacement, and the watermark update all apply from the very first message. Cold start is not a distinct processing mode: it is Steady State with empty per-instrument state, and the era's-first-message cases are handled there. Discard `BookDepth` for instruments not in the current manifest.

   What cold start does *not* need is a bootstrap barrier: no buffering, no replay, and no waiting for a snapshot before the first refresh becomes usable, because each `BookDepth` is itself complete.
4. An instrument is immediately usable the first time a `BookDepth` for it is applied. There is no channel-wide bootstrap barrier; readiness is per instrument on first refresh.

### Steady State

For each `BookDepth` that passed [Frame Parsing](#frame-parsing), arriving for instrument `I`, in this order:

1. **Check source.** If `I.source_id` is set and differs from the message's `Source ID`, count it and stop; otherwise set `I.source_id` (see [Single Source per Instrument](#single-source-per-instrument)).
2. **Check ordering, before applying anything.** The frame `Sequence Number` is the **sole** ordering discriminant:
   - if `I.last_book_seq` is set and the frame `Sequence Number` is `<= I.last_book_seq`, discard the message;
   - otherwise apply it — **including when its `Source Timestamp` is older than `I.last_source_ts`**. A timestamp regression under an advancing sequence means the venue's clock stepped backwards or the upstream re-timestamped; the later frame still carries the later book, so a subscriber MUST apply it and SHOULD count the regression. Gating on the timestamp here would freeze the instrument until wall-clock caught up — the same permanent-staleness failure the re-emission floor exists to prevent.

   A re-emission ([Publisher Behavior](#publisher-behavior) #2) carries an unchanged `Source Timestamp` but a newer frame `Sequence Number`, so it passes and is applied — which is the point of it.

   `Source Timestamp` is deliberately **not** a second gate. It cannot order a reordered pair in the first place: a venue publishing at millisecond resolution emits equal ns-padded timestamps for both, so the older book would be applied last and, with no further updates, stick. Its role here is staleness detection and regression counting only. The frame `Sequence Number` — per channel per port, monotonic within a `Reset Count` era, and single-host per [Single Publisher Host per Channel](#single-publisher-host-per-channel) — carries ordering alone.

   On the era's first `BookDepth` for `I` there is no `last_book_seq` to compare against and the message is applied as-is; the bounded future-skew guard in [Frame Parsing](#frame-parsing) step 7 is what protects that first application. On a `Reset Count` change all of this state is discarded with the rest of the channel state, so no comparison ever spans eras.
3. **Apply.** Replace `I.bids`/`I.asks` and `I.bid_count`/`I.ask_count` wholesale with the message's level arrays and counts. Nothing from the previous book survives.
4. **Then update the watermarks.** Set `I.last_book_seq`, `I.last_source_ts`, and `I.last_applied_at` from this message. Updating these before step 2's comparison would compare the message against itself, so the check could never fire and a stale datagram would already have been applied.
5. **Check the book.** Treat the inside market as unreliable if `Bid Levels` and `Ask Levels` are both non-zero and `Bids[0].Price >= Asks[0].Price`, or if `Book Flags` bit 2 is set (see [Crossed and Locked Books](#crossed-and-locked-books)). Independently, verify that each populated side is strictly monotonic in price — descending for bids, ascending for asks — and treat a side that is not as unreliable and count it; an unsorted or duplicate-price side breaks every cumulative-depth and VWAP computation, and like bit 2 the publisher requirement is not something the wire enforces.

Out-of-order full refreshes are safe to drop because a newer one supersedes them; `last_book_seq` is load-bearing for this ordering check, and additionally useful for measuring loss.

A publisher that violates [Publisher Behavior](#publisher-behavior) #10 by packing two `BookDepth` messages for the same instrument into one frame produces two messages with the same frame `Sequence Number`. Step 2 then keeps the **first** and discards the second, which is the older state of the two. A subscriber SHOULD count this case separately from ordinary reorder drops, since it is a publisher defect with a silent wrong-book outcome rather than a network artifact.

### Gap, Reset, and Manifest Handling

- **`mktdata` frame gap:** no action required beyond noting loss. The next `BookDepth` per instrument restores state, within one refresh interval or one re-emission period *T*, whichever is longer. There is no `gap` status and no recovery flow. A subscriber configured with *T* MAY treat an instrument whose last applied `BookDepth` is older than a small multiple of it as stale, since the publisher's re-emission floor means silence that long is a fault, not a quiet market; a subscriber without *T* cannot make this determination at all (see [Re-Emission Period *T* Is Subscriber Configuration](#re-emission-period-t-is-subscriber-configuration)).
- **`Reset Count` change on any port:** discard all channel state and restart from Cold Start.
- **`Manifest Seq` change on `refdata`:** per the supplement — drop instruments no longer in the manifest, admit new ones (they become `ready` on their first `BookDepth`), retain surviving instruments' books.
- **`EndOfSession` on `mktdata`:** the publisher has stopped emitting for this session. A subscriber MUST mark every instrument's book stale at that point and MUST NOT continue serving the last-known books as live; whether it clears them or retains them as last-known-values is an application policy. Books do not resume on their own: the next session arrives with an incremented `Reset Count`, which discards channel state and restarts from Cold Start. A subscriber that keeps consuming after `EndOfSession` without marking staleness will serve an indefinitely frozen book with no signal that anything is wrong.

---

## Publisher Behavior

A publisher operating the `mktdata` port MUST:

1. For each instrument, maintain the current top-*N* aggregated book and emit it as a `BookDepth` whenever it changes, subject to the operator's conflation cadence.
2. **Re-emit each active instrument's current `BookDepth` at least once every re-emission period *T*, even when the book has not changed** (recommended *T* = 30 s, aligned with the definition cycle), and **publish the channel's *T* out of band** so subscribers can be configured with it, per [Re-Emission Period *T* Is Subscriber Configuration](#re-emission-period-t-is-subscriber-configuration). This is the feed's only loss-recovery mechanism: without it, a `BookDepth` lost to a dropped frame on an instrument that then stops changing is never corrected; without the published *T*, subscribers cannot detect when it has failed. A re-emission repeats the previous message's payload — the same levels, counts and unchanged `Source Timestamp` — with `Book Flags` bits 0 and 1 clear; bits 2, 3 and 4 describe the current state at re-emission time and MAY therefore differ from the message being repeated. Re-emissions MAY be paced across *T* to spread the load rather than bursting the whole instrument set at once. An instrument is *active* while it is in the current manifest; a publisher MUST NOT re-emit for instruments dropped from the manifest.
3. Order bid levels by strictly descending price and ask levels by strictly ascending price, dense from index 0, with no price repeated within a side, and set `Bid Levels`/`Ask Levels` to the populated counts. Zero-fill unused level slots.
4. Aggregate `Quantity` across all resting orders at each price, and set `Order Count` when the venue exposes it (else `0`), saturating at `0xFFFF`.
5. Resolve transient crossed books before emission, or set `Book Flags` bit 2 per [Crossed and Locked Books](#crossed-and-locked-books).
6. Set application-header Flags bit 0 (snapshot) to `1` on every `BookDepth`.
7. Set `Book Flags` bits 0/1 relative to the immediately preceding emitted `BookDepth` for the same `(instrument, Source ID)` in this `Reset Count` era, setting both on the era's first message, per [Change Flag Semantics](#change-flag-semantics).
8. Set `Book Flags` bits 3/4 per [Depth Truncation](#depth-truncation) when the source book is known to hold levels beyond those carried, and leave them clear otherwise. Zero the reserved bits (5–7) and the 3 reserved bytes at offset 13.
9. Emit at most one `Source ID` per `(channel, instrument)`, never `0`, per [Single Source per Instrument](#single-source-per-instrument).
10. Pack at most one `BookDepth` per instrument into any one frame. Two refreshes of the same instrument would share a frame `Sequence Number`, which the subscriber's ordering check ([Steady State](#steady-state) step 2) uses as its discriminant — the subscriber would keep the first and silently discard the later, newer one. Conflation makes compliance free, since only the latest state need ever be emitted.
11. Emit exactly one sequence series per `(channel, port)` from exactly one host, per [Single Publisher Host per Channel](#single-publisher-host-per-channel).
12. Emit `Heartbeat` on `mktdata` at the operator-defined heartbeat interval (recommended 1 s) when otherwise idle. Note that a channel meeting the re-emission floor over a non-trivial instrument set is rarely idle, so heartbeats are a fallback signal, not the primary liveness indicator.
13. Emit `Trade` (and `Liquidation` where applicable) on `mktdata` when the upstream has a venue-level trade concept. Trades are independent of `BookDepth` and are not required to reconstruct the book.
14. Set frame `Schema Version` to `2` on both ports of the channel, and encode every `Message Length` per the contiguous 12-bit rule of [Application Message Header](#application-message-header-4-bytes) — `BookDepth` as bytes 1–2 = `A8 01`, every other message type with the length's high bits zero — and zero the reserved top 4 bits.
15. Set frame `Message Count` to the exact number of application messages packed into the frame, and `Frame Length` to the exact total byte count. Subscribers validate the message walk against both and discard frames that disagree ([Frame Parsing](#frame-parsing) step 4).

A publisher operating the `refdata` port MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md), identical to the sibling feeds.

For AMM sources, the publisher discretizes the curve into up to *N* synthetic levels per side, sets `Level Flags` bit 1 (`AMM-synthetic`) on each, and chooses level spacing per a documented, source-specific rule (e.g., fixed tick multiples out from mid). The discretization rule is out of scope for this spec and MUST be documented out of band.

---

## Session Lifecycle

1. Publisher starts → increments `Reset Count`, resets `Sequence Number` to 0 on both ports.
2. Begins emitting `InstrumentDefinition` on `refdata`, paced across the definition cycle (recommended 30 s).
3. Begins emitting `ManifestSummary` with `Valid = 1` on `refdata` at the manifest cadence (recommended 1 s).
4. Begins emitting `BookDepth` on `mktdata` as books change, subject to conflation cadence, and re-emits each active instrument's current book at least every *T* (recommended 30 s) whether or not it changed. Emits `Heartbeat` when idle.
5. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, emits an updated `ManifestSummary`.
6. On shutdown → emits `EndOfSession` on `mktdata`.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency and Bandwidth

At `N = 10`, one `BookDepth` is **424 bytes** of application payload, or 448 bytes including the frame header for a single-message frame. Two `BookDepth` messages plus the frame header is 872 bytes, within the 1,232-byte MTU, leaving 360 bytes for a `Trade`, a `Liquidation`, or a `Heartbeat` alongside them.

Per-instrument bandwidth is governed by the conflation cadence, because each refresh is a fixed 424 bytes regardless of how much changed:

- At a 100 Hz per-instrument cap: `424 B × 100 ≈ 42.4 KB/s ≈ 339 Kbps` per actively-updating instrument.
- At 50 Hz: about 170 Kbps per instrument.
- A quiet instrument costs proportionally less, with a floor set by the re-emission period: at *T* = 30 s that floor is `424 / 30 ≈ 14.1 B/s ≈ 113 bps` per idle instrument. Even 10,000 idle instruments re-emit at only about 1.1 Mbps aggregate, so the recovery guarantee remains effectively free.

For a channel of *M* actively-updating instruments at cadence *f* Hz, aggregate `≈ M × 424 × f` bytes/s. Sharding across channels divides *M* per channel. Because the message is fixed-size, deep-book venues and shallow-book venues cost the same per refresh; the lever is *f*, not book depth.

**What ten levels cost.** Against the *N* = 5 shape this feed was first drafted with, both bandwidth and datagram count roughly double: 424 bytes per refresh instead of 224 (1.9×), and two instruments per frame instead of five, so a given instrument set needs about 2.5× as many datagrams at the same cadence. The packing loss is the sharper of the two, since datagram rate — not bit rate — is what a receiving NIC and kernel feel. Both are bounded and both are the price of the depth the consumers require; the levers for an operator that finds either too high are the conflation cadence *f* and channel sharding, not *N*, which is fixed by this specification.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Relationship to Sibling Feeds

Sibling of the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md). Shared:

- The 24-byte frame header layout (except the `Magic` value).
- The 4-byte application message header at **common framing 0.2.0**, whose contiguous 12-bit `Message Length` this feed is the only one to need (`BookDepth` is 424 bytes) and whose `u8` `Flags` at offset 3 every sibling shares; see the canonical [Application Message Header](../top-of-book/spec.md#application-message-header-4-bytes).
- The [Reference Data Distribution supplement](../reference-data/spec.md), including `InstrumentDefinition` (`0x02`) and `ManifestSummary` (`0x07`).
- The cross-spec Type IDs `0x01`, `0x04`, `0x06`, `0x07`, `0x08` byte-for-byte.
- The session-lifecycle and `Reset Count` patterns and the forward-compatibility rules.

Distinctions of this feed:
- `Magic` is `0x4442` (vs. `0x445A` top-of-book, `0x4444` market-by-order, `0x4D44` midpoint, `0x4450` perp-stats, `0x494F` order-intent).
- **Two-port** channel model (`mktdata` + `refdata`); no snapshot port, because refreshes are self-contained.
- **Stateless full-refresh** delivery: no per-instrument sequence, no snapshot/delta recovery, no book maintenance required of the subscriber. Loss is repaired by the next refresh, with periodic re-emission bounding how long that takes. Consistency vs. L1: `BookDepth` level 0 equals the `Quote` BBO for the same source and `Source Timestamp` (see [L1 Consistency](#l1-consistency)); the two feeds conflate independently, so they are not instant-by-instant equal. Relationship to L3: `BookDepth` is the price-aggregated projection of the same book the market-by-order feed carries order-by-order, truncated to *N* levels per side — a consumer that needs the untruncated book uses the market-by-order feed and aggregates client-side.
- **One publisher host per `(channel, port)`**, a stricter deployment constraint than the order-intent feed's, because the frame `Sequence Number` orders book application here rather than serving only as a health signal (see [Single Publisher Host per Channel](#single-publisher-host-per-channel)).
- L2-specific payload lives at `0x40` (`BookDepth`), with `0x41` reserved and unused. The `0x40`–`0x4F` range does not overlap any sibling feed.
- **The only feed that needs the widened `Message Length`.** Every feed declares `Schema Version = 2` because 0.2.0 moved `Flags`, but this is the one whose messages exceed 255 bytes, so a 0.1.x decoder pointed at it desynchronizes the frame walk rather than merely misreading `Flags`.
- **Deeper by design**: ten price levels per side, against one for top-of-book and one derived price for midpoint. The depth costs packing density — two instruments per frame — which is the explicit trade recorded in [Wire Efficiency and Bandwidth](#wire-efficiency-and-bandwidth).

A publisher MAY operate any subset of the sibling feeds for the same instruments simultaneously. Subscribers MAY consume any subset independently.

---

## Versioning and Forward Compatibility

This document is version **0.1.0**, versioned independently of the sibling feed specs. It requires **common framing 0.2.0** (a contiguous 12-bit `Message Length` at offset 1 and a `u8` `Flags` at offset 3), and the Schema Version byte in the frame header is `2` for this release — see [Application Message Header](#application-message-header-4-bytes). Future versions MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared length).
- Define new message types in reserved type-ID ranges (old decoders skip unknown types via the length field).
- Define new values for enumerated fields and new `Book Flags` / `Level Flags` bits. Decoders MUST accept any value and ignore unknown bits.
- **Raise or reduce** the depth constant *N*, which changes `BookDepth`'s length and therefore requires a Schema Version bump. Under 0.2.0 framing the ceiling is the datagram budget: `24 + 40N ≤ 1208` at the 1,232-byte maximum frame size admits *N* ≤ 29. A deeper book is a longer `0x40`, not a new message type — which is why `0x41` is reserved but unused. Raising *N* costs packing density and per-instrument bandwidth proportionally (see [Wire Efficiency and Bandwidth](#wire-efficiency-and-bandwidth)), so it should follow a stated consumer requirement rather than available headroom.
- Carry the re-emission period *T* in band, by defining the two reserved bytes at `ManifestSummary` offset 10 as a `u16` seconds value. That is a change to the shared [Reference Data Distribution supplement](../reference-data/spec.md) and belongs to the family, not to this spec; until it happens *T* is required subscriber configuration (see [Re-Emission Period *T* Is Subscriber Configuration](#re-emission-period-t-is-subscriber-configuration)).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.
