package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

type capture struct {
	last core.Finding
	n    int
}

func (c *capture) Record(f core.Finding)                 { c.last = f; c.n++ }
func (c *capture) TransportLoss(core.Port)               {}
func (c *capture) TransportCorruption(core.Port, string) {}
func (c *capture) SnapshotAudit(string)                  {}
func (c *capture) SetInstrumentState(string, int)        {}

func TestEmitConditionalDowngrade(t *testing.T) {
	cap := &capture{}
	// REFDATA.MANIFEST_CADENCE is Conditional; with no --expect set it must NOT carry must/Violation.
	e := New(Config{Feed: core.FeedMBO}, cap)
	e.Emit("REFDATA.MANIFEST_CADENCE", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Severity == core.Must && cap.last.Status == core.Violation {
		t.Fatal("conditional cadence rule must downgrade when --expect unset")
	}
	// With the expectation configured, it keeps must/Violation.
	e2 := New(Config{Feed: core.FeedMBO, ExpectManifestCadence: 1}, cap)
	e2.Emit("REFDATA.MANIFEST_CADENCE", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Severity != core.Must || cap.last.Status != core.Violation {
		t.Fatalf("configured cadence rule should stay must/violation, got %v/%v", cap.last.Severity, cap.last.Status)
	}
}

func TestEmitUnknownSchemaDowngrade(t *testing.T) {
	cap := &capture{}
	e := New(Config{Feed: core.FeedMBO}, cap)
	// MBO implements schema 3 (spec 3.x), so 4 is the first unknown-future one.
	e.beginFrame(4)                                                           // schema version 4 > implemented
	e.Emit("FIELD.SIDE_ENUM", core.Violation, core.PortMktData, 0, 0, 0, "x") // version-specific → downgrade
	if cap.last.Severity == core.Must || cap.last.Status == core.Violation {
		t.Fatal("version-specific check must downgrade under unknown schema")
	}
	e.Emit("FRAME.MAGIC_MISMATCH", core.Violation, core.PortMktData, 0, 0, 0, "x") // envelope → stays
	if cap.last.Status != core.Violation {
		t.Fatal("envelope check must still fire under unknown schema")
	}
}

// TestEmitSupportedSchemaKeepsChecking pins that beginFrame's downgrade is
// membership in wire.SupportedSchemas, not a "how new is it" comparison: a
// producer on schema 1 is not the feed's newest (MBO's set is {1, 3}), but 1
// is a schema this build decodes, so it does not downgrade — the same as
// schema 3 would. There is no separate "stale" case any more; a schema either
// is in the set, in which case it is fully checked, or it is not, in which
// case it downgrades, regardless of whether it sits above or below the set.
//
// A schema-1 producer is not hypothetical: the bundled nonconformant_mbp
// capture is entirely schema 1. Silencing version-specific rules for it would
// drop MSG.SNAPSHOT_FLAG_MATCHES_PORT from 6 violations to 0, turning a
// regression fixture into a clean-looking run. If this test ever fails, read
// beginFrame's comment before "fixing" it — this pins the supported side of
// that membership test, not a particular schema number.
func TestEmitSupportedSchemaKeepsChecking(t *testing.T) {
	cap := &capture{}
	e := New(Config{Feed: core.FeedMBO}, cap) // MBO supports schema 1 and 3
	e.beginFrame(1)                           // supported, but not the newest

	e.Emit("MSG.LENGTH_PER_TYPE", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Status != core.Violation {
		t.Fatal("a supported older schema must NOT silence version-specific rules; they still catch real defects")
	}

	e.Emit("FRAME.SCHEMA_VERSION", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Status != core.Violation {
		t.Fatal("FRAME.SCHEMA_VERSION is an envelope rule and must report the version itself")
	}
}
