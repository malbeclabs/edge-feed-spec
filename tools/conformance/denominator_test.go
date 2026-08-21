package main

// denominator_test.go — enforcement for the invariant described in
// engine/denominator.go.
//
// A rule whose execution is conditional can fail in a way no assertion in this
// repo used to catch: it stops running. Every "expected zero violations" test
// still passes, the exit code stays 0, and the tool reports a clean feed it never
// actually checked. The market-by-price reconstruction oracle spent a revision in
// exactly that state — comparing 5 of 38 snapshot groups, with the other 33 dropped
// silently — and the way out is to make each such rule account for every
// opportunity it gets. This test is what keeps them accounting.
//
// It asserts the weakest useful property: on a capture that exercises the feed,
// every conditional-execution rule applicable to it emitted *something*. Exact
// numbers belong with the fixture that produces them (see
// engine.TestNonconformantMBPCapture); what belongs here is the floor, because a
// rule that has gone silent is a rule whose coverage is unknowable and no amount of
// pinning elsewhere reveals it.

import (
	"path/filepath"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/input"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// noOpportunity names the (capture, rule) pairs where a zero is correct because the
// capture contains no message the rule could judge — not because the rule went
// quiet. Each entry states the missing trigger, and is a claim about the capture
// that has to be rechecked when the capture changes.
//
// Keep this list at zero entries where you can. An exemption is the one way this
// test can be defeated, so a rule belongs here only when its trigger is provably
// absent, never because the assertion is inconvenient.
//
// Every entry below is a consequence of what buildMBOGoldenEntries does *not*
// contain — it emits Manifest, InstrumentDefinition, OrderAdd, OrderCancel,
// Heartbeat and two snapshot groups. So these are one gap in the capture rather than
// four independent excuses, and adding an InstrumentReset with its recovery snapshot
// plus one OrderExecute would empty the map.
var noOpportunity = map[string]string{
	// No OrderExecute, so nothing ever carries an execution price. Its sibling
	// FIELD.ORDERADD_PRICE_BOUND shares every line of the gate and is asserted below,
	// so the path itself is covered.
	"mbo/REF.EXEC_PRICE_BOUND": "the capture contains no OrderExecute",
	// No InstrumentReset, so no instrument ever awaits recovery and none of the three
	// reset rules has anything to judge. Their pass arms are asserted directly in
	// engine.TestResetHappyPath, which drives a real reset.
	"mbo/RESET.SNAPSHOT_FOLLOWS":                       "the capture contains no InstrumentReset",
	"mbo/RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET": "the capture contains no InstrumentReset",
	"mbo/RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR":  "the capture contains no InstrumentReset",
}

func TestConditionalRulesReportADenominator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		feed  core.Feed
		magic uint16
		// entries builds a synthetic conformant capture; pcap names a committed one.
		// Both drive the same engine path.
		entries func() []goldenPcapEntry
		pcap    string
		ports   map[int]core.Port
	}{
		{name: "mbo", feed: core.FeedMBO, magic: wire.MagicMBO, entries: buildMBOGoldenEntries},
		{name: "tob", feed: core.FeedTOB, magic: wire.MagicTOB, entries: buildTOBGoldenEntries},
		{name: "midpoint", feed: core.FeedMidpoint, magic: wire.MagicMid, entries: buildMidpointGoldenEntries},
		{
			// The market-by-price capture is real venue data rather than frames built
			// here, which matters for this assertion more than for most: the oracle's
			// gates are all about the two ports draining at different rates, and
			// hand-built entries arrive in an order that makes them trivially satisfiable.
			name: "mbp", feed: core.FeedMBP, magic: wire.MagicMBP,
			pcap: filepath.Join("testdata", "nonconformant_mbp.pcap"),
			ports: map[int]core.Port{
				31000: core.PortMktData, 41000: core.PortRefData, 51000: core.PortSnapshot,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := &goldenCapture{}
			if tc.pcap != "" {
				replayPcapInto(t, gc, tc.feed, tc.magic, tc.pcap, tc.ports)
			} else {
				replayEntriesInto(t, gc, tc.feed, tc.magic, tc.entries())
			}

			seen := map[string]int{}
			for _, f := range gc.findings {
				seen[f.RuleID]++
				// `reason` is a Prometheus label on a per-finding counter, so an ad-hoc
				// string here is unbounded cardinality on a live feed — the one cost the
				// denominator work could impose if it went unchecked.
				if !core.ValidReason(f.Reason) {
					t.Errorf("%s emitted reason %q, which is not in core's closed vocabulary; "+
						"it would become an unbounded unverifiable_total label", f.RuleID, f.Reason)
				}
			}

			for _, rule := range core.ConditionalExecRules(tc.feed) {
				meta, _ := core.Lookup(rule)
				if meta.Conditional {
					// Off until its --expect-* flag is set, and these runs set none. That is a
					// static fact the operator reads from rule_info, not a silent skip — the
					// distinction engine/denominator.go draws. Covered instead by
					// engine.TestDefinitionCycleCoverageComplete and its neighbours, which
					// configure the flag.
					continue
				}
				if why, exempt := noOpportunity[tc.name+"/"+rule]; exempt {
					if seen[rule] > 0 {
						t.Errorf("%s reported %d finding(s) but is listed as having no opportunity (%s); "+
							"the exemption is stale — drop it", rule, seen[rule], why)
					}
					continue
				}
				if seen[rule] == 0 {
					t.Errorf("%s reported nothing over the %s capture: its execution is conditional, "+
						"so silence is indistinguishable from having checked everything. Either it "+
						"stopped running (a regression) or it needs a pass/unverifiable arm "+
						"(see engine/denominator.go).", rule, tc.name)
				}
			}
		})
	}
}

// replayEntriesInto runs the engine over synthetic capture entries, recording every
// finding into gc. Unlike runGoldenCapture it keeps the findings rather than
// reducing them to a must-violation count.
func replayEntriesInto(t *testing.T, gc *goldenCapture, feed core.Feed, magic uint16, entries []goldenPcapEntry) {
	t.Helper()
	eng := engine.New(goldenEngineConfig(feed), gc)
	for _, e := range entries {
		port, ok := goldenPortMap[int(e.dstPort)]
		if !ok {
			continue
		}
		f, sf := wire.Decode(e.payload, magic)
		eng.Process(testSrc, f, port, sf)
	}
	eng.Flush()
	eng.EndRun()
}

// replayPcapInto is replayEntriesInto for a committed capture file.
func replayPcapInto(t *testing.T, gc *goldenCapture, feed core.Feed, magic uint16, path string, ports map[int]core.Port) {
	t.Helper()
	src, err := input.NewPcapSource(path, ports)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()

	eng := engine.New(goldenEngineConfig(feed), gc)
	for {
		dg, ok, err := src.Next()
		if err != nil || !ok {
			break
		}
		f, sf := wire.Decode(dg.Raw, magic)
		eng.Process(dg.Src, f, dg.Port, sf)
	}
	eng.Flush()
	eng.EndRun()
}

// The denominator invariant has one hole it cannot close from inside the engine: a
// rule whose only driver is a snapshot-port frame reports nothing at all when
// --snapshot-port is unset, because the emitting code is never entered. That is not
// a rule going quiet, it is a rule with no input — and it still reads as a pass.
//
// This test pins the two halves of the fix: that core's snapshotDriven set names
// exactly the rules which disappear without the port, and that the CLI accounts for
// each of them as NA when it is unset.
// incidentalToSnapshotPort names rules that vanish with --snapshot-port removed for a
// reason that has nothing to do with being snapshot-driven: they apply to every port,
// and it is only *this capture* that happens to carry their violating frames on that
// one. They must stay out of core's snapshotDriven set — reporting them starved would
// tell an operator a rule cannot run when it runs on every frame the tool sees.
var incidentalToSnapshotPort = map[string]bool{
	// nonconformant_mbp.pcap's oversized frames are all on the snapshot port. The rule
	// is a Tier-1 structural check that fires on any port.
	"FRAME.LENGTH_CONSISTENCY": true,
}

func TestStarvedRulesAreExactlyTheSnapshotDrivenOnes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		feed  core.Feed
		magic uint16
		pcap  string
		ports map[int]core.Port
	}{
		{
			name: "mbp", feed: core.FeedMBP, magic: wire.MagicMBP,
			pcap: filepath.Join("testdata", "nonconformant_mbp.pcap"),
			ports: map[int]core.Port{
				31000: core.PortMktData, 41000: core.PortRefData, 51000: core.PortSnapshot,
			},
		},
		{
			name: "mbo", feed: core.FeedMBO, magic: wire.MagicMBO,
			pcap: filepath.Join("testdata", "conformant_mbo.pcap"),
			ports: map[int]core.Port{
				18001: core.PortMktData, 18002: core.PortRefData, 18003: core.PortSnapshot,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same capture, twice: once with the snapshot port mapped, once without.
			withSnap := replayIDs(t, tc.feed, tc.magic, tc.pcap, tc.ports)
			noSnap := map[int]core.Port{}
			for p, lp := range tc.ports {
				if lp != core.PortSnapshot {
					noSnap[p] = lp
				}
			}
			without := replayIDs(t, tc.feed, tc.magic, tc.pcap, noSnap)

			driven := map[string]bool{}
			for _, id := range core.SnapshotDrivenRules(tc.feed) {
				driven[id] = true
			}

			// Every rule this capture loses with the port removed must be either listed as
			// snapshot-driven, or one of the incidental cases below.
			for id := range withSnap {
				if without[id] || driven[id] || incidentalToSnapshotPort[id] {
					continue
				}
				t.Errorf("%s disappears when --snapshot-port is dropped but is not listed as "+
					"snapshot-driven: either add it to core's snapshotDriven set or explain why "+
					"its absence is visible some other way", id)
			}
			// And nothing listed as snapshot-driven may still report without the port — that
			// would make the NA a lie.
			for id := range driven {
				if without[id] {
					t.Errorf("%s is listed as snapshot-driven but still reports without the port, "+
						"so reporting it starved would be wrong", id)
				}
			}
		})
	}
}

// replayIDs runs the engine over a capture with the given port map and returns the
// set of rule IDs that reported anything at all.
func replayIDs(t *testing.T, feed core.Feed, magic uint16, path string, ports map[int]core.Port) map[string]bool {
	t.Helper()
	gc := &goldenCapture{}
	replayPcapInto(t, gc, feed, magic, path, ports)
	out := map[string]bool{}
	for _, f := range gc.findings {
		out[f.RuleID] = true
	}
	return out
}
