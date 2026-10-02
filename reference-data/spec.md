# DoubleZero Edge Reference Data Distribution

This supplement defines a continuous in-band mechanism for DoubleZero Edge feeds to advertise their published instrument set. It allows new subscribers to reach a complete reference-data state without an offline file, an out-of-band catalog, or a replay service.

The mechanism is payload-independent. Any feed in the DoubleZero Edge family that uses the shared 24-byte frame header and 4-byte application message header MAY adopt it. All six currently do: the Top-of-Book & Trades, Midpoint, Market-by-Order, Market-by-Price, Order-Intent, and Perp Stats feeds each carry `ManifestSummary` on their `refdata` port and a `Manifest Seq` field in their `InstrumentDefinition`.

This document specifies version **1.1.0**: the two-port transport model, the `ManifestSummary` message, the `StrikeInterval` message, the publisher cadence requirements, and the subscriber algorithm.

---

## Motivation

Continuous markets — crypto spot, prediction markets, perpetuals — do not have natural session boundaries. A subscriber that joins the multicast feed at an arbitrary moment must be able to:

1. Discover which instruments the publisher considers active.
2. Receive an `InstrumentDefinition` for each one.
3. Detect when it has a complete view.
4. Detect when the published set changes (instruments added or removed).

Without an in-band mechanism, the alternatives are an offline catalog file (which drifts), a replay service (which adds infrastructure), or a definition retransmission triggered by some out-of-band signal (which doesn't exist in a multicast-only world). This supplement provides the in-band mechanism.

---

## Two-Port Transport Model

A channel adopting this mechanism uses **one multicast group with two destination ports**:

| Port | Purpose | Carries |
|------|---------|---------|
| mktdata | Live market data | Feed-specific market data path messages (e.g., `Quote`, `Trade`, `Midpoint`), `Heartbeat`, `EndOfSession` |
| refdata | Instrument metadata and channel state | `InstrumentDefinition`, `ManifestSummary`, `StrikeInterval` |

The frame header and application message header are identical on both ports. A single decoder implementation handles both. Concrete port assignments are out of scope for this supplement; each feed deployment publishes its port mapping out of band (e.g., in service discovery or in operator documentation).

A subscriber bootstrapping from a cold start MUST bind both ports. A subscriber that already has out-of-band `InstrumentDefinition` data MAY bind only the market data port; in that case it forfeits the in-band reference-data mechanism described here.

### Why Two Ports, Not Two Multicast Groups

At the bandwidth scales this mechanism targets (see Bandwidth Considerations below), reference-data traffic is small enough that splitting into a separate multicast group provides no NIC-filter benefit worth the operational cost of provisioning, IGMP-joining, and managing a second group per channel. A single multicast group with two destination ports gives the same logical separation with simpler operations.

---

## ManifestSummary Message (24 bytes)

A new application message type, advertised periodically on the reference-data port.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x07`, Length=24 |
| 4  | Channel ID | `u8` | Redundant with frame header; useful for standalone logging |
| 5  | Valid | `u8` | `1` when the channel has an established instrument set; `0` when the publisher is uninitialized or the channel is inactive. See Publisher Behavior. |
| 6  | Reserved | 2B | Padding |
| 8  | Manifest Seq | `u16` | Increments every time the published instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the published set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

`ManifestSummary` carries no list of Instrument IDs. The combination of `Manifest Seq` and `Instrument Count`, together with the `Manifest Seq` field on each `InstrumentDefinition` (see below), is sufficient for a subscriber to determine when it has a complete view.

`Manifest Seq` is `u16` here for symmetry with the `Manifest Seq` field on `InstrumentDefinition`, so subscribers can compare them directly without width conversion.

---

## Manifest Seq on InstrumentDefinition

Every feed adopting this supplement MUST add a `Manifest Seq (u16)` field to its `InstrumentDefinition` message. The field carries the value of the publisher's current `Manifest Seq` at the time the definition was emitted.

The field is `u16` (not `u32`) so that it would fit within the Reserved space already present in the `InstrumentDefinition` layouts when this supplement was introduced, without bumping the message length at the time. Subscribers MUST compare manifest sequence numbers using modular ordering (i.e., `(new − old) mod 65536 < 32768` means `new` is later). If wraparound becomes a practical concern, a later schema version may widen the field.

The exact byte offset of `Manifest Seq` within `InstrumentDefinition` is feed-specific and is documented in each feed's spec.

---

## StrikeInterval Message (40 bytes)

Some contracts pay on a comparison between a value and a strike. Examples are "above 92,799.99", "below 74,200" and "between 92,700 and 92,799.99". `InstrumentDefinition` has no field for a strike. `StrikeInterval` carries the strike of one instrument as an interval: a lower bound, an upper bound, or both.

The **settlement value** is the value that the venue compares with the strike. The contract pays when the settlement value is inside the interval. This message does not identify the settlement value. See [What StrikeInterval Does Not Carry](#what-strikeinterval-does-not-carry).

A venue can fix a strike after it lists the instrument. Until the venue fixes the strike, the strike is **pending**.

| Offset | Field | Type | Description |
|--------|-------|------|-------------|
| 0  | Header | 4B | Type=`0x09`, Length=40 |
| 4  | Instrument ID | `u32` | The instrument that this strike applies to. The same value as in the `InstrumentDefinition` that this message follows. |
| 8  | Source ID | `u16` | The matching engine, as assigned by the [Source ID Registry](../sources/spec.md). Where the `InstrumentDefinition` of the feed carries a `Source ID`, this is the same value. |
| 10 | Strike Exponent | `i8` | The decimal exponent of both bounds. For example, `-2` means that a bound is its raw value divided by 100. This field is independent of `Price Exponent`. `Price Exponent` applies to the price of the contract. `Strike Exponent` applies to the settlement value. |
| 11 | Bound Flags | `u8` | Bit 0: `Lower Bound` is present. Bit 1: `Lower Bound` is inclusive. Bit 2: `Upper Bound` is present. Bit 3: `Upper Bound` is inclusive. Bits 4–7: reserved. See [Bound Flags](#bound-flags). |
| 12 | Lower Bound | `i64` | The raw value of the lower bound. Uses `Strike Exponent`. `0` when bit 0 of `Bound Flags` is clear. |
| 20 | Upper Bound | `i64` | The raw value of the upper bound. Uses `Strike Exponent`. `0` when bit 2 of `Bound Flags` is clear. |
| 28 | Fixing Time | `ts_ns` | The time at which the venue fixed the strike. `0` while the strike is pending. `0` when the venue fixed the strike at listing. `0` when the time is not available. |
| 36 | Reserved | 4B | Padding. A publisher MUST set these bytes to `0`. |

The message is feed-independent. `Instrument ID` is the key, and the message reads no field of `InstrumentDefinition`. Thus it applies to the 130-byte `InstrumentDefinition` and to the 64-byte variant of the Midpoint feed.

The Type ID is `0x09`. The DoubleZero Edge family shares one Type ID space, and no feed in the family uses `0x09` for a different payload. A feed adopts this message when its spec lists `0x09` in its message table. The Top-of-Book & Trades feed and the Market-by-Price feed do. In a feed that does not list it, `0x09` is reserved for this payload.

### Bound Flags

A bound that is present is one end of the interval. A bound that is not present means that the interval has no limit on that side.

| Bit 0 | Bit 1 | Bit 2 | Bit 3 | `Bound Flags` | The contract pays when the settlement value `v` is |
|-------|-------|-------|-------|---------------|-----------------------------------------------------|
| 1 | 0 | 0 | 0 | `0b0001` | `v > Lower Bound` |
| 1 | 1 | 0 | 0 | `0b0011` | `v >= Lower Bound` |
| 0 | 0 | 1 | 0 | `0b0100` | `v < Upper Bound` |
| 0 | 0 | 1 | 1 | `0b1100` | `v <= Upper Bound` |
| 1 | 1 | 1 | 1 | `0b1111` | `Lower Bound <= v <= Upper Bound` |
| 0 | 0 | 0 | 0 | `0b0000` | The strike is pending. |

The table shows the common values. Three other values have both bounds present and one or two exclusive ends: `0b0101`, `0b0111` and `0b1101`. They are also valid. A subscriber applies one rule to all of them:

```
def pays(v, strike):
    f = strike.bound_flags
    if (f & 0b0101) == 0:
        return unknown                      # pending
    if f & 0b0001:                          # lower bound present
        if f & 0b0010:
            if v <  strike.lower_bound: return false
        else:
            if v <= strike.lower_bound: return false
    if f & 0b0100:                          # upper bound present
        if f & 0b1000:
            if v >  strike.upper_bound: return false
        else:
            if v >= strike.upper_bound: return false
    return true
```

`v` and the bounds use the same exponent in this comparison.

A publisher MUST obey these rules:

- A publisher MUST clear bit 1 when bit 0 is clear, and MUST clear bit 3 when bit 2 is clear. A bound that is not present cannot be inclusive.
- A publisher MUST set a bound to `0` when its presence bit is clear.
- A publisher MUST clear bits 4–7. A subscriber MUST ignore bits 4–7.
- When both bounds are present, `Lower Bound` MUST NOT be greater than `Upper Bound`. When they are equal, both bounds MUST be inclusive. Otherwise the interval is empty, and the contract can never pay.
- While the strike is pending, a publisher MUST clear bits 0–3, and MUST set both bounds and `Fixing Time` to `0`.

An interval with equal, inclusive bounds is a point. A contract that pays on "exactly 9" has `Lower Bound = Upper Bound = 9` and `Bound Flags = 0b1111`.

Presence bits are used and not an enumerated strike type. An enumerated value fixes which bounds exist and which are inclusive, so each new venue convention needs a new value. With presence bits, a subscriber applies one rule to every combination. `Update Flags` in the Top-of-Book `Quote` uses the same pattern.

### StrikeInterval Publisher Behavior

A publisher that implements this message MUST obey these rules. The rules in [Publisher Behavior](#publisher-behavior) continue to apply.

1. **Send a `StrikeInterval` for each instrument whose contract pays on a strike.** The publisher MUST send it from the first `InstrumentDefinition` of the instrument. Before the venue fixes the strike, the publisher sends a pending `StrikeInterval`. The publisher MUST NOT send a `StrikeInterval` for an instrument whose contract does not pay on a strike.

2. **Put the `StrikeInterval` immediately after its `InstrumentDefinition`, in the same datagram.** This applies to the first transmission and to each retransmission. The publisher MUST NOT send a `StrikeInterval` in a different position. Thus an `InstrumentDefinition` and its `StrikeInterval` are a pair of 170 bytes with the 130-byte definition, and a publisher MUST NOT divide the pair between two datagrams. `Liquidation` and its `Trade` are the precedent for a pair in one datagram.

3. **Send the pair when the venue fixes the strike.** When a pending strike becomes fixed, the publisher SHOULD send the `InstrumentDefinition` and the fixed `StrikeInterval` immediately. The publisher SHOULD NOT wait for the position of the instrument in the definition cycle. The definition cycle period is a maximum interval, so an early retransmission obeys Publisher Behavior rule 1.

4. **Do not change `Manifest Seq`.** A `StrikeInterval` does not change the published set. A change from pending to fixed does not increment `Manifest Seq` and does not change `Instrument Count`.

5. **Do not return a fixed strike to pending.** After the publisher sends a fixed `StrikeInterval` for an instrument, each later `StrikeInterval` for that instrument in the same era MUST be fixed. The publisher MUST NOT stop sending the `StrikeInterval` of an instrument while the instrument is in the published set.

6. **Change a fixed strike only when the venue corrects it.** The last `StrikeInterval` that the publisher sends for an instrument is the current strike. A publisher MUST NOT send a different fixed strike for a reason other than a correction by the venue.

7. **Derive the bounds from the comparison that the venue states.** Do not derive them from which venue values are set. A venue can set a value that the comparison does not use. For example, a venue can set a cap value of `0` on a contract that pays "at or above" a floor. That contract has a lower bound only.

### What StrikeInterval Does Not Carry

`StrikeInterval` carries the bounds. It does not identify the settlement value. `Leg1` in `InstrumentDefinition` names an asset, not a settlement value: a contract can settle on an average of an index, on a temperature at a weather station, or on a count that a company reports. No field in the family identifies these. A subscriber uses `Symbol` to find the rules of the venue, and the rules name the settlement value.

Values that a subscriber can derive from a set of strikes, such as an implied price, are not venue facts. This message does not carry them.

---

## Publisher Behavior

A publisher operating a channel adopting this supplement MUST:

1. **Retransmit every active `InstrumentDefinition` periodically.** The maximum interval between successive retransmissions of any single definition is the **definition cycle period**. Recommended cycle period: **30 seconds**.

2. **Spread retransmissions across the cycle.** Definitions SHOULD be paced evenly over the cycle period. Publishers MUST NOT emit the entire published set as a single burst. MTU-packed frames evenly spaced over the cycle is the canonical implementation.

3. **Emit `ManifestSummary` periodically on the reference-data port.** The maximum interval between `ManifestSummary` messages is the **manifest cadence**. Recommended cadence: **1 second**. The manifest cadence MUST be shorter than the definition cycle period so that a new subscriber sees a `ManifestSummary` before it has finished collecting definitions.

4. **Set the `Valid` flag to reflect channel state.** A publisher MUST set `Valid = 1` on every `ManifestSummary` once its instrument set is established, and `Valid = 0` when the channel is uninitialized or the publisher is shutting down.

5. **Bump `Manifest Seq` atomically when the published set changes.** When an instrument is added to or removed from the published set, the publisher MUST:
   - (a) Increment `Manifest Seq` by 1.
   - (b) Tag all subsequent `InstrumentDefinition` retransmissions with the new `Manifest Seq` value.
   - (c) Emit a `ManifestSummary` with `Valid = 1` carrying the new `Manifest Seq` and the new `Instrument Count` no later than the next manifest cadence interval.

6. **Restart the definition cycle on `Manifest Seq` change.** When `Manifest Seq` bumps, the publisher SHOULD begin a fresh cycle of definition retransmissions tagged with the new seq, so that subscribers can collect a complete set under the new seq within one cycle period.

7. **Reset via the frame header.** To reset the channel, the publisher increments `Reset Count` in the frame header and resets `Sequence Number` to 0. The publisher's `Manifest Seq` MAY restart from any value. Subscribers detect the reset by comparing `Reset Count` against their last-seen value and discard all cached state (see below). The reset takes effect at the beginning of the frame: all application messages in a frame carrying a new `Reset Count` belong to the post-reset era. A publisher that needs to reset MUST discard any partially constructed frame and start a new frame with the incremented `Reset Count`.

---

## Subscriber Algorithm

A subscriber adopting this mechanism maintains the following state per channel:

```
state = {
  valid: false,            // bool
  latest_seq: 0,           // u16
  expected_count: 0,       // u32
  last_reset_count: 0,     // u8
  defs: {},                // map: Instrument ID → InstrumentDefinition
  strikes: {}              // map: Instrument ID → StrikeInterval
}
```

State transitions:

```
on frame_header(reset_count):
    if reset_count != state.last_reset_count:
        state = { valid: false, latest_seq: 0, expected_count: 0,
                  last_reset_count: reset_count, defs: {}, strikes: {} }

on ManifestSummary(valid, seq, count):
    if not valid:
        state.valid          = false
        state.latest_seq     = 0
        state.expected_count = 0
        state.defs           = {}
        state.strikes        = {}
        return
    if not state.valid or seq is later than state.latest_seq:
        state.valid          = true
        state.latest_seq     = seq
        state.expected_count = count
        state.defs           = {}    // discard definitions tagged with the old seq
        state.strikes        = {}

// next is the message that follows def in the same datagram, if there is one
on InstrumentDefinition(def, next):
    if state.valid and def.manifest_seq == state.latest_seq:
        state.defs[def.instrument_id] = def
        if next is a StrikeInterval and next.instrument_id == def.instrument_id:
            state.strikes[def.instrument_id] = next
        else:
            delete state.strikes[def.instrument_id]
    // definitions tagged with any other seq are discarded

ready() = state.valid
       and len(state.defs) == state.expected_count
```

A subscriber that receives a market data path message (e.g., `Quote`, `Midpoint`) for an Instrument ID before `ready()` returns true SHOULD either buffer the message until it has the corresponding `InstrumentDefinition`, or drop it, according to its application policy. A subscriber that receives a market data path message for an Instrument ID *not* present in `state.defs` after `ready()` returns true SHOULD drop the message and wait for the next `ManifestSummary`, which may indicate a set change.

### Strike State

A subscriber reads the strike of an instrument from the datagram that carries its `InstrumentDefinition`. `state.strikes` has the same lifetime as `state.defs`.

1. **A subscriber that does not implement `StrikeInterval` skips it by `Message Length`.** Its `state.defs` and its `ready()` are the same as before.
2. **An `InstrumentDefinition` with a `StrikeInterval` after it gives a fixed strike or a pending strike.** An `InstrumentDefinition` without one means that the contract does not pay on a strike. A subscriber that joins late does not wait a cycle to know which case applies.
3. **`ready()` does not change.** A subscriber that holds every definition also holds every strike, fixed or pending.
4. **The last `StrikeInterval` for an instrument is the current strike.** A subscriber replaces the strike that it holds each time it accepts the definition.
5. **A subscriber MUST ignore a `StrikeInterval` that does not immediately follow an `InstrumentDefinition` with the same `Instrument ID`.**

Rule 2 applies only to a publisher that implements this message. `MINOR` versions are not visible on the wire, so a subscriber cannot tell from a datagram whether a publisher implements `StrikeInterval`. The operator of a feed states it out of band.

### Modular Ordering for Manifest Seq

The `Manifest Seq` field is `u16` and wraps. A subscriber comparing two seq values `a` and `b` MUST use modular ordering:

```
def is_later(b, a):  # returns true if b is "after" a
    return ((b - a) mod 65536) != 0 and ((b - a) mod 65536) < 32768
```

This is the same wraparound-safe comparison used by TCP sequence numbers (RFC 1323) and is well-defined as long as no more than 32,767 set-changes occur between two observations by the same subscriber.

---

## Bandwidth Considerations

For a channel with **N** active instruments, definition size **D** bytes, cycle period **T** seconds, and manifest cadence **M** seconds:

- **Definition retransmission rate:** `N × D / T` bytes/second
- **ManifestSummary rate:** `24 / M` bytes/second
- **Total reference-data port rate:** `N × D / T + 24 / M`

Worked example for the Top-of-Book & Trades Feed at the recommended settings (N=1000, D=130, T=30, M=1):

```
1000 × 130 / 30 + 24 / 1 = 4,333 + 24 ≈ 4,357 bytes/sec ≈ 35 kbps
```

Definitions pack into frames at 9 per frame (1,170 + 24-byte frame header = 1,194 bytes), giving `1000 / 9 ≈ 112` frames per cycle, or roughly one frame every 268 ms — comfortably within any modern network's burst tolerance.

An instrument with a `StrikeInterval` uses 170 bytes each cycle and not 130. A datagram of the maximum size, 1,232 bytes, holds 7 pairs (1,190 + 24-byte datagram header = 1,214 bytes). It holds 9 definitions without a `StrikeInterval`. If all 1000 instruments have a strike, the rate is `1000 × 170 / 30 + 24 ≈ 5,691 bytes/sec ≈ 46 kbps`, in `1000 / 7 ≈ 143` datagrams per cycle.

For the Midpoint Feed (D=64), the rate is `1000 × 64 / 30 + 24 ≈ 2,157 bytes/sec ≈ 18 kbps`.

Both numbers are negligible compared to typical market data path throughput. The mechanism is designed to be operationally invisible at B-scale (~1,000 instruments per channel).

---

## Cold-Start Latency

A new subscriber's worst-case time to `ready()` after binding both ports is bounded by:

```
worst_case_ready = manifest_cadence + definition_cycle_period
                 = M + T
```

At recommended settings (M=1s, T=30s), this is **~31 seconds**.

The subscriber sees a `ManifestSummary` within `M` seconds (worst case), then waits up to `T` seconds for a full pass of `InstrumentDefinition` retransmissions. Subscribers requiring faster cold-start can negotiate a shorter cycle period with the operator; the spec sets the recommended values, not hard caps.

---

## Forward Compatibility

This document is version **1.1.0**. It is a supplement, not a feed: it defines a mechanism that rides on a host feed's frame header and therefore has no `Magic` and no `Schema Version` of its own. Frames carrying it are versioned by the host feed. It is versioned independently of the feed specs that adopt it, because a subscriber and publisher operating under the same version of this supplement interoperate correctly regardless of which feed specs they implement. See the [Versioning Policy](../VERSIONING.md) for the full scheme.

Future `1.x` versions of this supplement MAY, without a host-feed Schema Version bump:

- Add optional fields to `ManifestSummary` (append-only; old decoders ignore trailing bytes).
- Define new reference-data message types in currently-reserved type ID ranges.
- Add an optional `manifest_hash` field to `ManifestSummary` for defense against publisher bugs in single- or multi-publisher channels.

The following is a known candidate that would be **breaking**, requiring a MAJOR release of this supplement and a coordinated MAJOR release plus Schema Version bump of every host feed that adopts it:

- Widen `Manifest Seq` to `u32`.

The two-port transport model and the subscriber algorithm are stable for the `1.x` line.

### Changes

**1.1.0** — additive. Added the `StrikeInterval` message (`0x09`, 40 bytes), which carries the strike of a contract as an interval, with its publisher rules and the subscriber strike state. A subscriber that does not implement it skips it by `Message Length`; `ready()`, `Manifest Seq` and `ManifestSummary` do not change. Corrected the bandwidth worked example to the 130-byte `InstrumentDefinition` (was 128 bytes). No host-feed Schema Version change.

**1.0.2** — editorial. Replaced "epoch" with "era" for the post-reset span, dropped the redundant qualifier in "publisher operator", and adopted the glossary's "published set". No wire change.

**1.0.1** — editorial. Refreshed the bandwidth worked example for the 128-byte `InstrumentDefinition` introduced by the feed specs at their `2.0.0` (was 80 bytes), and corrected the `Manifest Seq` width rationale, which described reserved space that the widened layout no longer has. No change to this supplement's own requirements or to `ManifestSummary`.

**1.0.0** — first stable release. Promoted from the `0.1.0` draft with no wire change.
