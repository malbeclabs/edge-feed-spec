# Versioning Policy

This document defines how the specifications in this repository are versioned, how a document version relates to the `Schema Version` byte carried on the wire, and how releases are tagged.

It is the canonical statement of the policy. Each spec's own *Versioning and Forward Compatibility* section states that spec's current version and links here for the rule.

---

## Independent versioning

Every spec in this repository is versioned independently. The Top-of-Book feed being at `1.2.0` says nothing about the Midpoint feed's version. Specs are siblings, not layers, and a publisher may implement any subset.

Each spec declares its version in its opening paragraph:

> This document specifies version **1.0.0**: ...

Versions are [Semantic Versioning](https://semver.org/) `MAJOR.MINOR.PATCH`.

---

## The wire binding

Every feed's 24-byte frame header carries a single `u8` `Schema Version` at offset 2. That byte is bound to the document version by one rule:

> **`Schema Version` byte == spec `MAJOR`**

There is no lookup table and no negotiation. A decoder that reads `Schema Version = 1` knows it is looking at a frame produced under some `1.x.y` release of that feed's spec, and that every `1.x.y` release is wire-compatible with every other.

The header is fully packed (24 bytes, no reserved space), so the wire carries `MAJOR` and nothing else. `MINOR` and `PATCH` are documentation-level facts that are deliberately **not** observable on the wire, because by construction they cannot affect a decoder that follows the forward-compatibility rules.

The current state of every feed:

| Spec | Frame `Magic` | `Schema Version` | Version |
|------|---------------|------------------|---------|
| [Top-of-Book & Trades](./top-of-book/spec.md) | `0x445A` | `3` | 3.0.1 |
| [Midpoint](./midpoint/spec.md) | `0x4D44` | `1` | 1.0.1 |
| [Market-by-Order](./market-by-order/spec.md) | `0x4444` | `3` | 3.2.0 |
| [Market-by-Price](./market-by-price/spec.md) | `0x4442` | `3` | 3.1.1 |
| [Order-Intent](./order-intent/spec.md) | `0x494F` | `3` | 3.0.1 |
| [Perp Stats](./perp-stats/spec.md) | `0x4450` | `3` | 3.0.1 |
| [Reference Data Distribution](./reference-data/spec.md) | *(host feed's)* | *(host feed's)* | 1.0.2 |
| [Source ID Registry](./sources/spec.md) | *(none)* | *(none)* | 1.2.0 |
| [Glossary](./GLOSSARY.md) | *(none)* | *(none)* | 1.3.0 |

Midpoint sits at `1` while its siblings are at `3` because it was deliberately left on its 64-byte `InstrumentDefinition` variant when the shared layout changed at both `2.0.0` and `3.0.0`. This is the scheme working as intended: the specs are siblings, not a single versioned family, and a decoder reads each feed's byte to know which layout it is holding.

`Magic` and `Schema Version` do different jobs and both are mandatory checks. `Magic` answers "is this the feed I subscribed to?" and rejects a misrouted sibling feed. `Schema Version` answers "is this a wire format I implement?" and rejects a future incompatible generation of the correct feed. Every feed spec MUST use a `Magic` value distinct from every other feed's, and feeds SHOULD use distinct multicast groups.

---

## What each level means

| Class | Example | Level | `Schema Version` | Effect on an existing decoder |
|-------|---------|-------|------------------|-------------------------------|
| **Editorial** | Reword a paragraph, fix a typo, add a diagram or rationale, clarify an ambiguity without changing the required behavior | `PATCH` | unchanged | None. |
| **Additive** | Define a new message type in a reserved Type ID range; define a new value for an enumerated field; append a field within a message's declared `Message Length`; assign a new Source ID | `MINOR` | unchanged | Keeps working. It skips the unknown type by `Message Length`, maps the unknown enum value to the field's unknown member, and ignores trailing bytes. |
| **Breaking** | Move or resize an existing field; change a message's length; redefine the meaning of a field or flag bit; remove a message type; change a required behavior | `MAJOR` | **incremented** | MUST reject the frame. |

The additive row is only safe because every spec already requires decoders to skip unknown Type IDs by `Message Length`, accept any `u8` in an enumerated field and treat unrecognized values as that field's unknown member, ignore unrecognized flag bits, and ignore trailing bytes within a declared `Message Length`. A decoder that does not do those things is not conformant and gets no compatibility promise.

Two changes already made under this rule, both `MINOR`, both with the byte left at `1`: `0x08 Liquidation` was added as a shared trade-companion message type, and Asset Class value `5` (Perpetual Future) was added.

Two `MAJOR` changes have been made to the shared non-midpoint `InstrumentDefinition`. Version `2.0.0` widened `Symbol` from `char[16]` to `char[64]`, moving every later field and growing the message from 80 to 128 bytes. Version `3.0.0` inserted `Source ID` (`u16`) after `Instrument ID`, shifting every later field by two bytes and growing the message to 130 bytes. Five feeds now use `Schema Version = 3`; midpoint kept its 64-byte variant and stayed at `1.0.0`.

---

## Compatibility promise

Within a `MAJOR` line:

- **Backward compatible.** A subscriber built against `1.0.0` correctly decodes frames from a publisher running any `1.x.y`. It will not see messages or values introduced after `1.0.0`, and skips them by the rules above.
- **Forward compatible.** A subscriber built against `1.4.0` correctly decodes frames from a publisher running any earlier `1.x.y`. It simply never observes the newer messages.

Across a `MAJOR` boundary there is no promise at all. A subscriber MUST validate `Schema Version` and discard frames carrying a version it does not implement, rather than attempting a best-effort parse. A `Schema Version` bump is the explicit signal that field offsets can no longer be trusted.

Publishers MUST NOT emit a `Schema Version` other than the one their frames actually conform to.

---

## Tags and releases

Releases are tagged per spec:

```
<directory>/vMAJOR.MINOR.PATCH
```

For example `top-of-book/v1.0.0`, `market-by-price/v1.0.0`, `sources/v1.2.0`. This matches the existing convention used by the conformance tool (`conformance/v0.1.0`).

A versioned document that does not live in a directory of its own takes its own name as the prefix instead, lowercased. [`GLOSSARY.md`](./GLOSSARY.md) is the only one today and its tags are `glossary/v*`. Its `v1.0.0` and `v1.1.0` name commits made before it carried a version line, since its history was reconstructed when the line was added; the tag names the state of the vocabulary at that commit, not a version string present in the file.

Because the tag's major and the wire byte are the same number by construction, a frame on the wire identifies the tag family that produced it. `Schema Version = 1` on a `0x4442` frame means the publisher implements some `market-by-price/v1.*` tag.

A tag is cut when a spec's version line changes in `main`. Tags are annotated and never moved. If a release is wrong, cut a new `PATCH`; do not retag.

The conformance tool in [`tools/conformance`](./tools/conformance) is software, not a specification. It has its own independent version and its own `conformance/v*` tags, which track the tool's release history and not any spec's version.

---

## Supplements and the glossary

Three documents here are not feed specs:

- **[Reference Data Distribution](./reference-data/spec.md)** defines a mechanism that rides on a host feed's frame header. It has no `Magic` and no `Schema Version` of its own; frames carrying it are versioned by the host feed. It is versioned independently because a publisher and subscriber operating under the same version of the supplement interoperate regardless of which feed specs they implement.
- **[Source ID Registry](./sources/spec.md)** is a registry with no wire format. Its version tracks the assignment set. Assigning a new Source ID is an additive change and therefore a `MINOR` bump, as is adding a column to the assignment table. Because assigned IDs are stable and MUST NOT be renumbered, reordered, removed, or reused, the registry has no mechanism by which a `MAJOR` bump could arise.
- **[Glossary](./GLOSSARY.md)** is the canonical vocabulary and has no wire format. Its version tracks the vocabulary, so that a spec or a repository can name the ruling it was written against. Defining a term, banning a word, or recording an exception is additive and therefore `MINOR`; redefining a term that conforming text already follows is `MAJOR`, because prose and identifiers in several repositories have to be revisited. Its own *Versioning* section states the rule.

---

## Pre-1.0 history

Several specs carried `0.1.0` (and Perp Stats carried `0.1.1`) while circulating as drafts for publisher and subscriber feedback. Those versions were never tagged. All specs were promoted to `1.0.0` together as the first stable, tagged release; the wire format did not change at the promotion, and the `Schema Version` byte was `1` before and after.

The Perp Stats `0.1.1` change (registering frame `Magic` `0x4450` and adding the consumer validation requirement) is retained in that spec's changelog as part of its `1.0.0` history.
