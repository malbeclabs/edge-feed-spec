# DoubleZero Top-of-Book & Trades Feed

The DoubleZero Top-of-Book & Trades Feed is a wire format for L1 price feeds delivered over the DoubleZero Edge service. It defines a compact, fixed-size, multicast-native binary protocol for publishing two-sided market data (best bid / best ask quotes and trades) from any venue with an order book.

This document specifies version **3.2.0**: the frame header, application message header, and the set of message types sufficient to operate a working publisher and subscriber.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups. Simple to parse in any language.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram. The protocol defines application messages only; transport, addressing, and group membership are out of scope.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every price message carries a `u16` Source ID. With a single publisher this is redundant; with many it is essential. Cheap to carry now, expensive to retrofit later.
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
| refdata | `InstrumentDefinition`, `ManifestSummary`, `StrikeInterval` |

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
| `0x09` | StrikeInterval | 40 | refdata | Strike of a contract that pays on a threshold or a range (see supplement) |

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
| 12 | Source Timestamp | `ts_ns` | Timestamp from the originating venue. `0` if the venue gives no time for this update. `0` is not a venue time, so a subscriber SHOULD read it as "unavailable". |
| 20 | Bid Price | `price` | Best bid. Uses instrument's Price Exponent. 0 if bid gone. |
| 28 | Bid Quantity | `qty` | Size at best bid. Uses instrument's Qty Exponent. |
| 36 | Ask Price | `price` | Best ask. Uses instrument's Price Exponent. 0 if ask gone. |
| 44 | Ask Quantity | `qty` | Size at best ask. Uses instrument's Qty Exponent. |
| 52 | Bid Source Count | `u16` | Orders/sources at best bid. 0 if unavailable. |
| 54 | Ask Source Count | `u16` | Orders/sources at best ask. 0 if unavailable. |
| 56 | Venue Update ID | `u32` | Low 32 bits of the venue's per-instrument book update counter for the book state that this quote shows. `0` if unavailable. See [Venue Update ID](#venue-update-id). |

#### Venue Update ID

Some venues give a number to each change of the book of an instrument. The number increases with each change. The venue assigns it, so it is the same on each connection to the venue and on each publisher that reads it. `Venue Update ID` carries the low 32 bits of that number. When two or more paths publish one channel, a subscriber can use it to select one copy of each book state.

A publisher MAY fill the field. A publisher:

- that fills the field MUST write the low 32 bits of the venue's counter for the book state that the quote shows. It MUST NOT make a value of its own, for example a count of the quotes that it sent.
- MUST write `0` when it does not fill the field, and when the venue gives no counter for the instrument. Before `3.2.0` these bytes were padding, and this spec did not give a value for them. A publisher with its own encoder MUST make sure that the encoder writes `0` here, because a subscriber reads a nonzero value as a venue counter.
- MUST write `0` when the low 32 bits of the venue's counter are `0`. It MUST NOT write a different value in its place.
- SHOULD fill the field on every path of one Source ID and one channel, or on none. A subscriber cannot compare a quote that has a value with a quote that has none.

A subscriber:

- MUST read `0` as "unavailable", not as a value.
- MUST compare two nonzero values with serial number arithmetic, as in RFC 1982. Value `a` is newer than value `b` if and only if `(a - b) mod 2^32` is between `1` and `2^31 - 1`, inclusive. For example, in Rust: `(a.wrapping_sub(b) as i32) > 0`. When `(a - b) mod 2^32` is `2^31`, neither value is newer.
- MUST compare two values only when both describe the same instrument of the same Source ID and channel.
- MUST read two equal values as the same book state. Two quotes for one instrument with the same nonzero value therefore have the same prices and quantities, compared after scaling: each raw integer times `10^exponent`, under the `InstrumentDefinition` that the subscriber holds for that quote's path. Raw integers alone can differ for one book state when a definition changes its exponent and one path has the new definition before the other. A difference seen while the paths hold different definitions of the instrument is therefore not evidence either way. `Bid Source Count` and `Ask Source Count` are not part of this comparison, because one publisher can write `0` (unavailable) where another writes a count. If the prices or quantities differ, the field does not hold a venue counter, for example because an encoder from before `3.2.0` writes nonzero padding. A subscriber SHOULD then stop using the field for that Source ID.
- MUST treat a quote whose value is `0` as it would treat the quote without this field: by `Source Timestamp` when the quote has one, and otherwise by forwarding it. While the subscriber keeps a value for the instrument, it MAY instead drop such a quote, with or without a `Source Timestamp`, because a quote with a value and a quote without one cannot be ordered against each other. A late copy without a value can otherwise go out after a newer book state, and stay the last state forwarded until the next update. This case comes up most during a rollout, when one path fills the field and another does not.
- MUST NOT use a gap between two values to find lost datagrams. The venue's counter counts book changes at the venue, not quotes, so it can skip values. `Sequence Number` finds lost datagrams.

A subscriber that receives one channel from two or more paths MAY forward a quote only when its value is newer than the last value that it forwarded for that instrument. Then each book state goes out one time, in the order of the venue, whatever the order of arrival. This ordering does not use `Source Timestamp`, so it also works when the venue gives no time for a quote, or when several quotes have the same time.

Serial number arithmetic is correct only when two values are less than `2^31` updates apart. A subscriber that keeps a value for an instrument with no update for a long time can break this condition. A venue can also restart its counter, and then its new values can compare as older. For these two reasons, a subscriber that keeps the last value SHOULD discard it when no newer value arrives for a period that it selects.

Once in each `2^32` updates of one instrument, the low 32 bits of the venue's counter are `0`. That quote then has no value, and a subscriber treats it by the rule above for a quote whose value is `0`. This can send a second copy of that one quote, or drop it. A publisher does not write `1` in its place, because the next value from the venue is `1`, and a subscriber then drops a real update as a copy.

### 0x04 Trade (52 bytes)

Reports a single trade execution.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x04`, Length=52 |
| 4 | Instrument ID | `u32` | Instrument traded |
| 8 | Source ID | `u16` | Originating `source_id`, as assigned by the [Source ID Registry](../sources/spec.md). |
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

### 0x09 StrikeInterval (40 bytes)

The strike of one instrument as an interval: a lower bound, an upper bound, or both. Carried on the `refdata` port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); the layout is reproduced here for convenience. The supplement is the authority for the meaning of `Bound Flags` and for the publisher and subscriber rules.

A publisher sends this message only for an instrument whose contract pays on a strike. A publisher MUST put it immediately after the `InstrumentDefinition` of the same instrument, in the same datagram. A subscriber that does not implement it skips it by `Message Length`.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x09`, Length=40 |
| 4  | Instrument ID | `u32` | The instrument that this strike applies to. The same value as in the `InstrumentDefinition` that this message follows. |
| 8  | Source ID | `u16` | The matching engine, as assigned by the [Source ID Registry](../sources/spec.md). The same value as in the `InstrumentDefinition`. |
| 10 | Strike Exponent | `i8` | The decimal exponent of both bounds. Independent of `Price Exponent`. |
| 11 | Bound Flags | `u8` | Bit 0: `Lower Bound` is present. Bit 1: `Lower Bound` is inclusive. Bit 2: `Upper Bound` is present. Bit 3: `Upper Bound` is inclusive. Bits 4–7: reserved. Bits 0 and 2 both clear: the strike is pending. |
| 12 | Lower Bound | `i64` | The raw value of the lower bound. Uses `Strike Exponent`. `0` when bit 0 of `Bound Flags` is clear. |
| 20 | Upper Bound | `i64` | The raw value of the upper bound. Uses `Strike Exponent`. `0` when bit 2 of `Bound Flags` is clear. |
| 28 | Fixing Time | `ts_ns` | The time at which the venue fixed the strike. `0` while the strike is pending, when the venue fixed the strike at listing, and when the time is not available. |
| 36 | Reserved | 4B | Padding. `0`. |

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0.
2. Begins emitting **InstrumentDefinition** for every active instrument on the reference data port, paced evenly across the definition cycle period (recommended 30 s). Definitions are retransmitted continuously, not just at startup. For an instrument whose contract pays on a strike, a **StrikeInterval** follows each definition in the same datagram.
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

This document is version **3.2.0**, versioned independently of the other feed specs. The Schema Version byte in the frame header is `3` and equals this spec's MAJOR version, so it stays `3` for every `3.x.y` release and changes only on a breaking wire change. See the [Versioning Policy](../VERSIONING.md) for the full rule, the change classification, and the tag scheme.

Future `3.x` versions of this specification MAY, without a Schema Version bump:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define a field in bytes that the current version calls Reserved, within the declared Message Length (old decoders ignore those bytes). `Venue Update ID` in `0x03 Quote` is an example.
- Define new message types in currently-reserved type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Asset Class and Market Model (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the `3.x` line. A change that moves or resizes a field, alters a message length, or redefines existing semantics requires a MAJOR release and a Schema Version bump, which old decoders MUST reject rather than parse.

### Changes

**3.2.0** — additive. Defined offset 56 of `0x03 Quote` as `Venue Update ID` (`u32`): the low 32 bits of the venue's per-instrument book update counter, `0` if unavailable, compared with serial number arithmetic. The four bytes were padding inside the declared 60 bytes, so the Quote stays 60 bytes and no other field moves. The field is optional for a publisher. This release adds one publisher requirement: a publisher that does not fill the field writes `0` there. The shared encoder already writes `0`, so a publisher that uses it is conformant with no change. An old decoder ignores the bytes, so the Schema Version byte stays `3`. The Versioning section also lists a field defined in reserved bytes as a change that a `3.x` version can make. The same release defines `0` in the `Source Timestamp` of `0x03 Quote`: the venue gives no time for the update. It adds one publisher requirement: a publisher writes `0` when the venue gives no time. Publishers write `0` in this case today. `0` is the Unix epoch and is never a venue time, so a subscriber SHOULD read it as "unavailable". The `Venue Update ID` rules refer to a quote with no `Source Timestamp`.

**3.1.0** — additive. Added `0x09 StrikeInterval` (40 bytes) on the `refdata` port, defined in the [Reference Data Distribution supplement](../reference-data/spec.md) at its `1.1.0`. It carries the strike of a contract that pays on a threshold or a range, and follows the `InstrumentDefinition` of the same instrument in the same datagram. `InstrumentDefinition` does not change. An old decoder skips the type by Message Length, so the Schema Version byte stays `3`. Editorial in the same release: the Versioning section says `other feed specs` where it said `sibling specs`, as `GLOSSARY.md` requires.

**3.0.1** — editorial. Qualified the bare uses of "source" on the `Quote` and `Trade` `Source ID` rows and in Design Principle 6, and added an *Identity Model* section stating that instrument identity is the `(channel_id, instrument_id)` tuple. Adopted the glossary's "published set". No wire change.

**3.0.0** — added `Source ID` (`u16`) after `Instrument ID` in `InstrumentDefinition`. `Symbol` and every later field move two bytes, and the message grows from 128 to 130 bytes. This is a breaking change: the Schema Version byte is now `3`, and a decoder built for `2.x` MUST reject these frames rather than parse them at the old offsets. The midpoint feed remains unchanged at Schema Version `1`.

**2.0.0** — widened the `InstrumentDefinition` `Symbol` field from `char[16]` to `char[64]`. Every field after `Symbol` moves and the message grows from 80 to 128 bytes, so this is a breaking change: the Schema Version byte is now `2`, and a decoder built for `1.x` MUST reject these frames rather than parse them at the old offsets. Nothing else on the wire changed. The midpoint feed keeps its 64-byte variant and stays at Schema Version `1`.

**1.0.0** — first stable release. Promoted from the `0.1.0` draft with no wire change; Schema Version was `1` before and after. Includes two additive changes made during the draft period, both of which left Schema Version at `1`: `0x08 Liquidation` was added as a shared trade-companion type (old decoders skip it via Message Length), and Asset Class value `5` (Perpetual Future) was added (decoders already MUST accept any `u8` and treat unknown values as `0` (Unknown)).
