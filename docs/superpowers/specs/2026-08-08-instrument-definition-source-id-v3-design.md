# InstrumentDefinition Source ID and Feed v3 Design

## Goal

Add source attribution to the shared non-midpoint `InstrumentDefinition`
message and publish the resulting breaking wire change as version 3.0.0 of
each affected feed.

## Scope

The change applies to the five feeds that currently share the 128-byte
`InstrumentDefinition` layout:

- Top-of-Book & Trades
- Market-by-Order
- Market-by-Price
- Order-Intent
- Perp Stats

Each affected feed moves from version 2.0.0 to 3.0.0, and its frame `Schema
Version` changes from `2` to `3`.

The Midpoint feed is explicitly excluded. It retains its independent 64-byte
`InstrumentDefinition`, version 1.0.0, and `Schema Version = 1`. The Reference
Data Distribution and Source ID Registry supplements also retain their
independent versions because this change does not alter either supplement's
wire contract or registry assignments.

## Wire Layout

The shared non-midpoint `InstrumentDefinition` grows from 128 to 130 bytes.
Its identity prefix becomes:

| Offset | Field | Type |
|--------|-------|------|
| 0 | Header | 4B |
| 4 | Instrument ID | `u32` |
| 8 | Source ID | `u16` |
| 10 | Symbol | `char[64]` |

`Source ID` identifies the instrument's originating venue using the canonical
Source ID Registry. Placing it immediately after `Instrument ID` matches the
ordering used by the other source-attributed message types.

Every field beginning with `Symbol` moves two bytes later. In particular,
`Price Bound` moves from offset 125 to 127 and `Manifest Seq` moves from offset
126 to 128. Existing field widths and semantics otherwise remain unchanged.
The message header declares `Length=130`.

Because the message length and existing field offsets change, the update is a
breaking change under `VERSIONING.md` and requires the major-version and
on-wire schema-version bump.

## Specification Updates

Each affected feed specification will:

- declare version 3.0.0 and require `Schema Version = 3`;
- list `InstrumentDefinition` as 130 bytes in its type registry;
- reproduce or reference the new shared layout consistently;
- update any byte-layout diagram containing the definition;
- change forward-compatibility text from the `2.x` line to the `3.x` line;
- add a 3.0.0 changelog entry describing the source field, shifted offsets,
  new message length, and required schema bump.

Repository-level documentation will show the five feeds at 3.0.0/schema 3 and
continue to call out Midpoint as the unchanged exception.

## Conformance Tool Updates

The conformance tool will treat schema version 3 as current for every
supported non-midpoint feed and schema version 1 as current for Midpoint. Its
fixed-size validation will require a 130-byte non-midpoint
`InstrumentDefinition`.

Reference-data decoding will keep reading `Instrument ID` from offset 4, read
the shifted `Price Bound` at offset 127 where relevant, and read `Manifest Seq`
at offset 128. Non-midpoint packet fixtures and message builders will insert a
registered nonzero `Source ID` after `Instrument ID` and use the new length.
Midpoint fixtures and decoding remain unchanged.

This change does not add a new conformance rule for Source ID registry
membership. The requested contract change is layout and versioning; registry
validation can be added separately if desired without conflating it with the
v3 migration.

## Testing

Tests will establish the new behavior before implementation by asserting:

- non-midpoint frames require schema version 3 while Midpoint still requires
  version 1;
- non-midpoint `InstrumentDefinition` messages require length 130 while
  Midpoint still requires length 64;
- reference-data state reads the shifted `Manifest Seq` and `Price Bound`
  fields from v3 definitions;
- representative generated captures remain conformant after the new field is
  inserted.

After the focused tests pass, the complete conformance-tool test suite will be
run to catch stale fixtures, offsets, and version expectations.
