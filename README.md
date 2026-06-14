# edge-feed-spec

Specifications for multicast data feeds published over the [DoubleZero](https://doublezero.xyz) Edge service.

This repository is the home of wire-format specifications that data publishers and subscribers can implement against. Each spec lives in its own directory and is versioned independently.

## Specifications

| Spec | Description |
|------|-------------|
| [Top-of-Book & Trades Feed](./top-of-book/spec.md) | Compact, fixed-size, multicast-native binary protocol for L1 quotes and trades from any two-sided market |
| [Midpoint Feed](./midpoint/spec.md) | Sibling protocol carrying a single derived mid price per instrument, computed from a venue's order book |
| [Market-by-Order Feed](./market-by-order/spec.md) | Sibling protocol carrying the full resting-order population per instrument, with continuous in-band snapshot+delta recovery |
| [Order-Intent Feed](./order-intent/spec.md) | Normalized, pre-consensus order-intent events (order/cancel/modify submissions) observed in a venue's mempool, as fixed-size binary multicast |
| [Reference Data Distribution](./reference-data/spec.md) | Shared supplement defining the two-port transport model and continuous in-band instrument definition retransmission used by the feed specs above |
| [Source ID Registry](./sources/spec.md) | Canonical registry of `Source ID` values identifying the venues whose books feed messages are derived from |

## Status

These specifications are drafts circulated for feedback from prospective publishers and subscribers. Field layouts and semantics may change between draft versions until a `v1.0.0` is declared stable.

## License

Licensed under the **Apache License 2.0**.

See [LICENSE](./LICENSE) for details.
