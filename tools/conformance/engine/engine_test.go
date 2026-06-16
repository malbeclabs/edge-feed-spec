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
	e.beginFrame(2)                                                           // schema version 2 > implemented
	e.Emit("FIELD.SIDE_ENUM", core.Violation, core.PortMktData, 0, 0, 0, "x") // v1-specific → downgrade
	if cap.last.Severity == core.Must || cap.last.Status == core.Violation {
		t.Fatal("v1-specific check must downgrade under unknown schema")
	}
	e.Emit("FRAME.MAGIC_MISMATCH", core.Violation, core.PortMktData, 0, 0, 0, "x") // envelope → stays
	if cap.last.Status != core.Violation {
		t.Fatal("envelope check must still fire under unknown schema")
	}
}
