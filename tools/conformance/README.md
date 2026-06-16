# dz-conformance

A strict conformance subscriber for [edge-feed-spec](../../) publishers: it subscribes to one feed channel (Top-of-Book, Midpoint, or Market-by-Order) — live multicast or pcap replay — and validates the stream against 82 explicit conformance rules, surfacing every structural, sequence, and semantic violation the spec defines.

Unlike a production consumer (which is tolerant of publisher quirks — skipping unknown types, ignoring reserved bits, recovering silently from loss), this tool is **strict by design**: everything a lenient parser would ignore, it checks.

> **Assembled in layered PRs.** This is the foundation layer — the wire **codec** and the **rule registry**. The validation engine, reporters/CLI, and the demo stack land in the follow-up PRs in this stack.

## Two-tier conformance model

Every rule belongs to exactly one tier:

- **Tier 1 — intra-frame structural (loss-immune).** Decidable from a single intact datagram: magic, schema version, frame-length self-consistency, message-count-vs-walk, per-type message length, port placement, enum ranges, reserved bits. Packet loss cannot create a false Tier-1 positive.
- **Tier 2 — stateful / relational (verifiability-gated).** Require cross-message state. Before firing, the engine verifies that loss, cold-start ordering, reorder, bound-subset capture, or an in-flight reset cannot explain the anomaly. If it can, the result is `Unverifiable` — a first-class visible signal — never a false `Violation`.

## This layer

| Package | Role |
|---------|------|
| `core` | Shared types (`Finding`, `Feed`, `Port`, `Severity`, `Status`), the 82-rule registry (`Rules`, `Lookup`), and per-rule docs (`RuleDoc`, `SpecURL`). `core/registry.go` is the in-code source of truth for the rule set. |
| `wire` | Strict binary decoder for all three frame formats. `Decode(raw, magic)` returns `(*Frame, []StructFinding)` — intolerant: structural anomalies become findings rather than silent skips. Owns the frame magic constants. `wire/wirebuild` builds wire bytes programmatically for tests. |

`core/registry_test.go` enforces registry shape (82 rules, no duplicates, tier/state consistency); `core/ruledoc_test.go` guarantees every rule has a non-empty summary and a spec link.

## Build and test

```bash
go build ./...
go test ./...
```

No external services; the module is stdlib-only at this layer.
