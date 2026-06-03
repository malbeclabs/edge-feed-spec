# DoubleZero Edge Reference Data Distribution

This supplement defines a continuous in-band mechanism for DoubleZero Edge feeds to advertise their active instrument set. It allows new subscribers to reach a complete reference-data state without an offline file, an out-of-band catalog, or a replay service.

The mechanism is payload-independent. Any feed in the DoubleZero Edge family that uses the shared 24-byte frame header and 4-byte application message header MAY adopt it. The Top-of-Book & Trades Feed and the Midpoint Feed both do.

This document specifies version **0.1.0**: the two-port transport model, the `ManifestSummary` message, the publisher cadence requirements, and the subscriber algorithm.

---

## Motivation

Continuous markets — crypto spot, prediction markets, perpetuals — do not have natural session boundaries. A subscriber that joins the multicast feed at an arbitrary moment must be able to:

1. Discover which instruments the publisher considers active.
2. Receive an `InstrumentDefinition` for each one.
3. Detect when it has a complete view.
4. Detect when the active set changes (instruments added or removed).

Without an in-band mechanism, the alternatives are an offline catalog file (which drifts), a replay service (which adds infrastructure), or a definition retransmission triggered by some out-of-band signal (which doesn't exist in a multicast-only world). This supplement provides the in-band mechanism.

---

## Two-Port Transport Model

A channel adopting this mechanism uses **one multicast group with two destination ports**:

| Port | Purpose | Carries |
|------|---------|---------|
| mktdata | Live market data | Feed-specific market data path messages (e.g., `Quote`, `Trade`, `Midpoint`), `Heartbeat`, `EndOfSession` |
| refdata | Instrument metadata and channel state | `InstrumentDefinition`, `ManifestSummary` |

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
| 8  | Manifest Seq | `u16` | Increments every time the active instrument set changes on this channel |
| 10 | Reserved | 2B | Padding |
| 12 | Instrument Count | `u32` | Number of instruments currently in the active set |
| 16 | Timestamp | `ts_ns` | When the publisher emitted this summary |

`ManifestSummary` carries no list of Instrument IDs. The combination of `Manifest Seq` and `Instrument Count`, together with the `Manifest Seq` field on each `InstrumentDefinition` (see below), is sufficient for a subscriber to determine when it has a complete view.

`Manifest Seq` is `u16` here for symmetry with the `Manifest Seq` field on `InstrumentDefinition`, so subscribers can compare them directly without width conversion.

---

## Manifest Seq on InstrumentDefinition

Every feed adopting this supplement MUST add a `Manifest Seq (u16)` field to its `InstrumentDefinition` message. The field carries the value of the publisher's current `Manifest Seq` at the time the definition was emitted.

The field is `u16` (not `u32`) so that it fits within the Reserved space already present in the `InstrumentDefinition` layouts of the Top-of-Book & Trades Feed and the Midpoint Feed without bumping the message length. Subscribers MUST compare manifest sequence numbers using modular ordering (i.e., `(new − old) mod 65536 < 32768` means `new` is later). If wraparound becomes a practical concern, a later schema version may widen the field.

The exact byte offset of `Manifest Seq` within `InstrumentDefinition` is feed-specific and is documented in each feed's spec.

---

## Publisher Behavior

A publisher operating a channel adopting this supplement MUST:

1. **Retransmit every active `InstrumentDefinition` periodically.** The maximum interval between successive retransmissions of any single definition is the **definition cycle period**. Recommended cycle period: **30 seconds**.

2. **Spread retransmissions across the cycle.** Definitions SHOULD be paced evenly over the cycle period. Publishers MUST NOT emit the entire active set as a single burst. MTU-packed frames evenly spaced over the cycle is the canonical implementation.

3. **Emit `ManifestSummary` periodically on the reference-data port.** The maximum interval between `ManifestSummary` messages is the **manifest cadence**. Recommended cadence: **1 second**. The manifest cadence MUST be shorter than the definition cycle period so that a new subscriber sees a `ManifestSummary` before it has finished collecting definitions.

4. **Set the `Valid` flag to reflect channel state.** A publisher MUST set `Valid = 1` on every `ManifestSummary` once its instrument set is established, and `Valid = 0` when the channel is uninitialized or the publisher is shutting down.

5. **Bump `Manifest Seq` atomically when the active set changes.** When an instrument is added to or removed from the active set, the publisher MUST:
   - (a) Increment `Manifest Seq` by 1.
   - (b) Tag all subsequent `InstrumentDefinition` retransmissions with the new `Manifest Seq` value.
   - (c) Emit a `ManifestSummary` with `Valid = 1` carrying the new `Manifest Seq` and the new `Instrument Count` no later than the next manifest cadence interval.

6. **Restart the definition cycle on `Manifest Seq` change.** When `Manifest Seq` bumps, the publisher SHOULD begin a fresh cycle of definition retransmissions tagged with the new seq, so that subscribers can collect a complete set under the new seq within one cycle period.

7. **Reset via the frame header.** To reset the channel, the publisher increments `Reset Count` in the frame header and resets `Sequence Number` to 0. The publisher's `Manifest Seq` MAY restart from any value. Subscribers detect the reset by comparing `Reset Count` against their last-seen value and discard all cached state (see below). The reset takes effect at the beginning of the frame: all application messages in a frame carrying a new `Reset Count` belong to the post-reset epoch. A publisher that needs to reset MUST discard any partially constructed frame and start a new frame with the incremented `Reset Count`.

---

## Subscriber Algorithm

A subscriber adopting this mechanism maintains the following state per channel:

```
state = {
  valid: false,            // bool
  latest_seq: 0,           // u16
  expected_count: 0,       // u32
  last_reset_count: 0,     // u8
  defs: {}                 // map: Instrument ID → InstrumentDefinition
}
```

State transitions:

```
on frame_header(reset_count):
    if reset_count != state.last_reset_count:
        state = { valid: false, latest_seq: 0, expected_count: 0,
                  last_reset_count: reset_count, defs: {} }

on ManifestSummary(valid, seq, count):
    if not valid:
        state.valid          = false
        state.latest_seq     = 0
        state.expected_count = 0
        state.defs           = {}
        return
    if not state.valid or seq is later than state.latest_seq:
        state.valid          = true
        state.latest_seq     = seq
        state.expected_count = count
        state.defs           = {}    // discard definitions tagged with the old seq

on InstrumentDefinition(def):
    if state.valid and def.manifest_seq == state.latest_seq:
        state.defs[def.instrument_id] = def
    // definitions tagged with any other seq are discarded

ready() = state.valid
       and len(state.defs) == state.expected_count
```

A subscriber that receives a market data path message (e.g., `Quote`, `Midpoint`) for an Instrument ID before `ready()` returns true SHOULD either buffer the message until it has the corresponding `InstrumentDefinition`, or drop it, according to its application policy. A subscriber that receives a market data path message for an Instrument ID *not* present in `state.defs` after `ready()` returns true SHOULD drop the message and wait for the next `ManifestSummary`, which may indicate a set change.

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

Worked example for the Top-of-Book & Trades Feed at the recommended settings (N=1000, D=80, T=30, M=1):

```
1000 × 80 / 30 + 24 / 1 = 2,667 + 24 ≈ 2,691 bytes/sec ≈ 22 kbps
```

Definitions pack into frames at 15 per frame (1,200 + 24-byte frame header = 1,224 bytes), giving `1000 / 15 ≈ 67` frames per cycle, or roughly one frame every 448 ms — comfortably within any modern network's burst tolerance.

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

The subscriber sees a `ManifestSummary` within `M` seconds (worst case), then waits up to `T` seconds for a full pass of `InstrumentDefinition` retransmissions. Subscribers requiring faster cold-start can negotiate a shorter cycle period with the publisher operator; the spec sets the recommended values, not hard caps.

---

## Forward Compatibility

This supplement is versioned independently of the feed specs that adopt it. A subscriber and publisher operating under the same version of this supplement interoperate correctly regardless of which feed specs they implement.

Future versions of this supplement MAY:

- Widen `Manifest Seq` to `u32` (with a corresponding feed-spec schema bump).
- Add optional fields to `ManifestSummary` (append-only; old decoders ignore trailing bytes).
- Define new reference-data message types in currently-reserved type ID ranges.
- Add an optional `manifest_hash` field to `ManifestSummary` for defense against publisher bugs in single- or multi-publisher channels.

The two-port transport model and the subscriber algorithm are stable for the v0.1.x line.
