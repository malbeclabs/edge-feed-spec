# Source ID Registry

This supplement is the canonical registry of `Source ID` values carried in the `u16` Source ID field of DoubleZero Edge feed messages (see the [Top-of-Book & Trades Feed](../top-of-book/spec.md) and [Midpoint Feed](../midpoint/spec.md) specs).

A Source ID identifies the venue whose order book a price message was derived from. Every price message on every feed carries exactly one Source ID. IDs assigned here are stable: once allocated, an ID MUST NOT be reused for a different venue.

## Reserved Ranges

| Range | Purpose |
|-------|---------|
| `0` | Reserved. MUST NOT be used on the wire. |
| `1` – `1023` | Production venues assigned in this registry. |
| `1024` – `32767` | Reserved for future assignment. |
| `32768` – `65535` | Private / experimental. Publishers MAY use these for internal testing; subscribers MUST NOT assume any meaning. |

## Assigned Sources

| ID | Name | Kind | Notes |
|----|------|------|-------|
| `1` | Hyperliquid | Perpetual DEX | |
| `2` | Phoenix | Perpetual DEX | |
| `3` | Lashay | | |

## Adding a New Source

To request a new Source ID, open a pull request against this file that:

1. Adds a row to the **Assigned Sources** table with the next unused ID in the production range.
2. Fills in the `Name`, `Kind`, and (optionally) `Notes` columns.
3. Does not renumber, reorder, or remove existing rows.
