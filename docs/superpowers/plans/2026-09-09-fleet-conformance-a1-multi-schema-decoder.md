# Multi-Schema Decoder Implementation Plan (Fleet Conformance, Phase A1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one `dz-conformance` binary grade both schema 1 and schema 3 feeds, so the per-venue version pin can be deleted.

**Architecture:** The frame header already carries a `Schema Version` byte. Today the decoder asserts it equals one value per feed, and every `InstrumentDefinition` offset is a compile-time constant for schema 3. This plan turns the assertion into a membership test over a supported set, and turns the offsets into a small lookup keyed by `(feed, schema)`. Nothing else in the engine is schema-dependent: schema 2 and 3 moved only the `InstrumentDefinition` layout, and schema 2 was never deployed.

**Tech Stack:** Go 1.x, standard library plus `github.com/prometheus/client_golang`. No new dependency.

**Spec:** `docs/superpowers/specs/2026-09-09-fleet-conformance-design.md` — §2 is this plan. §3, §4 and the deploy are phase A2 and are **out of scope here**.

## Global Constraints

- Repository: `malbeclabs/edge-feed-spec`. All bare paths below are relative to `tools/conformance/`.
- Commit subjects are `component: short description`, lowercase except proper nouns. No `Co-Authored-By` lines.
- The `dz-conformance` `--version` output must stay exactly `version+commit` on one line. `malbeclabs/infra`'s `dz_conformance` role parses it and requires `^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$` after trimming a leading `v`. A space or a parenthesis makes it re-download the binary and restart every instance on every run.
- Supported schema versions after this plan: **midpoint = {1}**, **every other feed = {1, 3}**. Schema **2 is not implemented** and stays rejected.
- Existing rule IDs and their severities do not change. No rule is added or removed.

---

## File Structure

| File | Responsibility after this plan |
| --- | --- |
| `wire/header.go` | Owns which schema versions a feed's decoder accepts. `ExpectedSchemaVersion` is replaced by `SchemaSupported`. |
| `wire/decode.go` | Emits `FRAME.SCHEMA_VERSION` on an unsupported version, unchanged otherwise. |
| `engine/instrdef.go` | **New.** The `(feed, schema) → InstrumentDefinition layout` table, in one place. |
| `engine/fields.go` | Field accessors read offsets from the layout instead of constants. |
| `engine/tier1.go` | `expectedMsgLen` takes a schema. |
| `engine/gate.go` | Seven call sites pass the frame's schema. |
| `engine/refdata.go` | One call site passes the frame's schema. |
| `VERSIONING.md` | Records the validator's documented exception to the MUST-reject rule. |

Why a new `engine/instrdef.go` rather than more constants in `fields.go`: the layout is the only schema-dependent thing in the tool, and it is about to be read from three files. One table, named once, is what stops the next schema from being a scavenger hunt. `fields.go` is 266 lines and already mixes eight message types; it does not need a second axis.

---

### Task 1: Wire accepts a set of schema versions

**Files:**
- Modify: `wire/header.go:16-30` (replace `ExpectedSchemaVersion`)
- Modify: `wire/decode.go:37-41` (the `FRAME.SCHEMA_VERSION` check)
- Test: `wire/decode_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `wire.SchemaSupported(magic uint16, ver uint8) bool` and `wire.SupportedSchemas(magic uint16) []uint8`. Task 2 and Task 6 use both.

- [ ] **Step 1: Write the failing test**

Add to `wire/decode_test.go`:

```go
func TestSchemaSupported(t *testing.T) {
	cases := []struct {
		name  string
		magic uint16
		ver   uint8
		want  bool
	}{
		{"tob schema 3 is current", MagicTOB, 3, true},
		{"tob schema 1 is the hyperliquid publisher", MagicTOB, 1, true},
		{"tob schema 2 was never deployed", MagicTOB, 2, false},
		{"tob schema 0 is not a version", MagicTOB, 0, false},
		{"tob schema 4 does not exist yet", MagicTOB, 4, false},
		{"mbo schema 1", MagicMBO, 1, true},
		{"mbo schema 3", MagicMBO, 3, true},
		{"mbp schema 3", MagicMBP, 3, true},
		{"midpoint kept its 64-byte variant at 1", MagicMid, 1, true},
		{"midpoint never widened to 3", MagicMid, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SchemaSupported(tc.magic, tc.ver); got != tc.want {
				t.Fatalf("SchemaSupported(0x%04X, %d) = %v, want %v", tc.magic, tc.ver, got, tc.want)
			}
		})
	}
}

func TestDecodeAcceptsSchema1TOB(t *testing.T) {
	raw := wirebuild.Frame(MagicTOB).Schema(1).Channel(0).Seq(1).
		Msg(TypeHeartbeat, 16, func(b *wirebuild.Body) { b.U8(0).Pad(11) }).
		Bytes()

	_, fs := Decode(raw, MagicTOB)
	for _, f := range fs {
		if f.RuleID == "FRAME.SCHEMA_VERSION" {
			t.Fatalf("schema 1 must not raise FRAME.SCHEMA_VERSION: %s", f.Detail)
		}
	}
}

func TestDecodeRejectsSchema2TOB(t *testing.T) {
	raw := wirebuild.Frame(MagicTOB).Schema(2).Channel(0).Seq(1).
		Msg(TypeHeartbeat, 16, func(b *wirebuild.Body) { b.U8(0).Pad(11) }).
		Bytes()

	_, fs := Decode(raw, MagicTOB)
	var found bool
	for _, f := range fs {
		if f.RuleID == "FRAME.SCHEMA_VERSION" {
			found = true
			if f.Transport {
				t.Fatal("an unsupported schema is a publisher fault, not transport corruption")
			}
		}
	}
	if !found {
		t.Fatal("schema 2 must raise FRAME.SCHEMA_VERSION")
	}
}
```

Add the import for `wirebuild` if `decode_test.go` does not already have it:

```go
import "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
```

If `decode_test.go` is in `package wire` rather than `wire_test`, `wirebuild` imports `wire`, so put these three tests in a new file `wire/schema_test.go` with `package wire_test` and qualify every symbol (`wire.MagicTOB`, `wire.Decode`, …). Check the existing package clause before writing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd tools/conformance && go test ./wire/ -run 'TestSchemaSupported|TestDecodeAcceptsSchema1TOB|TestDecodeRejectsSchema2TOB' -v`
Expected: FAIL, `undefined: SchemaSupported`.

- [ ] **Step 3: Replace `ExpectedSchemaVersion` in `wire/header.go`**

Delete `ExpectedSchemaVersion` and put this in its place:

```go
// SupportedSchemas returns the frame Schema Version bytes this validator decodes
// for the given feed, identified by magic.
//
// Per VERSIONING.md the byte equals the spec's MAJOR version, so the set is
// per-feed. Midpoint kept its slimmed 64-byte InstrumentDefinition when the other
// five widened Symbol to char[64], so it is still at spec 1.x while its siblings
// are at 3.x. Keying on magic rather than core.Feed avoids an import cycle and is
// exact: magic identifies the feed.
//
// **This validator accepts more than one MAJOR version, and that is deliberate.**
// VERSIONING.md requires a decoder to reject a Schema Version it was not built
// for. A fleet validator grades every venue at once, and venues sit on different
// MAJOR versions, so a single-version binary forces a per-venue build pin —
// exactly the failure this set removes. The exception is scoped to this tool and
// recorded in VERSIONING.md. Production consumers keep the reject rule.
//
// Schema 2 is absent on purpose. It widened Symbol to char[64] (80 -> 128 bytes)
// and no publisher ever deployed it, so there is no capture to test a schema-2
// layout against. A layout nobody has seen on the wire is a guess, and a guess
// that silently decodes is worse than a rejection.
func SupportedSchemas(magic uint16) []uint8 {
	if magic == MagicMid {
		return []uint8{1}
	}
	return []uint8{1, 3}
}

// SchemaSupported reports whether this validator decodes ver for the feed
// identified by magic.
func SchemaSupported(magic uint16, ver uint8) bool {
	for _, v := range SupportedSchemas(magic) {
		if v == ver {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Update the check in `wire/decode.go`**

Replace:

```go
	if want := ExpectedSchemaVersion(expectMagic); h.SchemaVersion != want {
		fs = append(fs, StructFinding{"FRAME.SCHEMA_VERSION", 2,
			fmt.Sprintf("schema version %d, expected %d", h.SchemaVersion, want), false})
	}
```

with:

```go
	if !SchemaSupported(expectMagic, h.SchemaVersion) {
		fs = append(fs, StructFinding{"FRAME.SCHEMA_VERSION", 2,
			fmt.Sprintf("schema version %d, supported %v", h.SchemaVersion, SupportedSchemas(expectMagic)), false})
	}
```

- [ ] **Step 5: Fix every other caller of the deleted function**

Run: `cd tools/conformance && grep -rn 'ExpectedSchemaVersion' --include '*.go' .`

Expected: hits in test files that assert the old single value. Rewrite each to `SchemaSupported(magic, ver)`. Do not delete a test to make it compile; a test asserting "schema 3 is expected for TOB" becomes "schema 3 is supported for TOB".

- [ ] **Step 6: Run the wire tests**

Run: `cd tools/conformance && go test ./wire/... -v`
Expected: PASS, including the three new tests.

- [ ] **Step 7: Commit**

```bash
git add tools/conformance/wire/
git commit -m "conformance: accept a set of schema versions per feed"
```

---

### Task 2: The `(feed, schema)` InstrumentDefinition layout table

**Files:**
- Create: `engine/instrdef.go`
- Test: `engine/instrdef_test.go`

**Interfaces:**
- Consumes: `wire.SchemaSupported` from Task 1.
- Produces: `type instrDefLayout struct` with fields `MsgLen uint8`, `InstrumentID, ManifestSeq, PriceBound, DefaultMethod int` (body offsets, `-1` when the field is absent), and `func instrDefLayoutFor(feed core.Feed, schema uint8) (instrDefLayout, bool)`. Tasks 3, 4 and 5 use both.

The offsets below are transcribed from the specs, not inferred. Schema 3 comes from the current `top-of-book/spec.md` and `market-by-order/spec.md`. **Schema 1 comes from tag `top-of-book/v1.0.0`, because the current spec files no longer describe the 80-byte layout.** That is the whole reason this table needs a comment: the source of truth for half of it is a git tag.

Spec offsets include the 4-byte message header; body offsets do not. Body offset = spec offset − 4.

Schema 1, 80-byte message (`top-of-book/v1.0.0`, "0x02 InstrumentDefinition (80 bytes)"):

| Spec off | Field | Body off |
| --- | --- | --- |
| 4 | Instrument ID `u32` | 0 |
| 8 | Symbol `char[16]` | 4 |
| 77 | Price Bound `u8` | 73 |
| 78 | Manifest Seq `u16` | 74 |

Schema 3, 130-byte message: Instrument ID body 0, Price Bound body 123, Manifest Seq body 124. Midpoint, 64-byte message, schema 1: Instrument ID body 0, Default Method body 38, Price Bound body 39, Manifest Seq body 56.

- [ ] **Step 1: Write the failing test**

Create `engine/instrdef_test.go`:

```go
package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

func TestInstrDefLayoutFor(t *testing.T) {
	cases := []struct {
		name          string
		feed          core.Feed
		schema        uint8
		ok            bool
		msgLen        uint8
		manifestSeq   int
		priceBound    int
		defaultMethod int
	}{
		// Schema 3: the current five-feed layout.
		{"tob schema 3", core.FeedTOB, 3, true, 130, 124, 123, -1},
		{"mbo schema 3", core.FeedMBO, 3, true, 130, 124, 123, -1},
		{"mbp schema 3", core.FeedMBP, 3, true, 130, 124, 123, -1},
		// Schema 1: the 80-byte layout, recovered from tag top-of-book/v1.0.0.
		{"tob schema 1", core.FeedTOB, 1, true, 80, 74, 73, -1},
		{"mbo schema 1", core.FeedMBO, 1, true, 80, 74, 73, -1},
		// Midpoint never left its own 64-byte variant.
		{"midpoint schema 1", core.FeedMidpoint, 1, true, 64, 56, 39, 38},
		// Not supported.
		{"tob schema 2 was never deployed", core.FeedTOB, 2, false, 0, 0, 0, 0},
		{"midpoint schema 3 does not exist", core.FeedMidpoint, 3, false, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := instrDefLayoutFor(tc.feed, tc.schema)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.MsgLen != tc.msgLen {
				t.Errorf("MsgLen = %d, want %d", got.MsgLen, tc.msgLen)
			}
			if got.ManifestSeq != tc.manifestSeq {
				t.Errorf("ManifestSeq = %d, want %d", got.ManifestSeq, tc.manifestSeq)
			}
			if got.PriceBound != tc.priceBound {
				t.Errorf("PriceBound = %d, want %d", got.PriceBound, tc.priceBound)
			}
			if got.DefaultMethod != tc.defaultMethod {
				t.Errorf("DefaultMethod = %d, want %d", got.DefaultMethod, tc.defaultMethod)
			}
		})
	}
}

// The two layouts differ by exactly the two MAJOR changes VERSIONING.md records:
// schema 2 widened Symbol from char[16] to char[64] (+48) and schema 3 inserted
// Source ID after Instrument ID (+2). Every field after Symbol therefore sits 50
// bytes later in schema 3 than in schema 1. This pins that arithmetic so a
// hand-edited offset cannot drift without failing here.
func TestSchema1And3DifferByFiftyBytesAfterSymbol(t *testing.T) {
	s1, ok1 := instrDefLayoutFor(core.FeedTOB, 1)
	s3, ok3 := instrDefLayoutFor(core.FeedTOB, 3)
	if !ok1 || !ok3 {
		t.Fatal("both TOB layouts must resolve")
	}
	if d := s3.ManifestSeq - s1.ManifestSeq; d != 50 {
		t.Errorf("ManifestSeq delta = %d, want 50", d)
	}
	if d := s3.PriceBound - s1.PriceBound; d != 50 {
		t.Errorf("PriceBound delta = %d, want 50", d)
	}
	if d := int(s3.MsgLen) - int(s1.MsgLen); d != 50 {
		t.Errorf("MsgLen delta = %d, want 50", d)
	}
	if s1.InstrumentID != s3.InstrumentID {
		t.Error("Instrument ID precedes both insertions and must not move")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd tools/conformance && go test ./engine/ -run 'TestInstrDefLayout|TestSchema1And3' -v`
Expected: FAIL, `undefined: instrDefLayoutFor`.

- [ ] **Step 3: Create `engine/instrdef.go`**

```go
package engine

import "github.com/malbeclabs/edge-feed-spec/tools/conformance/core"

// instrDefLayout holds the body offsets of the InstrumentDefinition (0x02) fields
// this validator reads, for one (feed, schema version) pair. Offsets are relative
// to the message body, so they are the spec offset minus the 4-byte message
// header. An absent field is -1.
//
// The InstrumentDefinition layout is the only part of the wire format that moves
// between the schema versions this tool accepts, which is why one table covers
// multi-schema support for the whole engine.
type instrDefLayout struct {
	MsgLen        uint8 // canonical Message Length, header included
	InstrumentID  int
	ManifestSeq   int
	PriceBound    int
	DefaultMethod int
}

// Body offsets for the three layouts. Read spec offsets from the tables named in
// each comment and subtract 4.
//
// **Schema 1's layout is not in the current spec files.** The 80-byte
// InstrumentDefinition was retired by the 2.0.0 and 3.0.0 MAJOR releases, and
// tools/../top-of-book/spec.md now documents only the 130-byte form. These offsets
// are transcribed from tag `top-of-book/v1.0.0`, section "0x02
// InstrumentDefinition (80 bytes)". Verify against that tag, not against HEAD.
//
// Per VERSIONING.md the two MAJOR steps were: 2.0.0 widened Symbol from char[16]
// to char[64] (80 -> 128 bytes), and 3.0.0 inserted Source ID (u16) after
// Instrument ID (128 -> 130). No publisher deployed schema 2, so the live gap
// between 1 and 3 is the sum of both steps and every field after Symbol sits 50
// bytes later. instrdef_test.go pins that delta.
var (
	// top-of-book/v1.0.0 "0x02 InstrumentDefinition (80 bytes)":
	// Instrument ID spec 4, Price Bound spec 77, Manifest Seq spec 78.
	instrDefSchema1 = instrDefLayout{MsgLen: 80, InstrumentID: 0, ManifestSeq: 74, PriceBound: 73, DefaultMethod: -1}

	// top-of-book/spec.md and market-by-order/spec.md at schema 3:
	// Instrument ID spec 4, Price Bound spec 127, Manifest Seq spec 128.
	instrDefSchema3 = instrDefLayout{MsgLen: 130, InstrumentID: 0, ManifestSeq: 124, PriceBound: 123, DefaultMethod: -1}

	// midpoint/spec.md, still at 1.0.x with the slimmed 64-byte variant:
	// Instrument ID spec 4, Default Method spec 42, Price Bound spec 43, Manifest Seq spec 60.
	instrDefMidpoint = instrDefLayout{MsgLen: 64, InstrumentID: 0, ManifestSeq: 56, PriceBound: 39, DefaultMethod: 38}
)

// instrDefLayoutFor returns the InstrumentDefinition layout for a feed at a schema
// version, and whether the pair is supported. An unsupported pair returns false;
// the caller must not fall back to a default layout, because decoding 130-byte
// offsets out of an 80-byte message reads garbage rather than failing.
func instrDefLayoutFor(feed core.Feed, schema uint8) (instrDefLayout, bool) {
	if feed == core.FeedMidpoint {
		if schema == 1 {
			return instrDefMidpoint, true
		}
		return instrDefLayout{}, false
	}
	switch schema {
	case 1:
		return instrDefSchema1, true
	case 3:
		return instrDefSchema3, true
	default:
		return instrDefLayout{}, false
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd tools/conformance && go test ./engine/ -run 'TestInstrDefLayout|TestSchema1And3' -v`
Expected: PASS.

- [ ] **Step 5: Assert the table agrees with `wire`**

Two files now hold "which schemas are supported". Add to `engine/instrdef_test.go`:

```go
// The layout table and wire's accepted set must agree. A frame wire accepts but
// the layout table cannot resolve would reach the field accessors with no offsets,
// and a layout for a version wire rejects is dead code.
func TestLayoutTableAgreesWithWire(t *testing.T) {
	for _, feed := range []core.Feed{core.FeedTOB, core.FeedMidpoint, core.FeedMBO, core.FeedMBP} {
		magic := MagicFor(feed)
		for ver := uint8(0); ver < 8; ver++ {
			_, layoutOK := instrDefLayoutFor(feed, ver)
			wireOK := wire.SchemaSupported(magic, ver)
			if layoutOK != wireOK {
				t.Errorf("feed %s schema %d: layout=%v wire=%v", feed, ver, layoutOK, wireOK)
			}
		}
	}
}
```

Add `"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"` to the test imports.

- [ ] **Step 6: Run it**

Run: `cd tools/conformance && go test ./engine/ -run TestLayoutTableAgreesWithWire -v`
Expected: PASS. A failure here means Task 1 and Task 2 disagree; fix the table, not the test.

- [ ] **Step 7: Commit**

```bash
git add tools/conformance/engine/instrdef.go tools/conformance/engine/instrdef_test.go
git commit -m "conformance: add the per-schema instrumentdefinition layout table"
```

---

### Task 3: `expectedMsgLen` takes a schema

**Files:**
- Modify: `engine/tier1.go:23-31` (the function), `engine/tier1.go:251`, `engine/tier1.go:292`
- Modify: `engine/gate.go:487,501,518,526,818,824,830`
- Test: `engine/tier1_test.go`

**Interfaces:**
- Consumes: `instrDefLayoutFor` from Task 2.
- Produces: `expectedMsgLen(feed core.Feed, schema uint8, typ uint8) uint8`. Task 4 relies on this signature.

- [ ] **Step 1: Write the failing test**

Add to `engine/tier1_test.go`:

```go
func TestExpectedMsgLenIsSchemaAware(t *testing.T) {
	// InstrumentDefinition is the only type whose length moves.
	if got := expectedMsgLen(core.FeedTOB, 3, wire.TypeInstrumentDef); got != 130 {
		t.Errorf("tob schema 3 instrdef = %d, want 130", got)
	}
	if got := expectedMsgLen(core.FeedTOB, 1, wire.TypeInstrumentDef); got != 80 {
		t.Errorf("tob schema 1 instrdef = %d, want 80", got)
	}
	if got := expectedMsgLen(core.FeedMidpoint, 1, wire.TypeInstrumentDef); got != 64 {
		t.Errorf("midpoint instrdef = %d, want 64", got)
	}
	// An unsupported pair returns 0, which the callers already treat as
	// "no canonical length known" and skip. It must not fall back to 130.
	if got := expectedMsgLen(core.FeedTOB, 2, wire.TypeInstrumentDef); got != 0 {
		t.Errorf("tob schema 2 instrdef = %d, want 0", got)
	}
	// Every other type is schema-invariant across 1 and 3.
	for _, typ := range []uint8{
		wire.TypeHeartbeat, wire.TypeQuote, wire.TypeTrade, wire.TypeEndOfSession,
		wire.TypeManifest, wire.TypeOrderAdd, wire.TypeOrderCancel, wire.TypeOrderExecute,
		wire.TypeBatchBoundary, wire.TypeInstrReset,
	} {
		one := expectedMsgLen(core.FeedTOB, 1, typ)
		three := expectedMsgLen(core.FeedTOB, 3, typ)
		if one != three {
			t.Errorf("type 0x%02X: schema 1 = %d, schema 3 = %d; only InstrumentDefinition may differ", typ, one, three)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd tools/conformance && go test ./engine/ -run TestExpectedMsgLenIsSchemaAware -v`
Expected: FAIL, "not enough arguments in call to expectedMsgLen".

- [ ] **Step 3: Change the signature and the InstrumentDefinition arm**

In `engine/tier1.go`, change the declaration and the one arm that depends on schema:

```go
// expectedMsgLen returns the canonical wire length (including the 4-byte header)
// for message types that have a fixed size at the given feed and schema version.
// Returns 0 for types not defined in a feed, and for a (feed, schema) pair this
// validator does not decode — the callers already skip a zero rather than
// asserting a length.
func expectedMsgLen(feed core.Feed, schema uint8, typ uint8) uint8 {
	switch typ {
	case wire.TypeHeartbeat:
		return 16
	case wire.TypeInstrumentDef:
		l, ok := instrDefLayoutFor(feed, schema)
		if !ok {
			return 0
		}
		return l.MsgLen
	case wire.TypeQuote: // 0x03: Quote (TOB) or Midpoint
```

Leave every other arm exactly as it is. Delete the old `if feed == core.FeedMidpoint { return 64 }; return 130` body of the `TypeInstrumentDef` arm; the layout table now owns those numbers.

- [ ] **Step 4: Update the nine call sites**

Each call site is inside per-frame processing and has the frame in scope. Pass `f.Header.SchemaVersion`.

`engine/tier1.go:251` and `engine/tier1.go:292` become:

```go
		if exp := expectedMsgLen(e.cfg.Feed, f.Header.SchemaVersion, m.Type); exp != 0 && m.Length != exp {
```

```go
	exp := expectedMsgLen(e.cfg.Feed, f.Header.SchemaVersion, m.Type)
```

The seven in `engine/gate.go` (487, 501, 518, 526, 818, 824, 830) become:

```go
			if m.Length != expectedMsgLen(e.cfg.Feed, f.Header.SchemaVersion, m.Type) {
```

Run: `cd tools/conformance && grep -n 'expectedMsgLen(' engine/gate.go` first. If a call site has no `f` in scope, the enclosing function needs the frame threaded in; add a `schema uint8` parameter to that function and pass `f.Header.SchemaVersion` from its caller rather than reaching for package state. Do **not** cache the schema on the `Engine` struct: it would be set in one place and read in nine, which is the temporal coupling this table exists to avoid.

- [ ] **Step 5: Run the full engine suite**

Run: `cd tools/conformance && go test ./engine/... -v 2>&1 | tail -40`
Expected: PASS. Existing tests call `expectedMsgLen` with two arguments and will fail to compile; add `3` as the schema to each, because every existing test fixture is a schema-3 frame. Confirm that assumption per test rather than assuming it: `grep -n 'Schema(' engine/*_test.go`.

- [ ] **Step 6: Commit**

```bash
git add tools/conformance/engine/
git commit -m "conformance: key the canonical message length on schema version"
```

---

### Task 4: Field accessors read offsets from the layout

**Files:**
- Modify: `engine/fields.go:207-266`
- Modify: `engine/refdata.go:786`
- Test: `engine/fields_test.go` (create if absent)

**Interfaces:**
- Consumes: `instrDefLayoutFor` from Task 2.
- Produces: `instrDefAllFields(feed core.Feed, schema uint8, m wire.Message) (instrID uint32, manifestSeq uint16, defaultMethod, priceBound uint8, ok bool)`. The new trailing `ok` is false for an unsupported `(feed, schema)`; Task 5 relies on it.

- [ ] **Step 1: Write the failing test**

Create `engine/fields_test.go` (or append if it exists):

```go
package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// instrDefMsg builds an InstrumentDefinition body of bodyLen bytes with
// instrumentID at body 0, and priceBound / manifestSeq written at the given body
// offsets, then decodes it back out through the real frame walk.
func instrDefMsg(t *testing.T, magic uint16, schema uint8, msgLen uint8,
	instrID uint32, priceBoundOff, manifestSeqOff int, priceBound uint8, manifestSeq uint16,
) wire.Message {
	t.Helper()
	bodyLen := int(msgLen) - wire.MsgHeaderLen
	body := make([]byte, bodyLen)
	body[0] = byte(instrID)
	body[1] = byte(instrID >> 8)
	body[2] = byte(instrID >> 16)
	body[3] = byte(instrID >> 24)
	body[priceBoundOff] = priceBound
	body[manifestSeqOff] = byte(manifestSeq)
	body[manifestSeqOff+1] = byte(manifestSeq >> 8)

	raw := wirebuild.Frame(magic).Schema(schema).Channel(0).Seq(1).
		Msg(wire.TypeInstrumentDef, msgLen, func(b *wirebuild.Body) {
			for _, x := range body {
				b.U8(x)
			}
		}).Bytes()

	f, fs := wire.Decode(raw, magic)
	for _, sf := range fs {
		if !sf.Transport {
			t.Fatalf("fixture frame is not clean: %s %s", sf.RuleID, sf.Detail)
		}
	}
	if len(f.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(f.Messages))
	}
	return f.Messages[0]
}

func TestInstrDefAllFieldsSchema3(t *testing.T) {
	m := instrDefMsg(t, wire.MagicTOB, 3, 130, 0xAABBCCDD, 123, 124, 2, 0x1234)
	id, seq, _, bound, ok := instrDefAllFields(core.FeedTOB, 3, m)
	if !ok {
		t.Fatal("schema 3 must resolve")
	}
	if id != 0xAABBCCDD {
		t.Errorf("instrumentID = %#x, want 0xAABBCCDD", id)
	}
	if seq != 0x1234 {
		t.Errorf("manifestSeq = %#x, want 0x1234", seq)
	}
	if bound != 2 {
		t.Errorf("priceBound = %d, want 2", bound)
	}
}

// The regression this whole plan exists to prevent: a schema-1 message read with
// schema-3 offsets. Before the layout table, offsets 123/124 fell outside an
// 80-byte message's 76-byte body.
func TestInstrDefAllFieldsSchema1(t *testing.T) {
	m := instrDefMsg(t, wire.MagicTOB, 1, 80, 0x11223344, 73, 74, 1, 0x0777)
	id, seq, _, bound, ok := instrDefAllFields(core.FeedTOB, 1, m)
	if !ok {
		t.Fatal("schema 1 must resolve")
	}
	if id != 0x11223344 {
		t.Errorf("instrumentID = %#x, want 0x11223344", id)
	}
	if seq != 0x0777 {
		t.Errorf("manifestSeq = %#x, want 0x0777", seq)
	}
	if bound != 1 {
		t.Errorf("priceBound = %d, want 1", bound)
	}
}

// Reading past the body must report absence, never a zero that looks like data.
func TestInstrDefAllFieldsRejectsUnsupportedSchema(t *testing.T) {
	m := instrDefMsg(t, wire.MagicTOB, 1, 80, 1, 73, 74, 1, 1)
	if _, _, _, _, ok := instrDefAllFields(core.FeedTOB, 2, m); ok {
		t.Fatal("schema 2 has no layout and must not resolve")
	}
}

func TestInstrDefAllFieldsMidpoint(t *testing.T) {
	m := instrDefMsg(t, wire.MagicMid, 1, 64, 7, 39, 56, 1, 9)
	id, seq, method, bound, ok := instrDefAllFields(core.FeedMidpoint, 1, m)
	if !ok {
		t.Fatal("midpoint schema 1 must resolve")
	}
	if id != 7 || seq != 9 || bound != 1 {
		t.Errorf("got id=%d seq=%d bound=%d, want 7/9/1", id, seq, bound)
	}
	_ = method // Default Method is exercised by the midpoint suite.
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd tools/conformance && go test ./engine/ -run TestInstrDefAllFields -v`
Expected: FAIL, "assignment mismatch: 5 variables but instrDefAllFields returns 4 values".

- [ ] **Step 3: Rewrite the accessors in `engine/fields.go`**

Replace everything from the `// --- InstrumentDefinition (0x02) — feed-dependent layout ---` comment through the end of `instrDefAllFields` with:

```go
// --- InstrumentDefinition (0x02) — feed- and schema-dependent layout ---
//
// Every offset lives in instrdef.go, keyed by (feed, schema). Nothing here
// hardcodes one, because the whole point of that table is that a reader looking
// for "where is Manifest Seq" finds one answer.
//
// bodyU8At and bodyU16LEAt bounds-check, because a wrong layout must read as
// absent rather than as zero: a zero Manifest Seq is a legal value and would be
// graded as data.

// instrDefAllFields extracts (instrumentID, manifestSeq, defaultMethod,
// priceBound) from an InstrumentDefinition at the given feed and schema version.
//
// ok is false when the (feed, schema) pair has no layout, or when the message is
// shorter than its layout requires. The caller MUST skip the message on false and
// MUST NOT treat the zero values as read data.
//
// defaultMethod is only present on midpoint. priceBound is absent on TOB in both
// schemas and reads zero there, as it did before this became schema-aware.
func instrDefAllFields(feed core.Feed, schema uint8, m wire.Message) (instrID uint32, manifestSeq uint16, defaultMethod, priceBound uint8, ok bool) {
	l, ok := instrDefLayoutFor(feed, schema)
	if !ok {
		return 0, 0, 0, 0, false
	}
	instrID, ok = bodyU32LEAt(m, l.InstrumentID)
	if !ok {
		return 0, 0, 0, 0, false
	}
	manifestSeq, ok = bodyU16LEAt(m, l.ManifestSeq)
	if !ok {
		return 0, 0, 0, 0, false
	}
	if l.DefaultMethod >= 0 {
		defaultMethod, _ = bodyU8At(m, l.DefaultMethod)
	}
	// TOB carries no Price Bound in either schema; MBO/MBP and midpoint do. The
	// layout gives an offset for all non-midpoint feeds, so the feed check stays
	// here rather than becoming a fourth layout row.
	if feed != core.FeedTOB && l.PriceBound >= 0 {
		priceBound, _ = bodyU8At(m, l.PriceBound)
	}
	return instrID, manifestSeq, defaultMethod, priceBound, true
}

// bodyU8At, bodyU16LEAt and bodyU32LEAt read at a body offset, reporting whether
// the body is long enough. The unchecked bodyU8/bodyU16LE/bodyU32LE helpers above
// are for fixed-length messages whose length checkTier1 already gated.
func bodyU8At(m wire.Message, off int) (uint8, bool) {
	if off < 0 || off >= len(m.Body) {
		return 0, false
	}
	return m.Body[off], true
}

func bodyU16LEAt(m wire.Message, off int) (uint16, bool) {
	if off < 0 || off+2 > len(m.Body) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(m.Body[off:]), true
}

func bodyU32LEAt(m wire.Message, off int) (uint32, bool) {
	if off < 0 || off+4 > len(m.Body) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(m.Body[off:]), true
}
```

Delete `instrDefInstrumentID`, `instrDefManifestSeqTOBMBO`, `instrDefManifestSeqMid`, `instrDefDefaultMethodMid`, `instrDefPriceBoundMid` and `instrDefPriceBoundMBO`. Run `grep -rn 'instrDefManifestSeq\|instrDefPriceBound\|instrDefDefaultMethod\|instrDefInstrumentID' --include '*.go' .` and update any test that called one directly to go through `instrDefAllFields`.

Add `"encoding/binary"` to the `engine/fields.go` imports if it is not already there.

- [ ] **Step 4: Update the caller in `engine/refdata.go:786`**

```go
			instrID, manifestSeq, defaultMethod, priceBound, ok := instrDefAllFields(e.cfg.Feed, f.Header.SchemaVersion, m)
			if !ok {
				// No layout for this (feed, schema), or the body is short. The frame
				// already raised FRAME.SCHEMA_VERSION or MSG.LENGTH_PER_TYPE; reading
				// on would grade zero values as published data.
				continue
			}
```

Confirm the enclosing loop has `f` in scope and that `continue` is the right skip for it. If the enclosing construct is not a loop, return instead, and keep the comment.

- [ ] **Step 5: Run the engine suite**

Run: `cd tools/conformance && go test ./engine/... 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/conformance/engine/
git commit -m "conformance: read instrumentdefinition fields through the layout table"
```

---

### Task 5: An end-to-end schema-1 run over the real pipeline

**Files:**
- Test: `golden_test.go` (append)

**Interfaces:**
- Consumes: everything from Tasks 1 to 4.
- Produces: nothing. This is the task that proves the binary a fleet deploy would ship.

Tasks 1 to 4 test units. This one runs a schema-1 TOB stream through `Decode` and the `Engine` together and asserts no `must` rule fires. That is the assertion the group_vars comment says a wrong pin breaks, so it is the one that has to pass.

- [ ] **Step 1: Write the failing test**

Append to `golden_test.go`:

```go
// A schema-1 TOB stream must grade clean. Before the layout table, the binary
// built for schema 3 read a 130-byte InstrumentDefinition out of an 80-byte
// message and fired MSG.LENGTH_PER_TYPE — a must rule — on every definition,
// which is why hyperliquid and kalshi needed different pinned builds.
func TestSchema1TOBStreamRaisesNoMustViolation(t *testing.T) {
	eng := engine.New(engine.Config{Feed: core.FeedTOB}, report.Discard())

	def := func(instrID uint32, manifestSeq uint16) []byte {
		return wirebuild.Frame(wire.MagicTOB).Schema(1).Channel(0).Seq(uint64(instrID)).
			Msg(wire.TypeInstrumentDef, 80, func(b *wirebuild.Body) {
				b.U32(instrID)          // body 0..3   Instrument ID
				b.Char("BTC-USDT", 16)  // body 4..19  Symbol char[16]
				b.Char("BTC", 8)        // body 20..27 Leg1
				b.Char("USDT", 8)       // body 28..35 Leg2
				b.U8(1)                 // body 36     Asset Class = Crypto Spot
				b.U8(uint8(0xFE))       // body 37     Price Exponent = -2
				b.U8(uint8(0xFE))       // body 38     Qty Exponent = -2
				b.U8(0)                 // body 39     Market Model
				b.U64(100)              // body 40..47 Tick Size
				b.U64(1)                // body 48..55 Lot Size
				b.U64(0)                // body 56..63 Contract Value
				b.U64(0)                // body 64..71 Expiry
				b.U8(0)                 // body 72     Settle Type
				b.U8(0)                 // body 73     Price Bound
				b.U16(manifestSeq)      // body 74..75 Manifest Seq
			}).Bytes()
	}

	for _, raw := range [][]byte{def(1, 1), def(2, 1)} {
		f, fs := wire.Decode(raw, wire.MagicTOB)
		for _, sf := range fs {
			if sf.RuleID == "FRAME.SCHEMA_VERSION" || sf.RuleID == "FRAME.LENGTH_CONSISTENCY" {
				t.Fatalf("clean schema-1 frame raised %s: %s", sf.RuleID, sf.Detail)
			}
		}
		eng.Process(f, core.PortRefData, fs)
	}

	for _, fi := range eng.Findings() {
		if fi.Severity == core.Violation && core.MustRule(fi.RuleID) {
			t.Errorf("must-rule violation on a clean schema-1 stream: %s %s", fi.RuleID, fi.Detail)
		}
	}
}
```

The exact constructor names (`engine.New`, `report.Discard`, `eng.Process`, `eng.Findings`, `core.MustRule`) must match what the existing tests in `golden_test.go` and `engine/engine_test.go` use. Read one of those tests first and copy its setup rather than the names above; the assertion is the part that matters.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd tools/conformance && go test . -run TestSchema1TOBStreamRaisesNoMustViolation -v`
Expected: FAIL on the first run if any offset in the body builder above is wrong. That is the point: the builder is a hand-transcription of `top-of-book/v1.0.0` and disagreeing with `instrdef.go` means one of the two misread the tag.

- [ ] **Step 3: Reconcile any disagreement against the tag, not against the code**

If the test fails, fetch the tagged spec and re-read the table before changing either side:

```bash
gh api -X GET repos/malbeclabs/edge-feed-spec/contents/top-of-book/spec.md \
  -f ref='top-of-book/v1.0.0' --jq '.content' | base64 -d | sed -n '/0x02 InstrumentDefinition (80 bytes)/,/^####/p'
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd tools/conformance && go test . -run TestSchema1TOBStreamRaisesNoMustViolation -v`
Expected: PASS.

- [ ] **Step 5: Run everything**

Run: `cd tools/conformance && go test ./... 2>&1 | tail -20`
Expected: all packages PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/conformance/golden_test.go
git commit -m "conformance: pin a clean schema-1 tob stream end to end"
```

---

### Task 6: Record the exception in VERSIONING.md

**Files:**
- Modify: `VERSIONING.md` (the "Compatibility promise" section, after the "Across a `MAJOR` boundary" paragraph)

**Interfaces:**
- Consumes: `wire.SupportedSchemas` from Task 1, by name in the prose.
- Produces: nothing in code.

The spec says a decoder MUST reject an unimplemented `Schema Version`. The validator now does not. An undocumented exception to a MUST is how a rule quietly stops meaning anything.

- [ ] **Step 1: Add the subsection**

After the paragraph beginning "Across a `MAJOR` boundary there is no promise at all", insert:

```markdown
### One documented exception: the conformance validator

`tools/conformance` (`dz-conformance`) decodes more than one `MAJOR` version per
feed, and is the only implementation permitted to. It is a validator, not a
consumer: its job is to grade whatever a publisher emits, and the fleet it grades
runs several `MAJOR` versions at once. A single-version build would need one
pinned binary per venue, and a wrong pin misreads field offsets and reports a
`must` violation on every affected message — a false alarm caused by the checker,
which is worse than the drift it exists to catch.

The versions it accepts are `wire.SupportedSchemas`, and the per-version field
offsets are `engine/instrdef.go`. It accepts only versions with a layout
transcribed from a released tag; it rejects every other version exactly as this
section requires, including `2`, which no publisher deployed.

This exception does not extend to production consumers. A subscriber decoding
market data MUST still reject a `Schema Version` it does not implement, because a
best-effort parse of a moved field yields plausible wrong prices rather than an
error.
```

- [ ] **Step 2: Check the release class**

Adding a subsection with no normative change to the wire format is **editorial**, which VERSIONING.md classes as `PATCH`. Confirm against the class table in the same file, and do not bump any `Schema Version`.

- [ ] **Step 3: Commit**

```bash
git add VERSIONING.md
git commit -m "docs: record the validator's multi-schema exception"
```

---

### Task 7: Cut the release and collapse the pins

**Files:**
- Modify (in `malbeclabs/infra`): `ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml`, `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml`

**Interfaces:**
- Consumes: the release tag cut in Step 2.
- Produces: a single `dz_conformance_version` for the fleet. Phase A2's fleet mode assumes one version.

- [ ] **Step 1: Verify against a real capture before releasing**

Do not release on unit tests alone. Replay one real capture of each schema through the new binary and diff the JSON reports against the two pinned binaries they replace.

```bash
cd tools/conformance && go build -o /tmp/dz-conformance-multi .
# schema 1, hyperliquid; schema 3, kalshi. Use captures from the recorder S3 bucket.
/tmp/dz-conformance-multi --feed tob --pcap hl-tob.pcap  --json-report /tmp/hl-new.json
/tmp/dz-conformance-multi --feed mbp --pcap kalshi.pcap  --json-report /tmp/kalshi-new.json
```

Expected: `/tmp/hl-new.json` shows no `MSG.LENGTH_PER_TYPE` violation, and both reports have the same violation set as the pinned binary that currently grades that feed. A *new* violation is a finding to investigate, not a regression to suppress: it may be a real publisher fault the wrong pin was masking.

- [ ] **Step 2: Tag the release**

Follow the existing `conformance/<version>` tag scheme in this repository. This is a MINOR release: it adds accepted versions and removes none.

- [ ] **Step 3: Collapse the pins in `malbeclabs/infra`**

In `ansible/inventory/mainnet-beta/group_vars/dz_conformance.yml`, set `dz_conformance_version` to the new tag and delete the whole comment block explaining why the Hyperliquid pin must not be unified with Kalshi's. It documents a constraint that no longer exists, and a stale warning is read as a live one.

Delete the Kalshi pin from `ansible/inventory/mainnet-beta/group_vars/kalshi_feed_capture.yml`, leaving that file's other variables alone.

- [ ] **Step 4: Verify the version parse still works**

The role greps `--version` output. Confirm the new binary prints exactly one line matching `^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$` after a leading `v` is trimmed:

```bash
/tmp/dz-conformance-multi --version
```

Expected: one line, `<version>+<commit>`, no spaces and no parentheses.

- [ ] **Step 5: Roll one host first**

Apply to a single Kalshi metro, confirm `checks_total` keeps advancing and `violations_total` does not step, then roll the rest. A schema regression shows up as a violation rate change, not as a crash.

- [ ] **Step 6: Commit**

```bash
# in malbeclabs/infra
git add ansible/inventory/mainnet-beta/group_vars/
git commit -m "dz_conformance: collapse the per-venue pins onto one release"
```

---

## Self-Review

**Spec coverage.** §2 of the design is Tasks 1 to 4 (the decoder), Task 5 (the end-to-end proof), Task 6 (the `VERSIONING.md` exception the design says is required) and Task 7 (the pin deletion §2 lists as its outcome). §3, §4, §5, §6 and §7 of the design are phase A2 and later, and are named out of scope in the header. **One design claim is not covered here and is deliberately deferred:** §2 says schema 2 stays rejected, which Tasks 1 and 2 implement, but the design's assertion that "no deployed feed runs schema 2" is not re-verified by this plan. Task 7 Step 1 would surface it, since a schema-2 capture would grade as a `FRAME.SCHEMA_VERSION` violation.

**Placeholders.** None. Every code step carries the code. Task 5 Step 1 names constructors that must be checked against the existing tests rather than trusted, and says so, which is an instruction rather than a placeholder.

**Type consistency.** `instrDefLayout` fields are `MsgLen uint8` and `InstrumentID, ManifestSeq, PriceBound, DefaultMethod int`, used identically in Tasks 2, 3 and 4. `instrDefLayoutFor` returns `(instrDefLayout, bool)` throughout. `expectedMsgLen(feed, schema, typ)` has the same argument order in Task 3's definition and all nine call sites. `instrDefAllFields` gains a fifth return `ok bool` in Task 4 and every caller in that task handles it. `wire.SchemaSupported(magic, ver)` and `wire.SupportedSchemas(magic)` keep their signatures across Tasks 1, 2 and 6.

**A known risk this plan does not remove.** Task 2's schema-1 offsets are a hand-transcription of a git tag, and Task 5's test fixture is a second hand-transcription of the same tag. Two transcriptions agreeing is weaker evidence than it looks, since one reader made both. Task 7 Step 1, replaying a real hyperliquid capture, is the independent check, and it is the step to insist on if anything is cut for time.
