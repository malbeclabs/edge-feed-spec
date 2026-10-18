# Midpoint Feed v3 Design

## Goal

Make the midpoint feed carry a VWAP, and move it to the shared 130-byte
`InstrumentDefinition`.

## Problem

The midpoint feed cannot carry a VWAP:

- Every field and flag assumes an order book. A VWAP is derived from trades.
- The Method table has no VWAP value.
- The `Midpoint` message has no field for the length of the VWAP window.

The midpoint feed also uses its own 64-byte `InstrumentDefinition`. That
layout has no `Source ID`, and the shared codec does not implement it. The
other five feeds use the 130-byte layout at `Schema Version = 3`.

## Version

`1.1.0` to `3.0.0`. `Schema Version` changes from `1` to `3`.

The change is `MAJOR`. It changes a message length, renames fields, and
changes the meaning of Method `0`.

The version skips `2`. With `3`, the byte has the same value as in the other
feeds that use the 130-byte `InstrumentDefinition`. `VERSIONING.md` requires
`Schema Version` to equal the spec `MAJOR`. It does not require consecutive
values.

## Changes

### `InstrumentDefinition`

Use the shared 130-byte layout from the top-of-book feed, unchanged.

| Field | Midpoint rule |
|---|---|
| `Source ID` | The matching engine whose order book or trades the price is derived from. |
| `Qty Exponent` | Exponent of `Volume`. |
| `Lot Size` | No field in this feed uses it. `0`. |
| `Contract Value` | `0`. |
| `Settle Type` | `0`. |
| `Tick Size` | Tick size of the matching engine. `0` if not available. |
| `Price Exponent` | Precision of `Price`. It can be finer than `Tick Size`. |

`Default Method` is removed. The shared layout has no field for it.

### `Midpoint`

The length grows from 40 to 48 bytes.

| Offset | 1.1.0 | 3.0.0 |
|---|---|---|
| 10 | `Method`. `0` = use `Default Method` | `Method`. `0` = Unknown. A publisher MUST NOT send `0`. |
| 12 | `Book Timestamp` | `Input Timestamp`: the venue timestamp of the latest input. For VWAP, the end of the window. |
| 28 | `Mid Price` | `Price` |
| 36 | Reserved, 4 bytes | `Window` (`u32`, milliseconds) |
| 40 | — | `Volume` (`qty`): sum of trade quantity in the window |

### Method

New value `5` = VWAP: `Σ(price × qty) / Σ qty` over the trades in the window.

- For VWAP, `Window` MUST NOT be `0`.
- For all other methods, `Window` and `Volume` MUST be `0`.
- A window with no trades: `Volume` is `0`, the last `Price` carries over, and
  the `synthetic` bit is set.
- The window is `(Input Timestamp − Window, Input Timestamp]`. A trade is in the window if its venue timestamp is in this interval.

Each Method value is now classified as a book method or a trade method.

### Quality Flags

- `one-sided` and `crossed-or-locked` apply only to book methods. For trade
  methods they are `0`.
- `stale` and `synthetic` now refer to "inputs", not "book".

## Not changed

- `Magic` `0x4D44`.
- Message Type IDs.
- The port roles.
- The datagram header.
- The `Heartbeat`, `EndOfSession` and `ManifestSummary` layouts.

## Other documents

| Document | Change |
|---|---|
| `VERSIONING.md` | Version table row. Remove the text about the 64-byte variant. |
| `README.md` | Feed summary and status line. |
| `reference-data/spec.md` | `1.1.1`, editorial. Remove the references to the 64-byte layout. |
| `market-by-order/spec.md` | `3.3.2`, editorial. Type ID policy lists midpoint with the 130-byte layout. |
| `market-by-price/spec.md` | `3.2.1`, editorial. The same change. |
| `sources/spec.md` | `1.6.1`, editorial. Midpoint `InstrumentDefinition` carries `Source ID`. |

## Follow-up

Each item is a separate pull request.

- `tools/conformance`: decode midpoint at `Schema Version = 3`.
  - Replace `MID.METHOD0_REQUIRES_DEFAULT` with a rule that rejects Method `0`.
  - Add rules for `Window` and `Volume`.
  - Keep the `Schema Version = 1` decoder for publishers that are already
    deployed.
- Shared codec: midpoint message types and golden vectors.
- A `Source ID` for a price that is derived from more than one matching
  engine. This needs a change to the Source ID Registry.
