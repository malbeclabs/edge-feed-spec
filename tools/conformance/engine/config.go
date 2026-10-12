package engine

import (
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// SourceRegistry authorises multicast source IDs. DefaultSourceRegistry (sources.go)
// provides the embedded default; tests inject a stub; nil means "all allowed".
type SourceRegistry interface {
	Allowed(sourceID uint16) bool
}

// Config carries the command-line options for a single conformance run.
type Config struct {
	Feed                core.Feed
	Strict              bool
	OracleConfirmCycles int
	ReorderWindow       int

	// Expect* durations gate Conditional rules: if zero, the rule downgrades to info/NA.
	ExpectManifestCadence time.Duration
	ExpectDefinitionCycle time.Duration
	ExpectHeartbeat       time.Duration
	ExpectSnapshotCycle   time.Duration

	// SourceRegistry authorises TOB source IDs. May be nil (all allowed).
	SourceRegistry SourceRegistry
}

// Configured reports whether the Conditional rule identified by ruleID has its
// corresponding --expect-* flag set. It is used by Emit to decide whether to
// downgrade the finding severity.
//
// Mapping:
//
//	REFDATA.MANIFEST_CADENCE        → ExpectManifestCadence
//	REFDATA.DEFINITION_CYCLE_COVERAGE → ExpectDefinitionCycle
//	REFDATA.NEVER_REACHES_READY     → ExpectManifestCadence + ExpectDefinitionCycle
//	REFDATA.NO_BURST_DEFINITIONS    → ExpectDefinitionCycle
//	HEARTBEAT.CADENCE               → ExpectHeartbeat
func (c Config) Configured(ruleID string) bool {
	switch ruleID {
	case "REFDATA.MANIFEST_CADENCE":
		return c.ExpectManifestCadence > 0
	case "REFDATA.DEFINITION_CYCLE_COVERAGE":
		return c.ExpectDefinitionCycle > 0
	case "REFDATA.NEVER_REACHES_READY":
		// Both, because the window this rule grades is their sum. Claiming the rule
		// is in scope on the cycle alone kept it at Must severity in a run where
		// decideNeverReachesReady can never measure anything -- a second silence
		// path of exactly the #50 shape, reached through the config instead.
		return c.ExpectManifestCadence > 0 && c.ExpectDefinitionCycle > 0
	case "REFDATA.NO_BURST_DEFINITIONS":
		return c.ExpectDefinitionCycle > 0
	case "HEARTBEAT.CADENCE":
		return c.ExpectHeartbeat > 0
	default:
		return false
	}
}
