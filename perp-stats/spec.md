# DoubleZero Perp Stats Feed

The DoubleZero Perp Stats Feed is a sibling cadence feed carrying per-perpetual derived state — funding, mark price, oracle price, open interest, and premium — relayed from the venue's REST surface. It is not the order-book hot path; data originates from REST polls rather than the matching engine, so it runs on a separate cadence from the top-of-book and market-by-order feeds.

This document specifies version **0.1.0**: the transport, message types, `PerpStats` wire layout, emission model, and instrument scope.

---

## Design Principles

1. **Little-endian.** Native for x86-64 and ARM64.
2. **Fixed-size messages.** Every message type has a constant length. No variable-length fields, no repeating groups.
3. **Schema-versioned.** The frame header carries a version byte. New fields append to messages; old decoders ignore trailing bytes. Unknown message types are skipped using the Message Length field.
4. **Multicast-native.** UDP multicast delivery. One frame per UDP datagram.
5. **Instrument-ID based.** Numeric `u32` IDs on the market data path. Human-readable strings only in reference data.
6. **Source-attributed.** Every `PerpStats` message carries a `u16` Source ID identifying the venue.
7. **Pure relay, no computation.** Every field traces one-to-one to a value the venue publishes. String-to-fixed-point encodings are lossless transcriptions at the field's declared precision.
8. **Cadence-matched.** REST-poll data belongs on a REST-cadence feed, not co-located with matching-engine events.

---

## Transport Framing

This feed uses the same 24-byte frame header and 4-byte application message header as the top-of-book feed. These are defined in the [Top-of-Book & Trades Feed spec](../top-of-book/spec.md) and are not restated here.

### Two-Port Channel Model

Each channel is delivered to **one multicast group on two destination ports**, per the [Reference Data Distribution supplement](../reference-data/spec.md):

| Port | Carries |
|------|---------|
| mktdata | `PerpStats`, `Heartbeat`, `EndOfSession` |
| refdata | `InstrumentDefinition`, `ManifestSummary` |

A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port.

---

## Data Types

The `price`, `qty`, and `ts_ns` types are reused from the [Top-of-Book & Trades Feed spec](../top-of-book/spec.md). Two additional types are introduced by this feed:

| Type | Size | Description |
|------|------|-------------|
| `u8` | 1 | Unsigned 8-bit integer |
| `u16` | 2 | Unsigned 16-bit integer, little-endian |
| `u32` | 4 | Unsigned 32-bit integer, little-endian |
| `u64` | 8 | Unsigned 64-bit integer, little-endian |
| `i64` | 8 | Signed 64-bit integer, little-endian |
| `ts_ns` | 8 | Nanoseconds since Unix epoch (`u64`) |
| `price` | 8 | Signed 64-bit integer with per-instrument implied exponent (`i64`) |
| `qty` | 8 | Unsigned 64-bit integer with per-instrument implied exponent (`u64`) |
| `rate` | 8 | `i64` with a **fixed** implied exponent of `−12` (value = raw × 10⁻¹²). For dimensionless rates: funding, predicted funding, premium. The exponent is a protocol constant — not per-instrument. |
| `notional` | 8 | `u64` with a **fixed** implied exponent of `−2` (value = raw × 10⁻², i.e. cents). For quote-currency monetary totals: day notional volume. The per-instrument Qty Exponent is for base units; notional needs its own fixed scale. The exponent is a protocol constant — not per-instrument. |

`price` fields use the instrument's **Price Exponent** and `qty` fields use its **Qty Exponent**, both carried on this feed's `InstrumentDefinition`. `rate` and `notional` exponents are fixed protocol constants and do not vary per instrument.

---

## Message Types

| Type ID | Name | Size | Port | Description |
|---------|------|------|------|-------------|
| `0x01` | Heartbeat | 16 | mktdata | Channel liveness signal |
| `0x02` | InstrumentDefinition | 80 | refdata | Reference data for a perpetual instrument |
| `0x06` | EndOfSession | 12 | mktdata | No more data for this session |
| `0x07` | ManifestSummary | 24 | refdata | Active instrument set summary (see supplement) |
| `0x30` | PerpStats | 124 | mktdata | Per-perpetual derived state snapshot |

Type IDs `0x03`, `0x04`, `0x05`, and `0x08` are intentionally not carried on this feed. `0x03` (`Quote`), `0x04` (`Trade`), and `0x08` (`Liquidation`) are message types of the top-of-book and market-by-order feeds, and `0x05` is reserved there; leaving them absent here prevents accidental cross-decoding if a frame is misrouted between feeds.

A decoder encountering an unknown type MUST skip the message using its Message Length field and continue parsing the frame.

---

## Message Definitions

### 0x01 Heartbeat (16 bytes)

Sent when there is no other traffic. Receivers use this for stale-connection detection.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x01`, Length=16 |
| 4 | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5 | Reserved | 3B | Padding |
| 8 | Timestamp | `ts_ns` | Current time |

### 0x02 InstrumentDefinition (80 bytes)

Maps a numeric Instrument ID to human-readable metadata. Carried on the reference data port and retransmitted continuously per the [Reference Data Distribution supplement](../reference-data/spec.md). Not on the market data path.

This feed uses the same 80-byte `InstrumentDefinition` layout as the top-of-book feed; see that spec for the full field table. The `InstrumentDefinition` set on this feed covers perpetual instruments only (see Instrument Scope).

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
| 12 | Instrument Count | `u32` | Number of instruments currently in the active set (perps only) |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

### 0x30 PerpStats (124 bytes)

A per-perpetual derived state snapshot, relayed from the venue's REST surface. One message per active perpetual instrument per poll sweep. Fields whose upstream value is momentarily unavailable encode as `0`.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0 | Header | 4B | Type=`0x30`, Length=124 |
| 4 | Instrument ID | `u32` | Perpetual instrument |
| 8 | Source ID | `u16` | Upstream venue |
| 10 | Funding Interval Hours | `u8` | Funding period length (1 for HL) |
| 11 | Reserved | `u8` | 0 |
| 12 | Source Timestamp | `ts_ns` | When this poll's data was fetched |
| 20 | Mark Price | `price` | Uses instrument Price Exponent |
| 28 | Oracle Price | `price` | Uses instrument Price Exponent |
| 36 | Mid Price | `price` | Uses instrument Price Exponent |
| 44 | Prev Day Price | `price` | Uses instrument Price Exponent |
| 52 | Impact Bid Price | `price` | Uses instrument Price Exponent |
| 60 | Impact Ask Price | `price` | Uses instrument Price Exponent |
| 68 | Open Interest | `qty` | Base units; uses instrument Qty Exponent |
| 76 | Day Base Volume | `qty` | Base units; uses instrument Qty Exponent |
| 84 | Day Notional Volume | `notional` | Quote-currency total (cents) |
| 92 | Funding | `rate` | Current funding rate |
| 100 | Predicted Funding | `rate` | Next-interval predicted rate |
| 108 | Premium | `rate` | Mark/oracle premium |
| 116 | Next Funding Time | `ts_ns` | Next funding boundary |

Total: 124 bytes. Offset arithmetic: 4 + 4 + 2 + 1 + 1 + 8 + (6 × 8) + (2 × 8) + 8 + (3 × 8) + 8 = 124.

The Mid Price field carries the poll-cadence mid derived from the venue REST response. This is distinct from the top-of-book feed's co-located mid, which is computed from live book state.

---

## Instrument Scope

This feed covers **perpetual instruments only** — core perpetuals and builder-DEX (HIP-3) perpetuals. Spot instruments are not emitted. The feed's `InstrumentDefinition` set and `ManifestSummary` `Instrument Count` reflect perps only.

Funding, mark price, oracle price, open interest, and premium are perpetual-futures concepts with no spot equivalent. Spot instruments from the same venue are carried on the top-of-book and midpoint feeds but are not included here.

---

## Emission and Recovery

### Two Poll Loops, One Merged Message

The publisher runs two REST poll loops and merges their results into each `PerpStats` message:

- **`metaAndAssetCtxs`** — the freshness driver (mark price, oracle price, mid price, open interest, premium, impact prices, volume). One request returns all active perps. Default cadence: **1 second**, configurable. This sets the sweep rate.
- **`predictedFundings`** — predicted funding rate, next funding time, and funding interval. Funding accrues on an hourly cycle, so this is polled more slowly. Default cadence: **60 seconds**, configurable. The most-recent values are merged into every `PerpStats` message until the next `predictedFundings` poll.

Only the `HlPerp` entry of `predictedFundings` is relayed. CEX entries (Binance, Bybit, etc.) are the venue re-publishing other venues' data and are out of scope.

### Full Sweep Per Poll

On each `metaAndAssetCtxs` poll, the publisher emits a **full sweep**: one `PerpStats` message per active perpetual, packed into frames up to the 1,232-byte MTU (~28 KB for ~230 perps, ~20 frames per sweep, ~0.2 Mbps at 1 s cadence). The interval between sweeps equals the poll cadence.

This is a deliberate departure from the Reference Data Distribution supplement's *cycling* model for `InstrumentDefinition`. That model spreads definitions evenly across a 30-second cycle and MUST NOT burst, because definitions rarely change and the goal is steady-state recovery coverage. `PerpStats` is the opposite: the entire active set genuinely refreshes every poll, so emitting the full sweep promptly per poll is correct, not a retransmission violation.

### Recovery and Late Joiners

The full sweep **is** the recovery mechanism. There are no deltas; every `PerpStats` message is a complete current snapshot for its instrument. A late-joining subscriber:

1. Binds both the `mktdata` and `refdata` ports.
2. Collects `InstrumentDefinition` messages and a `ManifestSummary` (which carries the expected perp instrument count).
3. After one full sweep, has a complete, current view of every active perpetual.

Anchor time is at most one poll interval (~1 s at default cadence). No dedicated snapshot port or sequence-recovery model is needed. `Heartbeat` is still emitted on `mktdata` per convention for liveness during any gap; in practice the 1 s sweep provides continuous traffic.

---

## Session Lifecycle

A typical publisher session proceeds as follows:

1. Publisher starts → increments `Reset Count` in the frame header and resets `Sequence Number` to 0.
2. Begins emitting **InstrumentDefinition** for every active perpetual on the reference data port, paced evenly across the definition cycle period (recommended 30 s). Definitions are retransmitted continuously.
3. Begins emitting **ManifestSummary** with `Valid = 1` on the reference data port at the manifest cadence (recommended 1 s).
4. On each `metaAndAssetCtxs` poll, emits a full sweep of **PerpStats** messages on the market data port.
5. When the market data path is idle → sends **Heartbeat** on the market data port.
6. When the active instrument set changes → bumps `Manifest Seq`, retags subsequent `InstrumentDefinition` retransmissions, and emits an updated `ManifestSummary` within the manifest cadence interval.
7. On shutdown → sends **EndOfSession** on the market data port.

The publisher MUST follow the cadence and atomicity rules in the [Reference Data Distribution supplement](../reference-data/spec.md).

---

## Versioning and Forward Compatibility

This document is version **0.1.0**, versioned independently of the sibling feed specs.

The Schema Version byte in the frame header is `1` for this release. Future versions of this specification MAY:

- Append new fields to existing messages (old decoders ignore trailing bytes within the declared Message Length).
- Define new message types (old decoders skip unknown types using the Message Length field).
- Define new values for enumerated fields (decoders MUST accept any `u8` value).

Existing field layouts and semantics will not change within the v0.x line without a Schema Version bump.

---

## Relationship to Sibling Feeds

The DoubleZero Perp Stats Feed is one of a family of sibling protocols sharing framing conventions:

- The **[Top-of-Book & Trades Feed](../top-of-book/spec.md)** carries two-sided BBO quotes and trade reports from a venue, on the matching-engine hot path.
- The **[Midpoint Feed](../midpoint/spec.md)** carries a single derived mid price per instrument.
- The **[Market-by-Order Feed](../market-by-order/spec.md)** carries the full resting-order population per instrument.
- The **Perp Stats Feed** (this document) carries per-perpetual derived state — funding, mark, oracle, OI, premium — relayed from the venue REST surface on a cadence path.

Sibling feeds share the 24-byte frame header, the 4-byte application message header, and the forward-compatibility rules. They differ in magic value, message type table, and payload. Sibling feeds MUST use distinct Magic values and SHOULD use distinct multicast groups.
