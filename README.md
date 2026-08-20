# edge-feed-spec

Specifications for multicast data feeds published over the [DoubleZero](https://doublezero.xyz) Edge service.

This repository is the home of wire-format specifications that data publishers and subscribers can implement against. Each spec lives in its own directory and is versioned independently. See the [Versioning Policy](./VERSIONING.md) for how spec versions, the on-wire `Schema Version` byte, and release tags relate.

## Specifications

| Spec | Description |
|------|-------------|
| [Top-of-Book & Trades Feed](./top-of-book/spec.md) | Compact, fixed-size, multicast-native binary protocol for L1 quotes and trades from any two-sided market |
| [Midpoint Feed](./midpoint/spec.md) | Sibling protocol carrying a single derived mid price per instrument, computed from a venue's order book |
| [Market-by-Order Feed](./market-by-order/spec.md) | Sibling protocol carrying the full resting-order population per instrument, with continuous in-band snapshot+delta recovery |
| [Market-by-Price Feed](./market-by-price/spec.md) | Sibling protocol carrying full-depth price-aggregated (L2) book levels per instrument, with continuous in-band snapshot+delta recovery |
| [Order-Intent Feed](./order-intent/spec.md) | Normalized, pre-consensus order-intent events (order/cancel/modify submissions) observed in a venue's mempool, as fixed-size binary multicast |
| [Reference Data Distribution](./reference-data/spec.md) | Shared supplement defining the two-port transport model and continuous in-band instrument definition retransmission used by the feed specs above |
| [Perp Stats Feed](./perp-stats/spec.md) | Sibling cadence feed carrying per-perpetual derived state (funding, mark, oracle, open interest, premium) relayed from the venue REST surface |
| [Source ID Registry](./sources/spec.md) | Canonical registry of `Source ID` values identifying the matching engine whose activity a feed message describes |

## Status

All specifications are stable to build against. Every feed's frame header carries a `Schema Version` byte equal to its spec's MAJOR version, so the byte on the wire tells a decoder which layout it is holding. Within a major line, changes are additive only: new message types, new enumerated values, and appended fields, all of which a conformant decoder already skips or ignores. Field layouts and semantics will not change without a MAJOR release and a `Schema Version` bump, which decoders MUST reject rather than parse.

The five feeds carrying the 130-byte `InstrumentDefinition` are at **3.0.0** (`Schema Version = 3`). The Midpoint Feed keeps its slimmed 64-byte variant and remains at **1.0.0** (`Schema Version = 1`).

See [VERSIONING.md](./VERSIONING.md) for the change classification, the compatibility promise, and the `<spec>/vMAJOR.MINOR.PATCH` tag scheme.

## License

Licensed under the **Apache License 2.0**.

See [LICENSE](./LICENSE) for details.
