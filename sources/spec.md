# Source ID Registry

This supplement is the canonical registry of `Source ID` values carried in the `u16` Source ID field of DoubleZero Edge feed messages (see the [Top-of-Book & Trades Feed](../top-of-book/spec.md) and [Midpoint Feed](../midpoint/spec.md) specs).

A Source ID identifies the venue whose order book a price message was derived from. Every price message on every feed carries exactly one Source ID. IDs assigned here are stable: once allocated, an ID MUST NOT be reused for a different venue.

This document specifies version **1.0.0**: the reserved ranges, the current assignment set, and the process for requesting a new ID.

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

## Versioning

This registry is a supplement with no wire format of its own. It carries no `Magic` and no `Schema Version`; the `u16` Source ID field it governs is defined by each feed spec. Its version tracks the assignment set, independently of every feed spec. See the [Versioning Policy](../VERSIONING.md) for the full scheme.

Assigning a new Source ID is an additive change and is therefore a **MINOR** release. Editorial changes to the `Name`, `Kind`, or `Notes` of an existing row, and changes to this document's prose, are **PATCH** releases.

Because assigned IDs are stable and MUST NOT be renumbered, reordered, removed, or reused, this registry has no mechanism by which a MAJOR release could arise. A subscriber pinned to any `1.x` version of this registry will find that every ID it knows still means what it meant; a later version only adds IDs it has not seen. Subscribers MUST treat an unrecognized Source ID as an unknown venue rather than an error, exactly as if they were running against an older copy of this registry.

### Changes

**1.0.0** — first stable release. Covers Source IDs `1` (Hyperliquid), `2` (Phoenix), and `3` (Lashay).
