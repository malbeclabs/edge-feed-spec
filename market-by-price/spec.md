# DoubleZero Market-by-Price Feed

The DoubleZero Market-by-Price Feed is a wire format for L2 aggregated-depth book data delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing the top *N* price levels of each side of an instrument's book, aggregated by price, from any venue with an order book.

This is a sibling protocol to the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md), not a layer on top. Where the top-of-book feed carries a single best bid/ask level per instrument and the market-by-order feed carries the full resting-order population, this feed carries a fixed-depth, price-aggregated view: the best *N* levels per side, each level a `(price, aggregate size, order count)` tuple.

The central design decision, and the thing that makes this the *easy* tier, is that **every message is a complete, self-contained refresh of the top *N* levels**. There is no incremental delta stream, no snapshot port, no per-instrument sequence bootstrap, and no book state for the subscriber to maintain. A subscriber reads the latest `BookDepth` for an instrument and has the whole top-*N* book. This is a deliberate departure from the market-by-order feed, which is stateful by necessity because order-level state cannot fit in a single datagram. A price-aggregated top-*N* book does fit, so the entire snapshot/delta recovery apparatus becomes unnecessary here.

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

The number of price levels carried per side, *N*, is fixed by this specification at **`N = 5`**. `BookDepth` is therefore a constant-length message (224 bytes), which fits within the `u8` `Message Length` cap of the shared application message header — so this feed reuses the sibling feeds' 4-byte application header verbatim, with no wire changes.

The number of levels actually populated on each side is carried per-message in the `Bid Levels` and `Ask Levels` fields (`0..N`); levels beyond the populated count are zero-filled. A shallow book (fewer than *N* levels resting) is represented by populating fewer levels, not by a different message.

A subscriber that needs more than *N* levels of depth is not an L2 consumer; that requirement is served by the [Market-by-Order Feed](../market-by-order/spec.md) with client-side aggregation. A deeper `BookDepth` variant would exceed the `u8` `Message Length` cap and needs a framing accommodation not defined in this version; the type ID `0x41` is reserved for it. Do not add it speculatively.

---

## Transport Framing

One UDP datagram = one frame. Frames do not span packet boundaries. Multiple application messages may be packed into a single frame. The maximum frame size is **1,232 bytes** to leave room for GRE encapsulation headers used by the DoubleZero network's last-mile delivery. At `N = 5` a `BookDepth` message is 224 bytes, so up to five fit in one frame alongside the frame header.

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
| 2 | Schema Version | `u8` | Protocol version. Starts at `1`. |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. |
| 4 | Sequence Number | `u64` | Monotonically increasing **per channel per port**, starting from 0. Resets to 0 when `Reset Count` changes. Used for per-port gap detection. The `mktdata` and `refdata` ports each have an independent series. |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value. Shared across both ports of the channel. |
| 22 | Frame Length | `u16` | Total frame length in bytes, including this header. |

---

## Application Message Header (4 bytes)

Every application message begins with the sibling feeds' standard 4-byte header, unchanged:

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Message Type | `u8` | See Message Types table. |
| 1 | Message Length | `u8` | Total message length including this header. Max 255. `BookDepth` at `N = 5` is 224 bytes, within this cap. |
| 2 | Flags | `u16` | Bit 0: snapshot (1) vs. incremental (0). Bits 1–15: reserved. Every `BookDepth` is a full refresh, so publishers SHOULD set bit 0 to `1`. |

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
| `0x40` | BookDepth | 224 | mktdata | Fixed-depth top-*N* aggregated book, both sides. The core L2 message. |
| `0x41` | *(reserved)* | — | — | Reserved for a future deeper `BookDepth` variant. Not defined in this version. |

A decoder encountering an unknown type MUST skip the message using its Message Length field and continue parsing the frame.

### Cross-Spec Type ID Policy

A message Type ID that appears in more than one sibling feed MUST carry the same semantic meaning in each. The shared Type IDs are `0x01` (Heartbeat), `0x02` (InstrumentDefinition), `0x04` (Trade), `0x06` (EndOfSession), `0x07` (ManifestSummary), and `0x08` (Liquidation). Heartbeat, EndOfSession, and ManifestSummary are byte-for-byte identical across every sibling that carries them. Trade and Liquidation are byte-for-byte identical between the top-of-book feed, the market-by-order feed, and this feed. `InstrumentDefinition` shares the Type ID and the 80-byte layout with the top-of-book and market-by-order feeds.

This feed's own payload Type IDs live in the **`0x40`–`0x4F` range**, which does not overlap any sibling feed. This is a deliberate choice: `0x10`/`0x11` are the market-by-order feed's `OrderAdd`/`OrderCancel`, `0x20`–`0x22` are its snapshot messages, and `0x30`–`0x34` are used by the perp-stats and order-intent feeds. A Type ID used by one sibling for a given payload MUST NOT be reassigned to a different payload here; placing `BookDepth` at `0x40` keeps this feed strictly additive to the family's type-ID allocation. The sibling Type IDs this feed does not carry — `0x03` (Quote/Midpoint) and `0x05` — are reserved here, never reassigned, so a misrouted sibling frame is rejected rather than mis-decoded.

---

## Identity Model

The unique key for an instrument is the tuple **`(channel_id, instrument_id)`**. `instrument_id` is a `u32` scoped to its channel; it need not be globally unique across channels. Subscribers consuming multiple channels MUST key their internal instrument map by the tuple. Channel sharding across multiple publisher instances is supported natively via `Channel ID` in the frame header, exactly as in the sibling feeds; grouping and discovery are deployment concerns and out of scope.

---

## Message Definitions

### 0x01 Heartbeat (16 bytes)

Inherited from the top-of-book feed. Sent every N seconds on the `mktdata` port when there is no other traffic. Receivers use this for stale-connection detection.

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

Publishers SHOULD use the most accurate value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown). For an AMM instrument, the publisher discretizes the curve into up to *N* synthetic price levels; see the `AMM-synthetic` level flag in [0x40 BookDepth](#0x40-bookdepth-224-bytes).

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

### 0x40 BookDepth (224 bytes)

The core message. A single, fixed-size, two-sided, top-*N* aggregated-depth refresh. Self-contained: it carries the entire top-*N* state of both sides at the source timestamp, and stands alone with no dependence on prior messages.

**Fixed prefix (24 bytes):**

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (0x40)  |  Length (224) |            Flags              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Instrument ID (u32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Source ID (u16)       |  Update Flags |  Bid Levels   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Ask Levels   |            Reserved (3 bytes)                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Source Timestamp (ts_ns)                    |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   ... followed by Bids[0..4] then Asks[0..4], 20 bytes each ...
```

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x40`, Length=224 |
| 4 | Instrument ID | `u32` | Instrument this book applies to |
| 8 | Source ID | `u16` | Originating source. Publishers operating a single source MAY use a fixed value (e.g., `1`). |
| 10 | Update Flags | `u8` | Bit 0: bid side changed since previous `BookDepth` for this instrument. Bit 1: ask side changed. Bit 2: book crossed/locked at capture (informational; see Crossed and Locked Books). Bits 3–7 reserved. |
| 11 | Bid Levels | `u8` | Count of populated bid levels, `0..5`. Entries `[Bid Levels..4]` in the `Bids` array are zero-filled. |
| 12 | Ask Levels | `u8` | Count of populated ask levels, `0..5`. Entries `[Ask Levels..4]` in the `Asks` array are zero-filled. |
| 13 | Reserved | 3B | Padding |
| 16 | Source Timestamp | `ts_ns` | Timestamp from the originating venue for this book state |
| 24 | Bids | `Level[5]` | Best-to-worst: index 0 is the best (highest) bid, descending price. |
| 124 | Asks | `Level[5]` | Best-to-worst: index 0 is the best (lowest) ask, ascending price. |

**Level struct (20 bytes):**

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Price | `price` (`i64`) | Level price. Uses instrument's Price Exponent. |
| 8 | Quantity | `qty` (`u64`) | Aggregate resting size at this price. Uses instrument's Qty Exponent. |
| 16 | Order Count | `u16` | Number of resting orders aggregated at this level. `0` if the venue does not expose it. |
| 18 | Level Flags | `u16` | Bit 0: implied level. Bit 1: AMM-synthetic level (discretized curve point, not a real resting order). Bits 2–15 reserved. |

Offset arithmetic: `24 (prefix) + 5 × 20 (bids) + 5 × 20 (asks) = 224` bytes.

**Rules:**

- A side with no liquidity sets its level count to `0`; its entire `Level[5]` array is zeroed. An empty book is a valid message with both counts `0`.
- Populated levels are dense from index 0: there are no gaps inside `[0..count)`.
- Bids are ordered by descending price, asks by ascending price. Index 0 of each side is the inside market. `Bids[0].Price` and `Asks[0].Price` together equal the L1 `Quote` best bid/ask for the same source and timestamp; the two feeds are consistent by construction.
- `Quantity` is the aggregate across all orders resting at that price, not a single order's size. This is the defining difference from the market-by-order feed.

### Crossed and Locked Books

A publisher MUST NOT emit a crossed book (`Bids[0].Price > Asks[0].Price`) as a settled state: transient crosses during upstream reordering MUST be resolved before emission (resolve-then-emit). If a publisher's upstream forces it to relay a crossed or locked (`Bids[0].Price == Asks[0].Price`) state it cannot resolve, it MUST set `Update Flags` bit 2 and SHOULD suppress emission until the book uncrosses if suppression is viable at its cadence. Subscribers MUST tolerate receiving a book with bit 2 set and treat the inside market as unreliable for that message.

---

## Conflation

Because each `BookDepth` is a complete refresh, the publisher owns a rate/detail tradeoff that incremental feeds cannot offer:

- A publisher MAY coalesce multiple upstream book changes into a single `BookDepth` and emit at a capped rate per instrument (e.g., 50–100 Hz), dropping every intermediate state. Correctness is preserved: a subscriber that receives only the latest refresh has a valid book, merely slightly delayed.
- A publisher MAY emit on every upstream change for latency-sensitive channels, accepting the higher frame rate.
- The conflation cadence is an operator policy, not a wire field, and is not advertised in-band in this version. Subscribers MUST NOT assume a specific cadence.

This is the mechanism by which an L2 consumer gets a bandwidth-bounded depth view without the publisher having to choose feed detail at design time.

---

## Sequence Numbers, Gaps, and Staleness

This feed carries **no per-instrument sequence number and no snapshot recovery**, by design. Gap and staleness handling reduce to two mechanisms already present:

1. **Per-port frame sequence.** The frame-header `Sequence Number` (per channel per port) detects lost frames. A gap on `mktdata` means one or more messages were lost. Because every `BookDepth` is a full refresh, recovery is automatic: the next `BookDepth` for an affected instrument fully restores its book. A subscriber never needs to request or await a snapshot. At most it is stale on some instruments for one refresh interval.
2. **Source Timestamp and Heartbeat.** A subscriber detects staleness per instrument by watching `Source Timestamp` advance, and detects channel-level silence via `Heartbeat` on `mktdata`. Neither requires book state.

A subscriber MAY track, per instrument, the frame `Sequence Number` of the last `BookDepth` it applied, purely for observability (measuring loss). It is not load-bearing for correctness.

---

## Subscriber Algorithm

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
  book:   { bids: Level[0..5], asks: Level[0..5] },
  last_book_seq: u64 = null,   // frame seq of last applied BookDepth (observability only)
  last_source_ts: ts_ns = 0
}
```

### Cold Start

1. Bind both ports. On the first frame from any port, record `reset_count` and initialise per-port `seq_last`.
2. Build reference-data state per the [Reference Data Distribution supplement](../reference-data/spec.md). As each `InstrumentDefinition` arrives under the current `Manifest Seq`, the corresponding instrument moves from `awaiting-refdata` to `ready`. Instruments not yet in the manifest are ignored.
3. On each `BookDepth` for a `ready` instrument, replace that instrument's book wholesale with the message contents. There is no buffering, replay, or ordering dependency. Discard `BookDepth` for instruments not in the current manifest.
4. An instrument is immediately usable the first time a `BookDepth` for it is applied. There is no channel-wide bootstrap barrier; readiness is per instrument on first refresh.

### Steady State

For each `BookDepth` arriving for instrument `I`:
- Replace `I.book` with the message's bid/ask arrays, truncated to `Bid Levels`/`Ask Levels`.
- Set `I.last_source_ts` and `I.last_book_seq`.
- If the message's `Source Timestamp` is older than `I.last_source_ts` (a reordered/stale datagram), the subscriber MAY discard it. Out-of-order full refreshes are safe to drop because a newer one supersedes them.

### Gap, Reset, and Manifest Handling

- **`mktdata` frame gap:** no action required beyond noting loss. The next `BookDepth` per instrument restores state. There is no `gap` status and no recovery flow.
- **`Reset Count` change on any port:** discard all channel state and restart from Cold Start.
- **`Manifest Seq` change on `refdata`:** per the supplement — drop instruments no longer in the manifest, admit new ones (they become `ready` on their first `BookDepth`), retain surviving instruments' books.

---

## Publisher Behavior

A publisher operating the `mktdata` port MUST:

1. For each instrument, maintain the current top-*N* aggregated book and emit it as a `BookDepth` whenever it changes, subject to the operator's conflation cadence.
2. Order bid levels by descending price and ask levels by ascending price, dense from index 0, and set `Bid Levels`/`Ask Levels` to the populated counts. Zero-fill unused level slots.
3. Aggregate `Quantity` across all resting orders at each price, and set `Order Count` when the venue exposes it (else `0`).
4. Resolve transient crossed books before emission, or set `Update Flags` bit 2 per [Crossed and Locked Books](#crossed-and-locked-books).
5. Set application-header Flags bit 0 (snapshot) to `1` on every `BookDepth`.
6. Emit `Heartbeat` on `mktdata` every N seconds when otherwise idle (recommended 1 s).
7. Emit `Trade` (and `Liquidation` where applicable) on `mktdata` when the upstream has a venue-level trade concept. Trades are independent of `BookDepth` and are not required to reconstruct the book.

A publisher operating the `refdata` port MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md), identical to the sibling feeds.

For AMM sources, the publisher discretizes the curve into up to *N* synthetic levels per side, sets `Level Flags` bit 1 (`AMM-synthetic`) on each, and chooses level spacing per a documented, source-specific rule (e.g., fixed tick multiples out from mid). The discretization rule is out of scope for this spec and MUST be documented out of band.

---

## Session Lifecycle

1. Publisher starts → increments `Reset Count`, resets `Sequence Number` to 0 on both ports.
2. Begins emitting `InstrumentDefinition` on `refdata`, paced across the definition cycle (recommended 30 s).
3. Begins emitting `ManifestSummary` with `Valid = 1` on `refdata` at the manifest cadence (recommended 1 s).
4. Begins emitting `BookDepth` on `mktdata` as books change, subject to conflation cadence. Emits `Heartbeat` when idle.
5. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, emits an updated `ManifestSummary`.
6. On shutdown → emits `EndOfSession` on `mktdata`.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Wire Efficiency and Bandwidth

At `N = 5`, one `BookDepth` is **224 bytes** of application payload, or 248 bytes including the frame header for a single-message frame. Five `BookDepth` messages plus the frame header is 1,144 bytes, within the 1,232-byte MTU.

Per-instrument bandwidth is governed by the conflation cadence, because each refresh is a fixed 224 bytes regardless of how much changed:

- At a 100 Hz per-instrument cap: `224 B × 100 ≈ 22.4 KB/s ≈ 179 Kbps` per actively-updating instrument.
- At 50 Hz: about 90 Kbps per instrument.
- A quiet instrument emits only on change and costs proportionally less.

For a channel of *M* actively-updating instruments at cadence *f* Hz, aggregate `≈ M × 224 × f` bytes/s. Sharding across channels divides *M* per channel. Because the message is fixed-size, deep-book venues and shallow-book venues cost the same per refresh; the lever is *f*, not book depth.

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path.

---

## Relationship to Sibling Feeds

Sibling of the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md). Shared:

- The 24-byte frame header layout (except the `Magic` value).
- The 4-byte application message header, unchanged (`BookDepth` fits the `u8` `Message Length` cap at `N = 5`).
- The [Reference Data Distribution supplement](../reference-data/spec.md), including `InstrumentDefinition` (`0x02`) and `ManifestSummary` (`0x07`).
- The cross-spec Type IDs `0x01`, `0x04`, `0x06`, `0x07`, `0x08` byte-for-byte.
- The session-lifecycle and `Reset Count` patterns and the forward-compatibility rules.

Distinctions of this feed:
- `Magic` is `0x4442` (vs. `0x445A` top-of-book, `0x4444` market-by-order, `0x4D44` midpoint, `0x4450` perp-stats, `0x494F` order-intent).
- **Two-port** channel model (`mktdata` + `refdata`); no snapshot port, because refreshes are self-contained.
- **Stateless full-refresh** delivery: no per-instrument sequence, no snapshot/delta recovery, no book maintenance required of the subscriber. Consistency vs. L1: `BookDepth` level 0 equals the `Quote` BBO. Relationship to L3: `BookDepth` is the price-aggregated projection of the same book the market-by-order feed carries order-by-order.
- L2-specific payload lives at `0x40` (`BookDepth`), with `0x41` reserved for a future deeper variant. The `0x40`–`0x4F` range does not overlap any sibling feed.

A publisher MAY operate any subset of the sibling feeds for the same instruments simultaneously. Subscribers MAY consume any subset independently.

---

## Versioning and Forward Compatibility

This document is version **0.1.0**, versioned independently of the sibling feed specs. The Schema Version byte in the frame header is `1` for this release. Future versions MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared length).
- Define new message types in reserved type-ID ranges (old decoders skip unknown types via the length field), including a deeper `BookDepth` variant at `0x41`.
- Define new values for enumerated fields and new `Update Flags` / `Level Flags` bits. Decoders MUST accept any value and ignore unknown bits.
- Change the depth constant *N* (requires a Schema Version bump, since it changes `BookDepth`'s length).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.
