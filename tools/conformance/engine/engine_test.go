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
	// MBO implements schema 2 (spec 2.x), so 3 is the first unknown-future one.
	e.beginFrame(3)                                                           // schema version 3 > implemented
	e.Emit("FIELD.SIDE_ENUM", core.Violation, core.PortMktData, 0, 0, 0, "x") // version-specific → downgrade
	if cap.last.Severity == core.Must || cap.last.Status == core.Violation {
		t.Fatal("version-specific check must downgrade under unknown schema")
	}
	e.Emit("FRAME.MAGIC_MISMATCH", core.Violation, core.PortMktData, 0, 0, 0, "x") // envelope → stays
	if cap.last.Status != core.Violation {
		t.Fatal("envelope check must still fire under unknown schema")
	}
}

// TestEmitStaleSchemaKeepsChecking pins the deliberate asymmetry in beginFrame:
// a *future* schema downgrades version-specific rules, a *stale* one does not.
//
// Suppressing on stale would read as the friendlier migration behaviour, but it
// silences rules that are still catching real defects — on the bundled pre-2.0
// nonconformant_mbp capture it drops MSG.SNAPSHOT_FLAG_MATCHES_PORT from 6
// violations to 0, turning a regression fixture into a clean-looking run. If
// this test ever fails because someone changed > to !=, read the beginFrame
// comment before "fixing" it.
func TestEmitStaleSchemaKeepsChecking(t *testing.T) {
	cap := &capture{}
	e := New(Config{Feed: core.FeedMBO}, cap) // MBO implements schema 2
	e.beginFrame(1)                           // stale 1.x publisher

	e.Emit("MSG.LENGTH_PER_TYPE", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Status != core.Violation {
		t.Fatal("a stale schema must NOT silence version-specific rules; they still catch real defects")
	}

	e.Emit("FRAME.SCHEMA_VERSION", core.Violation, core.PortRefData, 0, 0, 0, "x")
	if cap.last.Status != core.Violation {
		t.Fatal("FRAME.SCHEMA_VERSION is an envelope rule and must report the stale version itself")
	}
}
