# Source ID Registry

This supplement is the canonical registry of `Source ID` values carried in the `u16` Source ID field of DoubleZero Edge feed messages.

A Source ID identifies the **matching engine** whose activity the message describes, as [GLOSSARY.md](../GLOSSARY.md) defines it. Every price message and every event message carries exactly one Source ID, as does every `InstrumentDefinition` from Schema Version 3 onward. The [midpoint feed](../midpoint/spec.md)'s 64-byte `InstrumentDefinition` variant predates the field and does not carry it; that feed is still at Schema Version 1. IDs assigned here are stable: once allocated, an ID MUST NOT be reused for a different matching engine.

**A Source ID names a matching engine, not a venue.** One venue may run several matching engines and therefore hold several Source IDs. Two engines are distinct where their order-matching rules differ, and equally where they match independently over disjoint instrument sets under identical rules; a capacity shard the venue can rebalance is a channel rather than an engine. [GLOSSARY.md](../GLOSSARY.md) carries the full test and is the authority for it. A new engine at an already-registered venue is a new ID rather than a reuse of that venue's existing one. Because IDs are never renumbered, this only ever adds, and an ID already assigned keeps meaning whichever engine has been publishing under it. Venues split their own engines on their own schedule, so treat the set as open rather than assuming the current table is final.

This document specifies version **1.3.0**: the reserved ranges, the current assignment set, and the process for requesting a new ID.

## Reserved Ranges

| Range | Purpose |
|-------|---------|
| `0` | Reserved. MUST NOT be used on the wire. |
| `1` – `1023` | Production matching engines assigned in this registry. |
| `1024` – `32767` | Reserved for future assignment. |
| `32768` – `65535` | Private / experimental. Publishers MAY use these for internal testing; subscribers MUST NOT assume any meaning. |

## Assigned Source IDs

| ID | Name | Code | Venue | Kind | Notes |
|----|------|------|-------|------|-------|
| `1` | Hyperliquid | `HYPERLIQUID` | Hyperliquid | Perpetual DEX | |
| `2` | Phoenix | `PHOENIX` | Phoenix | Perpetual DEX | |
| `3` | Kalshi | `KALSHI` | Kalshi | Perpetual Futures, Prediction Market | Registered under the codename `Lashay` until the venue launched. Carries two matching engines and is due to split; see below. |
| `4` | Setai Financials | `SETAI_FINANCIALS` | Setai | Futures | Interface versioned separately from `5`. |
| `5` | Setai Commodities | `SETAI_COMMODITIES` | Setai | Futures, Options on Futures | Interface versioned separately from `4`. |
| `6` | Binance USD-Margined Futures | `BINANCE_USD_MARGINED_FUTURES` | Binance | Perpetual Futures, Dated Futures | The venue's own `futuresType` for this engine is `U_MARGINED`. Spot, coin-margined futures and options are separate engines at this venue and are unclaimed; see below. |

`Code` is the machine-readable short name: the uppercase full name, never an abbreviation. Where the name carries more than one word, each space becomes a single underscore, so `Setai Financials` is `SETAI_FINANCIALS`. A hyphen inside a name is a word separator like a space, so `Binance USD-Margined Futures` is `BINANCE_USD_MARGINED_FUTURES`. Downstream systems already carry these values as stable keys for the engine, in metric label values and in composed product identifiers among others, so this column documents what exists rather than introducing anything new. This registry is the authority for it; any table of these values held elsewhere is a [registry mirror](../GLOSSARY.md) and is derived, never authoritative.

`Venue` is the external exchange or market operator running the engine, as [GLOSSARY.md](../GLOSSARY.md) defines it. It is recorded separately from `Name` so that several engines at one venue are legible as such, which is the case the `Name` column alone cannot show.

Note that `Venue` is deliberately not `Operator`. The glossary reserves **Operator** for the person or organization running a *publisher*, which is a different question from which exchange runs the engine, and one this registry does not currently record.

`Name` on IDs `1` through `3` predates the rule that an ID names an engine, and each is currently the venue's name because each venue published one engine when its ID was assigned. Those names stand, because assigned rows are stable. A new row names the **engine**, not the venue, and where a venue runs more than one engine each takes its own row and its own uppercase `Code`.

### ID `3` carries two engines

Source ID `3` is currently stamped on four Kalshi feeds: crypto perpetuals on top-of-book and market-by-price, and sports event markets on top-of-book and market-by-price. Perpetual futures settled by funding and binary or scalar event contracts are different sets of order-matching rules, so by the rule above they are two matching engines and want two IDs.

Splitting them assigns a new ID to one engine and changes what that engine's publisher stamps, so it is a deployment change rather than a registry edit, and this row records the current state rather than pre-empting it. The registry moves first when it happens, per the codename and re-registration sequencing in [GLOSSARY.md](../GLOSSARY.md). Until then a subscriber MUST NOT infer which of the two engines a message came from out of `Source ID` alone; distinguishing them is a deployment concern, out of scope here, exactly as group and port assignment is.

### ID `6` claims one of a venue's four known engines

Binance runs at least four matching engines: spot, USD-margined futures, coin-margined futures, and options. They are separate stacks with different order-matching rules, separate rate limits, and in the case of spot a different transport generation — spot offers a binary SBE market data interface while the USD-margined stack is JSON only. By the rule above they are four engines and take four IDs.

This row claims **only the USD-margined futures engine.** The other three are deliberately unclaimed rather than overlooked, and each takes its own row and its own `Code` when a publisher emits for it. Nothing here should be read as reserving them.

Two things about the name, recorded because both were decided rather than obvious.

**It is not named "Perpetual".** The engine matches perpetual futures, dated quarterly and weekly futures, and perpetuals on non-crypto underlyings. A `Name` of "Binance Perpetual Futures" would describe what a first publisher chooses to carry rather than what the engine matches, and a Source ID names the engine. The distinction becomes load-bearing the day anyone publishes the dated contracts on this ID, which needs no new ID and no registry change.

**"USD-Margined" rather than the venue's "USDⓈ-M" branding.** The `Code` rule above forbids an abbreviation, and `USDS-M` is one. The expanded form is also the semantic reading of the venue's own machine-readable `futuresType`, `U_MARGINED`; it survives the venue rebranding its marketing name; and it generates the sibling name without further thought, since the coin-margined engine would be `Binance Coin-Margined Futures` / `BINANCE_COIN_MARGINED_FUTURES`.

Note for anyone doing diligence against this row: the operator of record for this engine is **Nest Exchange Limited**, licensed in the Abu Dhabi Global Market as a Recognised Investment Exchange for derivatives since 5 January 2026. `Venue` carries `Binance` because that is the name the market uses and the name every other column in this table is written in, not because the two are interchangeable.

### IDs `4` and `5` are one venue's two platforms

These two engines report the **same** order-matching algorithm, so differing rules — the first half of the rule above — does not separate them. They are distinct under the second half: they match independently of each other over disjoint sets of instruments, and the venue treats that boundary as durable rather than as a shard it can rebalance, versioning each platform's interface separately and running each on its own trading calendar.

Recorded because it is the first assignment resting on the second half of the rule rather than the first. [GLOSSARY.md](../GLOSSARY.md) `1.3.0` widened the definition to cover it, and this assignment is the case that prompted the widening.

## Adding a New Source ID

To request a new Source ID, open a pull request against this file that:

1. Adds a row to the **Assigned Source IDs** table with the next unused ID in the production range.
2. Names the matching engine in `Name`, gives its uppercase `Code`, names the exchange running it in `Venue`, and fills in `Kind` and (optionally) `Notes`.
3. States what makes the engine distinct from any already-registered engine at the same venue, where there is one.
4. Does not renumber, reorder, or remove existing rows.

## Versioning

This registry is a supplement with no wire format of its own. It carries no `Magic` and no `Schema Version`; the `u16` Source ID field it governs is defined by each feed spec. Its version tracks the assignment set, independently of every feed spec. See the [Versioning Policy](../VERSIONING.md) for the full scheme.

Assigning a new Source ID is an additive change and is therefore a **MINOR** release, as is adding a column to the assignment table. Editorial changes to the `Name`, `Code`, `Venue`, `Kind`, or `Notes` of an existing row, and changes to this document's prose, are **PATCH** releases.

Because assigned IDs are stable and MUST NOT be renumbered, reordered, removed, or reused, this registry has no mechanism by which a MAJOR release could arise. A subscriber pinned to any `1.x` version of this registry will find that every ID it knows still means what it meant; a later version only adds IDs it has not seen. Subscribers MUST treat an unrecognized Source ID as an unknown matching engine rather than an error, exactly as if they were running against an older copy of this registry.

### Changes

**1.3.0** — assigned Source ID `6` (Binance USD-Margined Futures), and stated that a hyphen inside a `Name` is a word separator for `Code`, which this is the first assignment to need. The row claims one of four known engines at the venue; the other three are recorded as unclaimed rather than left to inference. Named for the margin stack rather than for the perpetual contracts a first publisher carries, because the engine also matches dated futures and a Source ID names the engine.

**1.2.0** — assigned Source IDs `4` (Setai Financials) and `5` (Setai Commodities), and conformed the Source ID definition above to [GLOSSARY.md](../GLOSSARY.md) `1.3.0`. Two IDs rather than one because they match independently over disjoint instrument sets against separately versioned interfaces, which is the case the glossary widening covers; they report the **same** order-matching algorithm, so the rules-differ test alone would not have separated them. Both carry `Venue` `Setai`, which is the case that column was added for. Stated the underscore rule for a multi-word `Code`.

**1.1.0** — added the `Code` and `Venue` columns, and retired the `Lashay` codename on ID `3` now that the venue has launched. Restated the Source ID definition to name the matching engine rather than the venue, matching the glossary, and to cover event messages and `InstrumentDefinition` rather than price messages alone. Recorded that ID `3` currently carries two matching engines and is due to split. No assignment changed and no ID was renumbered.

**1.0.1** — editorial. Renamed the *Assigned Sources* and *Adding a New Source* headings to name Source IDs rather than bare sources. No assignment changed.

**1.0.0** — first stable release. Covers Source IDs `1` (Hyperliquid), `2` (Phoenix), and `3` (then under the codename `Lashay`).
