package engine

import (
	"strconv"
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// FRAME.SCHEMA_VERSION's rule description states the accepted set in prose, and
// wire.SupportedSchemas is the code that decides it. A comment asking the next
// editor to keep the two equal is not a mechanism; this is.
//
// It lives in engine rather than core because engine already imports both core
// and wire, so pinning them together costs no new import edge — and because core
// has no dependency on wire today, which is worth keeping.
func TestSchemaVersionDocNamesEverySupportedVersion(t *testing.T) {
	doc, ok := core.Doc("FRAME.SCHEMA_VERSION")
	if !ok {
		t.Fatal("FRAME.SCHEMA_VERSION has no rule doc")
	}
	for _, magic := range []uint16{wire.MagicTOB, wire.MagicMBO, wire.MagicMBP, wire.MagicMid} {
		for _, v := range wire.SupportedSchemas(magic) {
			if !strings.Contains(doc.Summary, strconv.Itoa(int(v))) {
				t.Errorf("schema %d is supported for magic 0x%04X but is not named in the rule description: %q",
					v, magic, doc.Summary)
			}
		}
	}
}

// The superseded rule's description must likewise stay true to CurrentSchema.
func TestSupersededDocNamesTheCurrentVersions(t *testing.T) {
	if _, ok := core.Doc("FRAME.SCHEMA_VERSION_SUPERSEDED"); !ok {
		t.Fatal("FRAME.SCHEMA_VERSION_SUPERSEDED has no rule doc")
	}
	// Every feed's current MAJOR must itself be a supported version, or the rule
	// would fire on a publisher doing exactly what the spec asks.
	for _, magic := range []uint16{wire.MagicTOB, wire.MagicMBO, wire.MagicMBP, wire.MagicMid} {
		cur := wire.CurrentSchema(magic)
		if !wire.SchemaSupported(magic, cur) {
			t.Errorf("magic 0x%04X: current schema %d is not in the supported set", magic, cur)
		}
	}
}
