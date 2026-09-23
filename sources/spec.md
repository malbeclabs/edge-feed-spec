# Source ID Registry

This supplement is the canonical registry of `Source ID` values carried in the `u16` Source ID field of DoubleZero Edge feed messages.

A Source ID identifies the **matching engine** whose activity the message describes, as [GLOSSARY.md](../GLOSSARY.md) defines it. Every price message and every event message carries exactly one Source ID, as does every `InstrumentDefinition` from Schema Version 3 onward. The [midpoint feed](../midpoint/spec.md)'s 64-byte `InstrumentDefinition` variant predates the field and does not carry it; that feed is still at Schema Version 1. IDs assigned here are stable: once allocated, an ID MUST NOT be reused for a different matching engine.

**A Source ID names a matching engine, not a venue.** One venue may run several matching engines and therefore hold several Source IDs. Two engines are distinct where their order-matching rules differ, and equally where they match independently over disjoint instrument sets under identical rules; a capacity shard the venue can rebalance is a channel rather than an engine. [GLOSSARY.md](../GLOSSARY.md) carries the full test and is the authority for it. A new engine at an already-registered venue is a new ID rather than a reuse of that venue's existing one. Because IDs are never renumbered, this only ever adds, and an ID already assigned keeps meaning whichever engine has been publishing under it. Venues split their own engines on their own schedule, so treat the set as open rather than assuming the current table is final.

This document specifies version **1.5.0**: the reserved ranges, the current assignment set, and the process for requesting a new ID.

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
| `1` | Hyperliquid | `HYPERLIQUID` | Hyperliquid | Perpetual DEX | Currently stamped on the venue's native perps and on its HIP-3 builder-deployed DEXes alike. Each builder DEX is a separate matching engine and takes its own ID; this row narrows to the native perps once the ID `1` publishers filter, and records the current state until then. See `7`. |
| `2` | Phoenix | `PHOENIX` | Phoenix | Perpetual DEX | |
| `3` | Kalshi | `KALSHI` | Kalshi | Perpetual Futures, Prediction Market | Registered under the codename `Lashay` until the venue launched. Carries two matching engines and is due to split; see below. |
| `4` | Setai Financials | `SETAI_FINANCIALS` | Setai | Futures | Interface versioned separately from `5`. |
| `5` | Setai Commodities | `SETAI_COMMODITIES` | Setai | Futures, Options on Futures | Interface versioned separately from `4`. |
| `6` | Binance USD-Margined Futures | `BINANCE` | Binance | Perpetual Futures, Dated Futures | The venue's own `futuresType` for this engine is `U_MARGINED`. Spot is a separate matching engine at this venue and holds ID `8`. Coin-margined futures and options are separate matching engines too, unclaimed, and will share this `Code`. |
| `7` | XYZ | `HYPERLIQUID` | Hyperliquid | Perpetual DEX | HIP-3 builder-deployed DEX. Matches the `xyz:`-prefixed perps on real-world assets, a set disjoint from ID `1`'s native perps. Shares ID `1`'s `Code`. The other builder DEXes at this venue are separate matching engines, unclaimed; see below. |
| `8` | Binance Spot | `BINANCE` | Binance | Spot | The spot matching engine at the venue that holds ID `6`. Different order-matching rules. Shares ID `6`'s `Code`. No publisher stamps this ID today; see below. |

`Code` is the machine-readable short name **for the venue**. It matches the `Venue` column and takes the same name TradingView uses. Uppercase, never an abbreviation, each space and hyphen a single underscore.

**Several matching engines at one venue share a `Code`.** It names the venue, not the engine, so `Code` alone cannot key an engine: `Source ID` is the engine key, and anything carrying `Code` as a stable identifier — a metric label value, a composed product identifier — needs `source_id` beside it to name an engine. `Name` is where a human reader tells two engines apart.

IDs `4` and `5` predate this rule and still carry engine-level Codes, `SETAI_FINANCIALS` and `SETAI_COMMODITIES`. They are left alone here: a `Code` already in use downstream cannot be changed without coordinating with whoever carries it.

Downstream systems already carry these values, so this column documents what exists rather than introducing anything new. This registry is the authority for it; any table of these values held elsewhere is a [registry mirror](../GLOSSARY.md) and is derived, never authoritative.

`Venue` is the external exchange or market operator running the engine, as [GLOSSARY.md](../GLOSSARY.md) defines it. It is recorded separately from `Name` so that several engines at one venue are legible as such, which is the case the `Name` column alone cannot show.

Name the venue as TradingView does, where TradingView lists it. Its [data coverage list](https://www.tradingview.com/data-coverage/) is the reference. That is one fewer name for a subscriber to map. This governs `Venue` and `Code`, which carry the same name. TradingView has one name per venue and distinguishes products in the symbol, so it cannot name a matching engine, and neither column tries to: `Source ID` and `Name` do that.

Note that `Venue` is deliberately not `Operator`. The glossary reserves **Operator** for the person or organization running a *publisher*, which is a different question from which exchange runs the engine, and one this registry does not currently record.

`Name` on IDs `1` through `3` predates the rule that an ID names an engine, and each is currently the venue's name because each venue published one engine when its ID was assigned. Those names stand, because assigned rows are stable. A new row names the **engine**, not the venue, and where a venue runs more than one engine each engine takes its own row and its own `Name`, sharing the venue's uppercase `Code`.

### ID `3` carries two engines

Source ID `3` is currently stamped on four Kalshi feeds: crypto perpetuals on top-of-book and market-by-price, and sports event markets on top-of-book and market-by-price. Perpetual futures settled by funding and binary or scalar event contracts are different sets of order-matching rules, so by the rule above they are two matching engines and want two IDs.

Splitting them assigns a new ID to one engine and changes what that engine's publisher stamps, so it is a deployment change rather than a registry edit, and this row records the current state rather than pre-empting it. The registry moves first when it happens, per the codename and re-registration sequencing in [GLOSSARY.md](../GLOSSARY.md). Until then a subscriber MUST NOT infer which of the two engines a message came from out of `Source ID` alone; distinguishing them is a deployment concern, out of scope here, exactly as group and port assignment is.

### ID `6` is one of a venue's four matching engines

Binance runs at least four: spot, USD-margined futures, coin-margined futures, options. Separate stacks, different order-matching rules, separate rate limits, and for spot a different transport generation, since spot offers a binary SBE market data interface while the USD-margined stack is JSON only. Four engines, four Source IDs, one shared `Code`. This claims the USD-margined engine. ID `8` claims spot. Coin-margined futures and options are unclaimed rather than overlooked.

The `Name` is not "Perpetual". This engine also matches dated quarterly and weekly futures, and a Source ID names the matching engine rather than the subset a publisher chooses to carry. "USD-Margined" expands the venue's own machine-readable `futuresType` of `U_MARGINED`, and names the coin-margined engine for free.

The operator of record is Nest Exchange Limited, licensed in the Abu Dhabi Global Market as a Recognised Investment Exchange for derivatives since 5 January 2026. `Venue` carries `Binance` because that is the name the market uses.

### IDs `4` and `5` are one venue's two platforms

These two engines report the **same** order-matching algorithm, so differing rules — the first half of the rule above — does not separate them. They are distinct under the second half: they match independently of each other over disjoint sets of instruments, and the venue treats that boundary as durable rather than as a shard it can rebalance, versioning each platform's interface separately and running each on its own trading calendar.

Recorded because it is the first assignment resting on the second half of the rule rather than the first. [GLOSSARY.md](../GLOSSARY.md) `1.3.0` widened the definition to cover it, and this assignment is the case that prompted the widening.

### ID `7` is a second engine at an already-registered venue

Hyperliquid matches its own native perps on the engine that holds ID `1`. It also hosts HIP-3 builder-deployed DEXes. A builder deploys a perp DEX on the venue, and lists its own instruments under its own name prefix. `XYZ` is one such DEX.

The order-matching rules are the venue's in both cases, so the first half of the rule above does not separate the two. They are distinct under the second half: they match independently over disjoint instrument sets. On 17 September 2026 the venue API listed 234 bare-symbol native perps and 122 `xyz:`-prefixed perps on real-world assets: equities, metals, currencies, indices and commodities. No instrument appeared in both sets.

The boundary is durable rather than a capacity shard the venue can rebalance. The builder holds its own deployer keys for this engine, and controls asset registration, the oracle updater, trading halts, margin tables and funding multipliers for its own instrument set alone. The venue publishes each builder DEX with its own such controls, so an instrument does not cross between the two engines. Several of those controls are microstructure parameters, which [GLOSSARY.md](../GLOSSARY.md) places under the first half of the rule; they are recorded here as evidence that the instrument-set boundary is durable, not as a claim that the two engines match by different rules. The assignment rests on the second half alone.

This is the same basis as IDs `4` and `5`, which this document records as the first assignment resting on the second half of the rule. [GLOSSARY.md](../GLOSSARY.md) `1.3.0` already counts each HIP-3 builder DEX as an engine of its own, so the definition needs no change; its worked example named a fixed total that this assignment outgrows, corrected in glossary `1.3.1`.

**The set of engines at this venue is open.** On 17 September 2026 the venue API listed ten builder DEXes. This row claims one of them. The other nine are separate matching engines, as are HIP-4 prediction markets, and each is unclaimed rather than overlooked. No ID is reserved for them here: each takes the next unused ID when a publisher is ready, exactly as this one did. The count grows whenever a builder deploys, which is the open set the Source ID definition above describes.

Until the ID `1` publishers filter, the two IDs overlap rather than partition: an `xyz:` instrument is on the wire under ID `1` and under ID `7` at the same time. For as long as that holds, a subscriber MUST NOT treat ID `1` as native-only, and MUST expect the `xyz:` instruments under either ID. This is the counterpart to ID `3`'s rule above, and it lifts when those publishers filter, not when this row merges.

`Name` is `XYZ`, which is the builder name the venue itself publishes. It is not a codename, so it needs no later retirement.

### ID `8` is the spot engine at the venue that holds ID `6`

This is the second of that venue's four matching engines to be claimed. The ID `6` section lists all four. This ID is distinct from ID `6` under the **first** half of the engine rule: the order-matching rules differ. The spot engine matches an exchange of two assets, and settles it at the match. The USD-margined engine matches margined contracts, with funding payments and liquidation. The two run on separate stacks under separate rate limits, and spot offers a binary SBE market data interface where the USD-margined stack is JSON only.

The assignment does **not** rest on the second half of the rule. The engines can expose the same symbol strings: `BTCUSDT` names a spot pair on one engine and a perpetual contract on the other. These are different instruments even though they use the same symbol, so a symbol cannot establish whether the instrument sets are disjoint; `Source ID` remains the engine key.

`Name` is `Binance Spot`. It says spot and nothing narrower, because the engine matches every spot pair whatever the quote asset. On 18 September 2026 the venue listed 1,368 pairs in the `TRADING` state, against 566 instruments on the engine that holds ID `6`. `Code` is `BINANCE`, which ID `6` already carries, under the rule that a `Code` names the venue.

No publisher stamps this ID today. The ID is assigned here so that the number is fixed before a publisher needs it; when a publisher starts, which instruments it carries is a deployment concern and out of scope here, exactly as group and port assignment is.

## Adding a New Source ID

To request a new Source ID, open a pull request against this file that:

1. Adds a row to the **Assigned Source IDs** table with the next unused ID in the production range.
2. Names the matching engine in `Name`, gives the venue's uppercase `Code`, names the exchange running it in `Venue`, and fills in `Kind` and (optionally) `Notes`. Where the venue already holds rows, the `Code` is the one those rows carry, where they agree; IDs `4` and `5` predate this rule and keep their engine-level Codes.
3. States what makes the engine distinct from any already-registered engine at the same venue, where there is one.
4. Does not renumber, reorder, or remove existing rows.

## Versioning

This registry is a supplement with no wire format of its own. It carries no `Magic` and no `Schema Version`; the `u16` Source ID field it governs is defined by each feed spec. Its version tracks the assignment set, independently of every feed spec. See the [Versioning Policy](../VERSIONING.md) for the full scheme.

Assigning a new Source ID is an additive change and is therefore a **MINOR** release, as is adding a column to the assignment table. Editorial changes to the `Name`, `Code`, `Venue`, `Kind`, or `Notes` of an existing row, and changes to this document's prose, are **PATCH** releases.

Because assigned IDs are stable and MUST NOT be renumbered, reordered, removed, or reused, this registry has no mechanism by which a MAJOR release could arise. A subscriber pinned to any `1.x` version of this registry will find that every ID it knows still names the same matching engine; the set of instruments a publisher stamps with it is a deployment concern and may be corrected. A later version only adds IDs it has not seen. Subscribers MUST treat an unrecognized Source ID as an unknown matching engine rather than an error, exactly as if they were running against an older copy of this registry.

### Changes

**1.5.0** — assigned Source ID `8` to the spot matching engine at Binance. It is the second engine registered at that venue, so it shares ID `6`'s `Code` under the `1.3.0` rule. Distinct from ID `6` under the first half of the engine rule, which is the rules-differ test: spot matches an exchange of two assets settled at the match, and the USD-margined engine matches margined contracts with funding and liquidation. Stated that the assignment does not rest on the second half, because the two engines share symbol strings and their instrument sets are therefore not disjoint as strings. Updated ID `6`'s row and its section, which recorded spot as unclaimed. No publisher stamps ID `8` today.

**1.4.0** — assigned Source ID `7` to the `XYZ` HIP-3 builder-deployed matching engine at Hyperliquid. It is the second engine registered at that venue, so it shares ID `1`'s `Code` under the `1.3.0` rule. ID `1` is not redefined here: its publishers stamp it on the builder DEXes today, so its row records that current scope and notes that it narrows to the native perps once those publishers filter. That narrowing is a deployment change and a later release, per the sequencing ID `3` follows. Distinct from ID `1` under the second half of the engine rule: the same order-matching rules over a disjoint instrument set, which is the basis IDs `4` and `5` rest on. Recorded that this venue lists ten builder DEXes today, and that the other nine and HIP-4 prediction markets are unclaimed. Repaired two sentences that `1.3.0` left behind: a new engine at a known venue shares that venue's `Code` rather than taking its own, which this row is the first to exercise. Separated what an ID means from what a publisher stamps with it in the compatibility paragraph, which promised only the former but read as promising both. Stated that ID `1` and ID `7` overlap rather than partition until those publishers filter, and what a subscriber MUST do meanwhile, which is the counterpart to ID `3`'s rule. Brought the [VERSIONING.md](../VERSIONING.md) row for this registry current: it still read `1.2.0`, because neither `1.2.1` nor `1.3.0` updated it.

**1.3.0** — assigned Source ID `6` to the USD-margined futures matching engine at Binance, and made `Code` name the venue rather than the matching engine. Several engines at one venue now share a `Code`, so `Code` alone no longer keys an engine and `Source ID` is stated as the engine key. This supersedes `1.2.1`'s statement that `Code` names the matching engine and stays unique. IDs `4` and `5` keep their engine-level Codes, because a `Code` already carried downstream cannot be changed without coordination.

**1.2.1** — editorial. Name the venue as TradingView does. `Code` is unaffected, because TradingView cannot name a matching engine.

**1.2.0** — assigned Source IDs `4` (Setai Financials) and `5` (Setai Commodities), and conformed the Source ID definition above to [GLOSSARY.md](../GLOSSARY.md) `1.3.0`. Two IDs rather than one because they match independently over disjoint instrument sets against separately versioned interfaces, which is the case the glossary widening covers; they report the **same** order-matching algorithm, so the rules-differ test alone would not have separated them. Both carry `Venue` `Setai`, which is the case that column was added for. Stated the underscore rule for a multi-word `Code`.

**1.1.0** — added the `Code` and `Venue` columns, and retired the `Lashay` codename on ID `3` now that the venue has launched. Restated the Source ID definition to name the matching engine rather than the venue, matching the glossary, and to cover event messages and `InstrumentDefinition` rather than price messages alone. Recorded that ID `3` currently carries two matching engines and is due to split. No assignment changed and no ID was renumbered.

**1.0.1** — editorial. Renamed the *Assigned Sources* and *Adding a New Source* headings to name Source IDs rather than bare sources. No assignment changed.

**1.0.0** — first stable release. Covers Source IDs `1` (Hyperliquid), `2` (Phoenix), and `3` (then under the codename `Lashay`).
