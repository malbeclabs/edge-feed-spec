# DoubleZero Order-Intent Feed

The DoubleZero Order-Intent Feed is a wire format for normalized, pre-consensus order-intent events delivered over the DoubleZero Edge service. It defines a fixed-size, multicast-native binary protocol for publishing the order, cancel, and modify *submissions* observed in a venue's mempool (or equivalent pre-consensus layer) — attributed to the signing account when signature recovery succeeds, otherwise carried as an unauthenticated, zero-signer observation — typically before the venue's consensus commits them.

This is a sibling protocol to the DoubleZero [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md), not a layer on top. Where those feeds carry accepted book state — quotes, resting orders, mid prices — this feed carries *intent*: every successfully normalized supported submission, plus dead-man-switch arm/disarm, as a fixed-size binary message.

This document specifies version **0.1.0**: the frame header, application message header, and the message types that define the wire format. The wire format is venue-generic; the per-venue derivations it deliberately does not fix (account-id encoding, Action Tag rule, source-message mapping) are defined out of band by each venue's publisher and are not part of this specification. The [Source ID Registry](../sources/spec.md) only assigns the Source ID → venue mapping.

**Trust semantics (normative).** Events are *observed signed submissions*, not accepted orders. An event may reference an invalid order, a replay, an action the venue later rejects, or intent that never executes. The publisher performs no venue-level validation: it *attempts* signature recovery to attribute the event, but recovery is **not a gate on publication** — when recovery fails or is skipped, the event is still published with a zeroed Signer and the `signer unverified` flag (an unauthenticated observation; see [Common Event Fields](#common-event-fields)). Subscribers MUST treat the feed as intent, never as fills or book state. The boundary is syntactic, not semantic: values that parse and convert exactly are published as-is even if venue-invalid (zero quantity, a past schedule-cancel time, an order id that never existed). "Successfully normalized" means malformed, unmappable, or precision-violating entries are dropped; the feed does not promise every observed submission.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size, fixed-offset messages.** Every message type has a constant length and every field sits at a constant byte offset. No variable-length fields, no repeating groups, no TLV on the market-data path. A multi-entry batch action explodes into **N fixed-size messages** (sharing an Action Tag and Batch Index/Count), never one variable-length jumbo message.
3. **Flags gate interpretation, never position.** No field offset depends on a flag or enum value. Where two fields are mutually exclusive (e.g. `OrderModify`'s Target Order ID vs. Target Client Order ID), **both are always physically present** at fixed offsets and a flag selects which is valid; the **Unused-Field Rule** mandates zeroing the unused field rather than omitting it.
4. **All message lengths are multiples of 4.** The 24-byte frame header plus the 4-byte application header form a 4-aligned 28-byte prefix, so a ÷4 message length keeps every message and every 32-bit-or-wider payload field on the 4-byte grid however messages pack into a frame. (`u64`/`i64`/`ts_ns` fields therefore land on 4-byte but not 8-byte boundaries — the 28-byte prefix is a multiple of 4, not 8. This matches the sibling feeds and suits streaming/FPGA parsers; a CPU consumer wanting 8-byte-aligned zero-copy loads should copy fields out rather than reference them in place.)
5. **No strings and no crypto on the market-data hot path.** The event messages (`0x30`–`0x34` on `mktdata`) carry no strings: prices and sizes are fixed-point `i64`/`u64` mantissas, with per-instrument exponents carried in `InstrumentDefinition` on the `refdata` port. The Action Tag and Signer are **pre-computed by the publisher** — a consumer parsing the market-data path performs no hashing, signature recovery, or deserialization, only fixed-offset field extraction.
6. **Instrument-ID based.** Numeric `u32` IDs and the `u16` Source ID are the only identifiers on the market-data path; human-readable strings live only in reference data.
7. **Source-attributed.** Every event carries a `u16` Source ID identifying the venue. With one venue this is redundant; with many it is essential.

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
| `byte[N]` | N | Fixed-length opaque byte array, copied verbatim (no endian conversion) |
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
| mktdata | `OrderNew`, `OrderCancel`, `OrderCancelByClientId`, `OrderModify`, `ScheduleCancel`, `Heartbeat` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |

The frame header and application message header are identical on both ports. A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port; doing so forgoes not just the in-band instrument definitions but also the `refdata` liveness signal and the `ManifestSummary` (its `Valid` flag, `Manifest Seq`, and instrument-count) — so a `mktdata`-only subscriber will not observe instrument-set changes in band. Concrete port assignments are out of scope for this spec; each deployment publishes its port mapping out of band.

v1 uses a single channel (ID 0). The frame header supports instrument sharding across channels later without format change.

### Frame Header (24 bytes)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     Magic (0x494F)            |  Schema Ver   |  Channel ID   |
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
| 0 | Magic | `u16` | `0x494F` ("OI", wire bytes `[0x4F, 0x49]`). Frame delimiter. Distinct from the top-of-book feed's `0x445A`, the market-by-order feed's `0x4444`, and the midpoint feed's `0x4D44` to prevent cross-protocol misrouting. |
| 2 | Schema Version | `u8` | Protocol version. Starts at `1`. |
| 3 | Channel ID | `u8` | Logical channel for instrument sharding. `0` in v1. |
| 4 | Sequence Number | `u64` | Monotonically increasing **per publisher host, per channel, per port**, starting from 0. Resets to 0 when `Reset Count` changes. Used for per-port gap detection. The `mktdata` and `refdata` ports each have an independent series. The frame header carries no Source ID, so a subscriber binding several hosts on one multicast group sees one independent sequence series **per originating host** and MUST track them keyed by transport origin (the datagram's source IP and destination port; each host of a venue publishes on a distinct destination port, and the per-host port offset is a deployment convention defined out of band, not by this spec). Hosts of one venue all carry the **same** Source ID (per-venue; see [Common Event Fields](#common-event-fields)), so the host is identified by transport, not by the in-message Source ID. Sequence gaps are a per-host, per-channel, per-port health signal and never gate delivery. |
| 12 | Send Timestamp | `ts_ns` | When the publisher sent this frame. Subscribers can measure publisher-internal latency as the difference from a message's Source Timestamp. |
| 20 | Message Count | `u8` | Number of application messages in this frame (1–255). |
| 21 | Reset Count | `u8` | Incremented each time the publisher resets the channel. Subscribers detect a reset by comparing against their last-seen value **for inequality** — any change, including the `255`→`0` wrap, is a reset; never compare for ordering. Shared across both ports of the channel. |
| 22 | Frame Length | `u16` | Total frame length in bytes, including this header. |

A `Reset Count` change means the sequence space restarted and any events during the gap are unrecoverable **by design** — this is a real-time intent feed with no replay. Subscribers MUST treat a `Reset Count` change as a feed restart, not as recoverable packet loss (see [Reset and Recovery Semantics](#reset-and-recovery-semantics)).

---

## Application Message Header (4 bytes)

Every application message begins with:

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Message Type | `u8` | See Message Types table. |
| 1 | Message Length | `u8` | Total message length including this header. Max 255. |
| 2 | Flags | `u16` | Bit 0: snapshot (1) vs. incremental (0) — always 0 on this feed, which has no snapshot mechanism. Bits 1–15: reserved. The per-event semantic flags live in the **Event Flags** `u8` field inside each message body, not in this header. |

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|-----:|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal. Inherited; byte-for-byte identical to siblings. |
| `0x02` | InstrumentDefinition | 80 | refdata | Reference data for an instrument. Inherited from the top-of-book feed verbatim. |
| `0x03` | *(reserved)* | — | — | Quote in the top-of-book feed, Midpoint in the midpoint feed. Intentionally unused here to prevent accidental cross-decoding if a frame is misrouted. |
| `0x04` | *(reserved)* | — | — | Trade in the top-of-book and market-by-order feeds. This feed carries intent, not executions; intentionally unused. |
| `0x05` | *(reserved)* | — | — | |
| `0x06` | *(reserved)* | — | — | EndOfSession in sibling feeds. This is a real-time intent stream with no session boundary — a publisher going away is signaled by a `Reset Count` change on its return, not a session-end message — so it is intentionally unused here. |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary. Inherited; see the [Reference Data Distribution supplement](../reference-data/spec.md). |
| `0x30` | OrderNew | 124 | mktdata | A new order was submitted. |
| `0x31` | OrderCancel | 88 | mktdata | Cancel request by venue order id. |
| `0x32` | OrderCancelByClientId | 96 | mktdata | Cancel request by client order id. |
| `0x33` | OrderModify | 148 | mktdata | Modify request (cancel-replace semantics). |
| `0x34` | ScheduleCancel | 88 | mktdata | Dead-man-switch arm/disarm (account-wide). |

A decoder encountering an unknown type MUST skip the message using its `Message Length` field and continue parsing the frame.

### Cross-Spec Type ID Policy

Order-intent payload Type IDs occupy the `0x30`–`0x3F` range, which does not overlap any sibling feed. As with the sibling specs, a Type ID that appears in more than one feed MUST carry the same semantic meaning in each. The inherited Type IDs `0x01` (Heartbeat) and `0x07` (ManifestSummary) are byte-for-byte identical to the siblings that carry them; `0x02` (InstrumentDefinition) shares the top-of-book/market-by-order 80-byte layout. The sibling Type IDs this feed does **not** use — `0x03` (Quote/Midpoint), `0x04` (Trade), and `0x06` (EndOfSession) — are reserved here, never reassigned to a different payload, so a misrouted sibling frame is rejected rather than mis-decoded.

---

## Message Definitions

### Inherited Messages

`Heartbeat` (0x01), `InstrumentDefinition` (0x02), and `ManifestSummary` (0x07) are inherited from the [Top-of-Book & Trades Feed](../top-of-book/spec.md) and the [Reference Data Distribution supplement](../reference-data/spec.md). Their layouts are unchanged; they are reproduced below for standalone readability. `Heartbeat` is carried on `mktdata`; `InstrumentDefinition` and `ManifestSummary` on `refdata`.

The `refdata` port re-emits `InstrumentDefinition` for the feed's instrument set on the standard cycle (recommended 30 s definitions / 1 s manifest), so subscribers can map the `u32` Instrument IDs carried on `mktdata` to symbols, asset classes, and the per-instrument price/qty exponents used to interpret `price`/`qty` fields.

#### 0x01 Heartbeat (16 bytes)

Inherited verbatim. Sent every N seconds on the `mktdata` port when there is no other traffic. Receivers use this for stale-connection detection. The `refdata` port's `ManifestSummary` cadence is its own liveness signal; `Heartbeat` is emitted on `mktdata` only.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x01`, Length=16 |
| 4 | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5 | Reserved | 3B | Padding |
| 8 | Timestamp | `ts_ns` | Current time |

#### 0x02 InstrumentDefinition (80 bytes)

Inherited from the top-of-book feed verbatim. Maps a numeric Instrument ID to human-readable metadata. Carried on the `refdata` port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x02`, Length=80 |
| 4 | Instrument ID | `u32` | Unique numeric ID for this instrument |
| 8 | Symbol | `char[16]` | Human-readable label. Truncate if needed (e.g., `"BTC-USDT"`). |
| 24 | Leg1 | `char[8]` | First leg/component. Context-dependent: base currency, underlying, outcome name. |
| 32 | Leg2 | `char[8]` | Second leg/component. Context-dependent: quote/settlement currency. |
| 40 | Asset Class | `u8` | `0`=Unknown, `1`=Crypto Spot, `2`=Prediction Binary, `3`=Prediction Scalar, `4`=Prediction Categorical |
| 41 | Price Exponent | `i8` | Implied decimal exponent for price fields. e.g., `-2` means divide raw value by 100. |
| 42 | Qty Exponent | `i8` | Implied decimal exponent for quantity fields. |
| 43 | Market Model | `u8` | `0`=Unknown, `1`=CLOB, `2`=AMM |
| 44 | Tick Size | `price` | Minimum price increment (interpreted via Price Exponent). |
| 52 | Lot Size | `qty` | Minimum quantity increment (interpreted via Qty Exponent). |
| 60 | Contract Value | `u64` | Notional per contract. 0 if not applicable (e.g., spot). |
| 68 | Expiry | `ts_ns` | Expiration timestamp. 0 for non-expiring. |
| 76 | Settle Type | `u8` | 0=N/A, 1=Cash, 2=Physical |
| 77 | Price Bound | `u8` | 0=Unbounded, 1=Bounded [0,1] (binary outcomes), 2=Non-negative only |
| 78 | Manifest Seq | `u16` | The publisher's `Manifest Seq` at the time this definition was emitted. See supplement. |

Publishers SHOULD use the most accurate Asset Class / Market Model value available; receivers MUST accept any `u8` value and treat unknown values as `0` (Unknown).

#### 0x07 ManifestSummary (24 bytes)

Inherited. Periodic summary of the active instrument set on this channel. Carried on the `refdata` port. Defined in the [Reference Data Distribution supplement](../reference-data/spec.md); reproduced here for convenience.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x07`, Length=24 |
| 4  | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5  | Valid | `u8` | `1` when the channel has an established instrument set; `0` when the publisher is uninitialized or the channel is inactive. See supplement. Asserts that the channel's instrument set is established — **not** that the source is producing flow. |
| 6  | Reserved | 2B | Padding |
| 8  | Manifest Seq | `u16` | Increments every time the active instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the active set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

### Common Event Fields

Every event message (`0x30`–`0x34`) carries the following core fields. Field offsets differ per message; the semantics are shared.

- **Instrument ID** `u32` — publisher instrument id, mapped from the venue-native asset id and described on the `refdata` port. Account-wide scope is a property of the **message type** (`ScheduleCancel`), not of a sentinel value: a real venue may have an instrument with id 0, so 0 cannot mean "none". Account-wide messages set the field to `0xFFFFFFFF` (reserved "no instrument") and receivers identify scope by type. Instrument identity is the **`(Channel ID, Instrument ID)`** tuple, matching the sibling feeds: an Instrument ID is unique only within its channel, so a sharded deployment (Channel ID ≠ 0) MUST key instrument state on the pair. In v1 — a single channel (ID 0) — the channel component is constant and the two are equivalent.
- **Source ID** `u16` — identifies the **venue** whose flow this event was derived from, per the canonical [Source ID Registry](../sources/spec.md): one stable id per venue, never reused. **Every publisher host of a venue emits the same Source ID**, and hosts are distinguished by the port they send on (a deployment-defined per-host port offset, out of scope for this spec), not by Source ID. The account-id encoding and Action Tag derivation are properties of the venue, defined out of band by that venue's publisher; the registry itself only maps the Source ID to a venue. A subscriber receiving a Source ID absent from its pinned registry SHOULD drop those events and surface a counter prompting a registry update; it MUST NOT fail the channel.
- **Action Tag** `u64` — a per-action correlation handle, pre-computed by the publisher and identical for every event exploded from one action. Its derivation is a **per-venue property defined out of band by the venue's publisher**, not a wire-format constant; the wire carries only the resulting `u64`. The **fallback action tag** flag (Event Flags bit 5) marks a venue-defined fallback derivation. Because cross-host dedupe depends on tag agreement, all publisher hosts of one venue MUST run identical tag-derivation behavior in steady state; the sole tolerated exception is transient version skew during a rolling deploy, whose duplicates subscribers absorb under the at-least-once model. The tag is a **correlation aid, not a globally unique id**: 64 bits make collisions negligible within any realistic correlation window, but consumers MUST NOT assume global uniqueness over long horizons.
- **Nonce** `u64` — the client's nonce, the order *intent* time as the client recorded it (commonly a client-side **millisecond** timestamp — note the unit contrast with the nanosecond `Source Timestamp`). A per-signer replay/ordering value, not an identity and not a venue-global sequence.
- **Source Timestamp** `ts_ns` — the source node's recorded observation time for the batch, nanoseconds since the Unix epoch, passed through **verbatim from the source node**. Its exact provenance is a property of the source node, not of this feed. A batch whose source timestamp cannot be parsed is skipped and counted in a metric.
- **Signer** `byte[20]` — the recovered signing address. **Zeroed** with the `signer unverified` flag (Event Flags bit 3) set when recovery fails or was not attempted. An all-zero signer is never a real account; consumers MUST NOT join zero-signer events into account-level analytics. An event with bit 3 set is an **unauthenticated observation** — not attributable to any account. A recovered address asserts only *cryptographic consistency* of the observed signature with the observed action — venue-level validity (nonce freshness, account existence) is not checked.
- **Vault** `byte[20]` — the vault/subaccount the action targets; **zeroed** when absent. The **vault present** flag (Event Flags bit 4) distinguishes "no vault" from a pathological all-zero vault address.
- **Batch Index / Batch Count** `u16` × 2 — position within the exploded signed action (0-based index; count ≥ 1). The batch is the **whole signed action**: index/count span every event emitted from it regardless of message kind — a single action producing a mix of `OrderNew` and `OrderModify` shares one index space. Indexes are assigned from the source entry positions and Count is the source entry count: if an entry is withheld (malformed shape, unsupported shape, unmappable asset, or a precision-violating price/size), the remaining events keep their original indexes and Count, so an **index gap with full Count** tells the subscriber an entry was withheld. The whole signed action is skipped (producing no events) only when the action-level schema itself fails. Actions exploding to more than 65,535 entries are dropped whole and counted in a metric.

#### Account Identifier Encoding (venue-defined)

The encoding of the Signer and Vault fields is a property of the **venue**, defined out of band by that venue's publisher. A venue whose native account id fits in 20 bytes carries it verbatim; a venue whose native ids are longer (e.g. a 32-byte public key) defines a deterministic 20-byte derivation — conventionally the first 20 bytes of a hash of the native id, a non-reversible **feed identity** rather than an account address. Where a venue's encoding is a hashed/truncated identity it carries no cross-venue domain separation, so the consumer rule is normative regardless of venue: **account ids MUST only be compared within one venue (one Source ID), never across venues.**

---

## Enumerations and Flags

- **Side** `u8`: `0` unknown, `1` buy, `2` sell. (Intentionally **not** the same encoding as the [Market-by-Order Feed](../market-by-order/spec.md), whose Side is `0` bid/buy, `1` ask/sell: this feed reserves `0` for *unknown* so the unknown-enum rule — treat an unrecognized value as `0` — cannot alias to a real side. Side is not harmonized across the family; a consumer reading more than one feed MUST decode Side per the feed it is reading.)
- **Order Type** `u8`: `0` unknown, `1` limit, `2` trigger-market, `3` trigger-limit.
- **TIF** `u8` (time-in-force): `0` unknown, `1` GTC, `2` IOC, `3` ALO (post-only).
- **Grouping** `u8`: `0` na, `1` normalTpsl, `2` positionTpsl.
- **Event Flags** `u8`:

| Bit | Name | Meaning |
|-----|------|---------|
| 0 | reduce-only | Order will not increase position |
| 1 | trigger-is-tp | The trigger is a take-profit (see note below) |
| 2 | trigger-is-sl | The trigger is a stop-loss (see note below) |
| 3 | signer unverified | The Signer field is zeroed; recovery failed or was not attempted (unauthenticated observation) |
| 4 | vault present | The Vault field is a real vault address (distinguishes a present vault from an absent, all-zero one) |
| 5 | fallback action tag | The Action Tag was derived from the venue-defined fallback rule, not the primary rule. Distinct from bit 3: the two usually co-occur but encode different facts |
| 6 | target-by-client-id | `OrderModify` only: the modify targets the Target Client Order ID, not the Target Order ID. When set, the 16-byte Target Client Order ID is mandatory and valid; there is no separate presence flag for it |
| 7 | client order id present | Governs the message's *primary* client-order-id field: Client Order ID in `OrderNew`, New Client Order ID in `OrderModify`. Distinguishes absent from a literal all-zero id. The *target* client id in `OrderModify` is governed by bit 6; the client id in `OrderCancelByClientId` is always present by construction |

Bits 1 and 2 (`trigger-is-tp` / `trigger-is-sl`) are **mutually exclusive**: at most one is set. Both clear means the take-profit/stop-loss role is unspecified — the normal state for a non-trigger order, and also valid for a trigger order whose source carried no tp/sl designation. Both set is invalid; receivers MUST treat it as unspecified (neither). These bits are only meaningful when Order Type is trigger-market/trigger-limit; on a non-trigger order they are zeroed per the Unused-Field Rule.

These are the order-intent feed's **`Event Flags`**, a distinct field from the [Market-by-Order Feed](../market-by-order/spec.md)'s `Order Flags`; bit positions are **not** shared across feeds (for example `reduce-only` is bit 0 here but bit 1 in market-by-order). A cross-feed reader MUST apply each feed's own bit definitions and not reuse flag masks across feeds.

Receivers MUST NOT reject a message over an unrecognized enum value: they treat the value semantically as `0`/unknown (the raw byte MAY be preserved for logging) and MUST ignore flag bits they do not recognize.

### Unused-Field Rule (normative)

Any field whose validity condition is not met — the non-selected modify target, an absent client order id, Trigger Price on a non-trigger order type, every Reserved byte, and any flag bit that does not apply to a message type (e.g. bit 7 on `OrderCancelByClientId`) — MUST be sent **zeroed** and MUST be ignored by receivers. This keeps encodings byte-deterministic for test vectors and cross-implementation comparison.

---

## Message Definitions (Events)

All multi-byte numeric fields are little-endian. `Signer` and `Vault` are opaque 20-byte arrays; `Client Order ID` fields are opaque 16-byte arrays. Header `Flags` (offset 2 of every message) is always 0 on this feed; the semantic flags are the **Event Flags** `u8` inside each body.

### 0x30 OrderNew (124 bytes)

A new order was submitted.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x30`, Length=124 |
| 4 | Instrument ID | `u32` | Instrument this order applies to |
| 8 | Source ID | `u16` | Venue (same on every publisher host of the venue; see Common Event Fields) |
| 10 | Side | `u8` | See Side |
| 11 | Order Type | `u8` | See Order Type |
| 12 | TIF | `u8` | See TIF |
| 13 | Event Flags | `u8` | See Event Flags |
| 14 | Batch Index | `u16` | Position within the exploded signed action (0-based) |
| 16 | Batch Count | `u16` | Source entry count of the signed action (≥ 1) |
| 18 | Grouping | `u8` | See Grouping |
| 19 | Reserved | `u8` | Padding; zeroed |
| 20 | Action Tag | `u64` | Per-action correlation handle |
| 28 | Nonce | `u64` | Client nonce (intent time) |
| 36 | Source Timestamp | `ts_ns` | Node observation time |
| 44 | Price | `price` | The order's submitted price as the venue provides it: the limit price for a limit order; for a trigger order, the post-trigger limit or slippage-bound price the venue carried (a trigger-market order whose source supplies no such price is zeroed). Uses instrument's Price Exponent |
| 52 | Trigger Price | `price` | The price level at which the trigger fires. Meaningful only when Order Type is trigger-market/trigger-limit (Order Type, not a zero sentinel, is the discriminator); zeroed otherwise |
| 60 | Quantity | `qty` | Order size. Uses instrument's Qty Exponent |
| 68 | Client Order ID | `byte[16]` | Valid only when Event Flags bit 7 (client order id present) is set; zeroed otherwise |
| 84 | Signer | `byte[20]` | Recovered signing address; zeroed when bit 3 set |
| 104 | Vault | `byte[20]` | Vault/subaccount; zeroed when bit 4 clear |

### 0x31 OrderCancel (88 bytes)

Cancel request by venue order id.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x31`, Length=88 |
| 4 | Instrument ID | `u32` | |
| 8 | Source ID | `u16` | |
| 10 | Event Flags | `u8` | See Event Flags (Side/Order Type/TIF/Grouping do not apply to a cancel and are not carried) |
| 11 | Reserved | `u8` | Padding; zeroed |
| 12 | Batch Index | `u16` | |
| 14 | Batch Count | `u16` | |
| 16 | Action Tag | `u64` | |
| 24 | Nonce | `u64` | |
| 32 | Source Timestamp | `ts_ns` | |
| 40 | Order ID | `u64` | Venue-assigned order id to cancel |
| 48 | Signer | `byte[20]` | |
| 68 | Vault | `byte[20]` | |

### 0x32 OrderCancelByClientId (96 bytes)

Cancel request by client order id. Identical to `OrderCancel`, with the 8-byte Order ID replaced by a 16-byte Client Order ID (shifting Signer and Vault by 8).

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x32`, Length=96 |
| 4 | Instrument ID | `u32` | |
| 8 | Source ID | `u16` | |
| 10 | Event Flags | `u8` | See Event Flags (the client-id-present bit 7 does not apply to this type — the client id is always present — and is zeroed) |
| 11 | Reserved | `u8` | Padding; zeroed |
| 12 | Batch Index | `u16` | |
| 14 | Batch Count | `u16` | |
| 16 | Action Tag | `u64` | |
| 24 | Nonce | `u64` | |
| 32 | Source Timestamp | `ts_ns` | |
| 40 | Client Order ID | `byte[16]` | Client order id to cancel (always present by construction) |
| 56 | Signer | `byte[20]` | |
| 76 | Vault | `byte[20]` | |

### 0x33 OrderModify (148 bytes)

Modify request (cancel-replace semantics). Carries both target-id fields at fixed offsets; Event Flags bit 6 selects which is valid (Unused-Field Rule).

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x33`, Length=148 |
| 4 | Instrument ID | `u32` | |
| 8 | Source ID | `u16` | |
| 10 | Side | `u8` | See Side |
| 11 | Order Type | `u8` | See Order Type |
| 12 | TIF | `u8` | See TIF |
| 13 | Event Flags | `u8` | See Event Flags |
| 14 | Batch Index | `u16` | |
| 16 | Batch Count | `u16` | |
| 18 | Grouping | `u8` | See Grouping |
| 19 | Reserved | `u8` | Padding; zeroed |
| 20 | Action Tag | `u64` | |
| 28 | Nonce | `u64` | |
| 36 | Source Timestamp | `ts_ns` | |
| 44 | Target Order ID | `u64` | The order being modified, by venue id. Valid only when Event Flags bit 6 is **clear** (flag, not a zero sentinel, is the discriminator); zeroed otherwise |
| 52 | Target Client Order ID | `byte[16]` | The order being modified, by client id. Valid only when Event Flags bit 6 (target-by-client-id) is **set**; zeroed otherwise |
| 68 | Price | `price` | The replacement order's submitted price, same semantics as `OrderNew` Price. Uses instrument's Price Exponent |
| 76 | Trigger Price | `price` | The replacement order's trigger level. Meaningful only for trigger order types; zeroed otherwise |
| 84 | Quantity | `qty` | New order size. Uses instrument's Qty Exponent |
| 92 | New Client Order ID | `byte[16]` | The replacement order's client id. Valid only when Event Flags bit 7 (client order id present) is set; zeroed otherwise |
| 108 | Signer | `byte[20]` | |
| 128 | Vault | `byte[20]` | |

### 0x34 ScheduleCancel (88 bytes)

Dead-man-switch arm/disarm (account-wide). Identical layout to `OrderCancel`, with the Order ID field replaced by **Trigger Time**.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x34`, Length=88 |
| 4 | Instrument ID | `u32` | `0xFFFFFFFF` (account-wide; see Common Event Fields) |
| 8 | Source ID | `u16` | |
| 10 | Event Flags | `u8` | See Event Flags |
| 11 | Reserved | `u8` | Padding; zeroed |
| 12 | Batch Index | `u16` | `0` (single-entry by construction; see below) |
| 14 | Batch Count | `u16` | `1` |
| 16 | Action Tag | `u64` | |
| 24 | Nonce | `u64` | |
| 32 | Source Timestamp | `ts_ns` | |
| 40 | Trigger Time | `u64` | Milliseconds since the Unix epoch at which the dead-man switch fires — a `u64` **ms** value, **not** `ts_ns` (the venue's verbatim unit; contrast the nanosecond `Source Timestamp`); **`0` = disarm** |
| 48 | Signer | `byte[20]` | |
| 68 | Vault | `byte[20]` | |

`Batch Index 0 / Count 1` is not a special rule but the general one: a schedule-cancel action contains no entry array, so it is single-entry by construction. `ScheduleCancel` is account-wide because an armed dead-man switch signals imminent mass cancellation — high-value intent — even though it is not tied to one instrument.

Venue-specific mapping of source actions to these messages is defined out of band by each venue's publisher and is not part of this specification.

---

## Subscriber Algorithm

A subscriber consuming this feed MUST:

1. **Bind both ports and read both immediately.** Bind `mktdata` and `refdata` and begin reading both from the start — **do not block `mktdata` parsing on refdata.** Build reference-data state per the [Reference Data Distribution supplement](../reference-data/spec.md) until it is `ready` (its `Valid` flag set and the full instrument set received) so `u32` Instrument IDs and the per-instrument price/qty exponents resolve; this proceeds in parallel with consuming events, and events seen before their definition are handled by step 9, not by waiting. A subscriber with out-of-band `InstrumentDefinition` data MAY bind `mktdata` only.
2. **Parse frames.** Validate `Magic`, then walk the frame's application messages using each `Message Length`, skipping unknown Type IDs by length. Treat a message whose `Message Length` is `< 4`, exceeds the bytes remaining in the frame, or is inconsistent with `Frame Length` as a malformed frame: stop parsing it and count it; do not fail the channel.
3. **Deduplicate on `(Source ID, Action Tag, Batch Index, Signer)`** over a bounded time window when consuming the same venue flow from more than one publisher host. The Source ID **is the venue** (identical on every host of that venue, which are distinguished by port), so copies of one action from different hosts collapse on this key, while flows from different venues (different Source IDs) never dedupe against each other. (Zero-signer degraded events share the all-zero Signer component, so they dedupe on it; the residual over-collapse risk is bounded by the 64-bit Action Tag within the window and is accepted.)
4. **Tolerate duplicates.** Cross-host delivery is **at-least-once**: a subscriber MUST tolerate receiving the same event from multiple hosts, beyond the dedupe window, or across a publisher restart.
5. **Re-latch on a `Reset Count` change.** `Reset Count` is tracked per `(host, channel)` — it is shared across the channel's two ports. When it changes: reset the expected `Sequence Number` for **both ports** of that `(host, channel)`, and **invalidate that channel's reference-data state**, rebuilding readiness from the refetched `InstrumentDefinition`/`ManifestSummary` cycle (a reset may have changed the instrument set; stale definitions MUST NOT survive it). Treat the change as a feed restart, **not** packet loss — gap events spanning the reset are unrecoverable by design (see [Reset and Recovery Semantics](#reset-and-recovery-semantics)). Compare `Reset Count` for inequality, not ordering: it is a `u8` and any change (including the wrap from `255` to `0`) is a reset.
6. **Never attribute a zero-signer event.** An event with Event Flags bit 3 (`signer unverified`) set carries an all-zero Signer and is an unauthenticated observation; it MUST NOT be joined into account-level analytics.
7. **Treat an unknown Source ID gracefully.** A subscriber receiving a Source ID absent from its pinned registry SHOULD drop those events (it cannot resolve the venue, and so cannot obtain that venue's publisher-documented account-id encoding or Action Tag rule) and surface a counter; it MUST NOT fail the channel.
8. **Do not infer source liveness from the protocol.** Heartbeats and `ManifestSummary.Valid` assert channel/refdata liveness only, not that the source is producing flow; a subscriber needing source liveness MUST monitor event recency itself.
9. **Tolerate pre-definition events.** A subscriber MAY receive events for an instrument before that instrument's `InstrumentDefinition` has arrived on `refdata` (the publisher has the entry, the subscriber's refdata has not cycled). It MUST tolerate this — buffer until refdata arrives or drop — the same cold-start tolerance as the sibling feeds.

Per-host, per-channel, per-port `Sequence Number` gaps are a health signal only; they never gate delivery (late or duplicate frames still flow through dedupe).

---

## Publisher Behavior

A publisher operating this feed MUST:

1. **Emit in source order** on `mktdata` as venue events arrive, exploding each multi-entry signed action into one fixed-size event per entry sharing the action's Action Tag, Nonce, Signer, Vault, and Batch Count (per [Common Event Fields](#common-event-fields)). Pack multiple messages into a frame up to the MTU.
2. **Maintain a monotonic `Sequence Number`** per `(host, channel, port)`, starting at 0 and resetting only on `Reset Count` change.
3. **Pre-compute the Action Tag and recover the Signer** so the market-data path carries no strings or crypto. All hosts of one venue MUST run identical tag-derivation behavior in steady state.
4. **Perform first-observation dedup** over a bounded window, using a venue-defined publisher dedupe identity (a full-precision per-venue key, distinct from the truncated wire Action Tag), so a re-observed copy of an action it already published is not re-emitted by the same host. (Cross-host duplicates remain possible and are reconciled by the subscriber.)
5. **Retransmit `InstrumentDefinition` and `ManifestSummary`** on `refdata` per the [Reference Data Distribution supplement](../reference-data/spec.md) (recommended 30 s definition cycle, 1 s manifest cadence). Drive the `ManifestSummary.Valid` flag per the supplement: `Valid = 0` while the instrument set is not yet established (cold start), transitioning to `Valid = 1` once it is.
6. **Emit `Heartbeat`** on `mktdata` every N seconds when otherwise idle (recommended 1 s).
7. **Bump `Reset Count`** and reset per-port `Sequence Number` to 0 on restart, resuming at the live edge of the source. Events that occurred during downtime are intentionally never published.

---

## Reset and Recovery Semantics

This feed has **no snapshot or replay mechanism** — it is a real-time intent stream, and late events are worse than absent ones. Recovery semantics are therefore deliberately minimal:

- **Publisher restart** bumps `Reset Count`, resets per-port `Sequence Number` to 0, and resumes at the live edge of the source. Events that occurred during publisher downtime are **intentionally never published**.
- A `Reset Count` change is a **feed restart**, not recoverable packet loss; subscribers re-latch that `(host, channel)`'s sequence tracking on **both ports**, **invalidate and rebuild that channel's reference-data state** (the instrument set may have changed across the reset; stale definitions MUST NOT survive), and accept that gap events are unrecoverable. `Reset Count` is a `u8`, shared across the channel's two ports; compare it for inequality (any change, including the `255`→`0` wrap, is a reset), never for ordering.
- **At-most-once by the source reread:** a batch partially read, or normalized but not yet framed, at crash time is never re-emitted by the reread itself — events can be lost across a crash but are never duplicated by it. (Cross-host re-observation after a restart is still possible and is covered by the at-least-once cross-host model and the subscriber dedupe key.)
- Per-host, per-channel, per-port `Sequence Number` gaps (see the [frame-header rule](#frame-header-24-bytes)) are genuine multicast path loss and a **health signal only**; they never gate delivery.
- Subscribers MUST tolerate receiving events for an instrument whose `InstrumentDefinition` has not yet arrived on `refdata` — the same cold-start tolerance as the sibling feeds.

---

## Wire Efficiency

The event messages are 88–148 bytes each; multiple pack into one 1,232-byte frame. For example, nine 124-byte `OrderNew` messages plus the 24-byte frame header is 1,140 bytes, fitting one frame.

| Message | Size | Per-frame packing |
|---------|-----:|-------------------|
| OrderNew | 124 B | ~9 per 1,232-byte frame |
| OrderCancel | 88 B | ~13 per frame |
| OrderCancelByClientId | 96 B | ~12 per frame |
| OrderModify | 148 B | ~8 per frame |
| ScheduleCancel | 88 B | ~13 per frame |

The format is fixed-size and binary, so parsing requires no allocation, no string handling, and no schema negotiation on the market data path; the Action Tag and Signer are pre-computed publisher-side, so no crypto runs on the hot path.

---

## Venue Genericity and the Source Registry

The wire format above is venue-generic. The [Source ID Registry](../sources/spec.md) assigns the Source ID → venue mapping — and nothing more. Three further things vary per venue and are defined **out of band by each venue's publisher**, not by this spec or the registry: the account-id encoding of Signer/Vault (see [Account Identifier Encoding](#account-identifier-encoding-venue-defined)), the Action Tag derivation rule (see [Common Event Fields](#common-event-fields)), and the mapping from the venue's source actions to these messages. The wire layouts do not change per venue; a venue's publisher is responsible for emitting conformant frames and for documenting and testing its own derivations.

---

## Relationship to Sibling Feeds

The DoubleZero Order-Intent Feed is a sibling of the [Top-of-Book & Trades Feed](../top-of-book/spec.md), the [Market-by-Order Feed](../market-by-order/spec.md), and the [Midpoint Feed](../midpoint/spec.md). Sibling feeds share:

- The 24-byte frame header layout (except for the `Magic` value).
- The 4-byte application message header.
- The [Reference Data Distribution supplement](../reference-data/spec.md) conformance, including `InstrumentDefinition` (`0x02`) and `ManifestSummary` (`0x07`).
- The cross-spec message Type IDs `0x01` (Heartbeat) and `0x07` (ManifestSummary) byte-for-byte.
- The `Reset Count` reset pattern.
- The forward-compatibility rules.

Distinctions of the order-intent feed:

- `Magic` is `0x494F` (vs. `0x445A` top-of-book, `0x4444` market-by-order, `0x4D44` midpoint).
- It has **no session-boundary message** — the siblings end a session with `EndOfSession` (`0x06`); this real-time intent stream has no session concept (a publisher restart is signaled by a `Reset Count` change), so `0x06` is reserved and unused.
- It carries **pre-consensus intent**, not accepted book state — events are observed submissions, never fills (see [Trust semantics](#doublezero-order-intent-feed)).
- It does **not** carry `Trade` (`0x04`) or any quote/book payload; those Type IDs are not used here.
- It has **no snapshot/recovery mechanism** — there is no canonical state to snapshot.
- Order-intent-specific payload Type IDs live in `0x30`–`0x3F`.

A publisher MAY operate any subset of the sibling feeds for the same instruments simultaneously. Subscribers MAY consume any subset independently.

---

## Versioning and Forward Compatibility

The Schema Version byte in the frame header is `1` for this release (spec version **0.1.0**). Future versions of this specification MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types in currently-reserved Type ID ranges (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields such as Side, Order Type, TIF, and Grouping (decoders MUST accept any `u8` value and treat unknowns as `0`).
- Define new Event Flag bits (decoders MUST ignore flag bits they do not recognize).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.
