package engine

// strike_test.go — the StrikeInterval (0x09) rules of the reference-data
// supplement: three structural rules decided from one datagram, and two
// continuity rules that compare the definitions of one instrument over time.

import (
	"slices"
	"strings"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// StrikeInterval layout (40 bytes total = 4-byte header + 36-byte body):
//
//	Body[0:4]   = Instrument ID (u32 LE)
//	Body[4:6]   = Source ID (u16 LE)
//	Body[6]     = Strike Exponent (i8)
//	Body[7]     = Bound Flags (u8)
//	Body[8:16]  = Lower Bound (i64 LE)
//	Body[16:24] = Upper Bound (i64 LE)
//	Body[24:32] = Fixing Time (u64 LE)
//	Body[32:36] = Reserved
func strikeBody(instrID uint32, flags uint8, lower, upper int64, fixingTime uint64) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID)    // Instrument ID (body off 0)
		b.U16(1)          // Source ID (body off 4)
		b.U8(0xFE)        // Strike Exponent -2 (body off 6)
		b.U8(flags)       // Bound Flags (body off 7)
		b.I64(lower)      // Lower Bound (body off 8)
		b.I64(upper)      // Upper Bound (body off 16)
		b.U64(fixingTime) // Fixing Time (body off 24)
		b.Pad(4)          // Reserved (body off 32) → total body 36 → msg 40
	}
}

// The three strikes the continuity tests use.
var (
	strikePendingBody = func(id uint32) func(*wb.Body) { return strikeBody(id, 0x00, 0, 0, 0) }
	strikeFixedBody   = func(id uint32) func(*wb.Body) { return strikeBody(id, 0x03, 8394517, 0, 1_790_000_000_000_000_000) }
)

// strikePairTOB builds one refdata datagram: a TOB InstrumentDefinition and the
// given StrikeInterval immediately after it.
func strikePairTOB(instrID uint32, manifestSeq uint16, strike func(*wb.Body)) []byte {
	return wb.Frame(wire.MagicTOB).
		Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(instrID, manifestSeq)).
		Msg(wire.TypeStrikeInterval, 40, strike).Bytes()
}

// statuses returns the statuses a rule reported for one instrument, in order.
func statuses(findings []core.Finding, ruleID string, instrID uint32) []core.Status {
	var out []core.Status
	for _, f := range findings {
		if f.RuleID == ruleID && f.InstrumentID == instrID {
			out = append(out, f.Status)
		}
	}
	return out
}

func wantStatuses(t *testing.T, findings []core.Finding, ruleID string, instrID uint32, want ...core.Status) {
	t.Helper()
	if got := statuses(findings, ruleID, instrID); !slices.Equal(got, want) {
		t.Errorf("%s instrument=%d: statuses %v, want %v", ruleID, instrID, got, want)
	}
}

// --- structural rules ---

func TestStrikeFollowsDefinition(t *testing.T) {
	const rule = "STRIKE.FOLLOWS_DEFINITION"
	fixed := strikeBody(100, 0x03, 8394517, 0, 0)
	for _, c := range []struct {
		name string
		feed core.Feed
		raw  []byte
		want bool
	}{
		{"pair", core.FeedTOB, strikePairTOB(100, 1, fixed), false},
		{"two pairs in one datagram", core.FeedTOB, wb.Frame(wire.MagicTOB).
			Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
			Msg(wire.TypeStrikeInterval, 40, fixed).
			Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(200, 1)).
			Msg(wire.TypeStrikeInterval, 40, strikeBody(200, 0x00, 0, 0, 0)).Bytes(), false},
		{"first message", core.FeedTOB, wb.Frame(wire.MagicTOB).
			Msg(wire.TypeStrikeInterval, 40, fixed).Bytes(), true},
		{"before its definition", core.FeedTOB, wb.Frame(wire.MagicTOB).
			Msg(wire.TypeStrikeInterval, 40, fixed).
			Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).Bytes(), true},
		{"after a ManifestSummary", core.FeedTOB, wb.Frame(wire.MagicTOB).
			Msg(wire.TypeManifest, 24, manifestBody(1, 1, 1)).
			Msg(wire.TypeStrikeInterval, 40, fixed).Bytes(), true},
		{"a second StrikeInterval for one definition", core.FeedTOB, wb.Frame(wire.MagicTOB).
			Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
			Msg(wire.TypeStrikeInterval, 40, fixed).
			Msg(wire.TypeStrikeInterval, 40, fixed).Bytes(), true},
		{"definition of a different instrument", core.FeedTOB, strikePairTOB(200, 1, fixed), true},
		// The same pair on the other feed that lists 0x09.
		{"mbp pair", core.FeedMBP, wb.Frame(wire.MagicMBP).
			Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
			Msg(wire.TypeStrikeInterval, 40, fixed).Bytes(), false},
		{"mbp first message", core.FeedMBP, wb.Frame(wire.MagicMBP).
			Msg(wire.TypeStrikeInterval, 40, fixed).Bytes(), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := fires(t, c.feed, MagicFor(c.feed), c.raw, core.PortRefData, rule); got != c.want {
				t.Errorf("%s fired=%v, want %v", rule, got, c.want)
			}
		})
	}
}

func TestStrikeFields(t *testing.T) {
	const rule = "STRIKE.FIELDS"
	reserved := func(b *wb.Body) {
		b.U32(100).U16(1).U8(0xFE).U8(0x03).I64(8394517).I64(0).U64(0).U32(1) // reserved bytes non-zero
	}
	for _, c := range []struct {
		name   string
		strike func(*wb.Body)
		want   bool
	}{
		{"greater", strikeBody(100, 0x01, 8394517, 0, 0), false},
		{"greater or equal", strikeBody(100, 0x03, 8394517, 0, 0), false},
		{"less", strikeBody(100, 0x04, 0, 7420000, 0), false},
		{"less or equal", strikeBody(100, 0x0C, 0, 7420000, 0), false},
		{"between", strikeBody(100, 0x0F, 9270000, 9279999, 0), false},
		{"pending", strikeBody(100, 0x00, 0, 0, 0), false},
		{"fixed with a fixing time", strikeBody(100, 0x03, 8394517, 0, 1_790_000_000_000_000_000), false},
		// A bound of 0 that is present is a value, not an absent bound.
		{"present lower bound of zero", strikeBody(100, 0x03, 0, 0, 0), false},
		{"reserved flag bit", strikeBody(100, 0x13, 8394517, 0, 0), true},
		{"lower inclusive, not present", strikeBody(100, 0x06, 0, 7420000, 0), true},
		{"upper inclusive, not present", strikeBody(100, 0x09, 8394517, 0, 0), true},
		{"absent lower bound non-zero", strikeBody(100, 0x04, 5, 7420000, 0), true},
		{"absent upper bound non-zero", strikeBody(100, 0x03, 8394517, 5, 0), true},
		{"pending with a bound", strikeBody(100, 0x00, 8394517, 0, 0), true},
		{"pending with a fixing time", strikeBody(100, 0x00, 0, 0, 1_790_000_000_000_000_000), true},
		{"reserved bytes", reserved, true},
		// The definition built by instrDefTOBBody carries Source ID 1.
		{"source id differs from the definition", func(b *wb.Body) {
			b.U32(100).U16(2).U8(0xFE).U8(0x03).I64(8394517).I64(0).U64(0).Pad(4)
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := strikePairTOB(100, 1, c.strike)
			if got := fires(t, core.FeedTOB, wire.MagicTOB, raw, core.PortRefData, rule); got != c.want {
				t.Errorf("%s fired=%v, want %v", rule, got, c.want)
			}
		})
	}
}

func TestStrikeIntervalNotEmpty(t *testing.T) {
	const rule = "STRIKE.INTERVAL_NOT_EMPTY"
	for _, c := range []struct {
		name         string
		flags        uint8
		lower, upper int64
		want         bool
	}{
		{"range", 0x0F, 9270000, 9279999, false},
		{"open range", 0x05, 9270000, 9279999, false},
		{"point, both ends inclusive", 0x0F, 9, 9, false},
		{"negative range", 0x0F, -500, -100, false},
		{"lower above upper", 0x0F, 9279999, 9270000, true},
		{"point, lower end exclusive", 0x0D, 9, 9, true},
		{"point, upper end exclusive", 0x07, 9, 9, true},
		{"point, both ends exclusive", 0x05, 9, 9, true},
		// One bound only: there is no second bound to compare, whatever its bytes say.
		{"lower bound only", 0x03, 9279999, 0, false},
		{"upper bound only", 0x0C, 0, -100, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := strikePairTOB(100, 1, strikeBody(100, c.flags, c.lower, c.upper, 0))
			if got := fires(t, core.FeedTOB, wire.MagicTOB, raw, core.PortRefData, rule); got != c.want {
				t.Errorf("%s fired=%v, want %v", rule, got, c.want)
			}
		})
	}
}

// A StrikeInterval is a known type with a fixed length and one port, so the
// shared structural rules cover it like any other message.
func TestStrikeSharedStructuralRules(t *testing.T) {
	fixed := strikeBody(100, 0x03, 8394517, 0, 0)
	short := wb.Frame(wire.MagicTOB).
		Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
		Msg(wire.TypeStrikeInterval, 36, func(b *wb.Body) { b.Pad(32) }).Bytes()
	if !fires(t, core.FeedTOB, wire.MagicTOB, short, core.PortRefData, "MSG.LENGTH_PER_TYPE") {
		t.Error("MSG.LENGTH_PER_TYPE did not fire on a 36-byte StrikeInterval")
	}
	// The body of a short message is not read: its zero bytes are not field values.
	for _, rule := range []string{"STRIKE.FIELDS", "STRIKE.INTERVAL_NOT_EMPTY", "STRIKE.FOLLOWS_DEFINITION"} {
		if fires(t, core.FeedTOB, wire.MagicTOB, short, core.PortRefData, rule) {
			t.Errorf("%s fired on a StrikeInterval whose length is wrong", rule)
		}
	}

	pair := strikePairTOB(100, 1, fixed)
	if fires(t, core.FeedTOB, wire.MagicTOB, pair, core.PortRefData, "MSG.LENGTH_PER_TYPE") {
		t.Error("MSG.LENGTH_PER_TYPE fired on a 40-byte StrikeInterval")
	}
	if fires(t, core.FeedTOB, wire.MagicTOB, pair, core.PortRefData, "MSG.WRONG_PORT_PLACEMENT") {
		t.Error("MSG.WRONG_PORT_PLACEMENT fired on a StrikeInterval on refdata")
	}
	onMktdata := wb.Frame(wire.MagicTOB).Msg(wire.TypeStrikeInterval, 40, fixed).Bytes()
	if !fires(t, core.FeedTOB, wire.MagicTOB, onMktdata, core.PortMktData, "MSG.WRONG_PORT_PLACEMENT") {
		t.Error("MSG.WRONG_PORT_PLACEMENT did not fire on a StrikeInterval on mktdata")
	}
}

// A feed whose spec does not list 0x09 skips the message as an unknown type, and
// no STRIKE rule judges it. The registry must agree with the engine on which
// feeds those are, or a finding has no rule_info row.
func TestStrikeFeedsMatchTheEngine(t *testing.T) {
	var strikeRules []core.RuleMeta
	for _, r := range core.Rules {
		if strings.HasPrefix(r.ID, "STRIKE.") {
			strikeRules = append(strikeRules, r)
		}
	}
	if len(strikeRules) == 0 {
		t.Fatal("no STRIKE.* rule in the registry: this test would check nothing")
	}
	for _, feed := range []core.Feed{core.FeedTOB, core.FeedMidpoint, core.FeedMBO, core.FeedMBP} {
		carries := carriesStrikeInterval(feed, wire.DefaultSchema(MagicFor(feed)))
		for _, r := range strikeRules {
			if slices.Contains(r.Feeds, feed) != carries {
				t.Errorf("%s: registry lists feed %s = %v, engine carries 0x09 = %v",
					r.ID, feed, slices.Contains(r.Feeds, feed), carries)
			}
		}
		if got := knownTypes(feed)[wire.TypeStrikeInterval]; got != carries {
			t.Errorf("feed %s: knownTypes has 0x09 = %v, carriesStrikeInterval = %v", feed, got, carries)
		}
		if got := portAllowed(feed, wire.TypeStrikeInterval, core.PortRefData); got != carries {
			t.Errorf("feed %s: 0x09 allowed on refdata = %v, carriesStrikeInterval = %v", feed, got, carries)
		}
		if got := expectedMsgLen(feed, wire.DefaultSchema(MagicFor(feed)), wire.TypeStrikeInterval) == 40; got != carries {
			t.Errorf("feed %s: 0x09 has a canonical length = %v, carriesStrikeInterval = %v", feed, got, carries)
		}
	}

	// On market-by-order the message is unknown: skipped, reported, never judged.
	raw := wb.Frame(wire.MagicMBO).Msg(wire.TypeStrikeInterval, 40, strikeBody(100, 0x02, 0, 0, 0)).Bytes()
	f, sf := wire.Decode(raw, wire.MagicMBO)
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO}, ac)
	e.Process(srcA, f, core.PortRefData, sf)
	e.Flush()
	if len(findingsFor(ac, "MSG.UNKNOWN_TYPE_SKIPPED")) == 0 {
		t.Error("market-by-order did not report 0x09 as an unknown type")
	}
	for _, fn := range ac.findings {
		if strings.HasPrefix(fn.RuleID, "STRIKE.") {
			t.Errorf("market-by-order reported %s, which does not apply to it", fn.RuleID)
		}
	}
}

// The type exists only at the schema whose spec lists it. This tool also decodes
// schema 1, and no 1.x release lists 0x09: there it is an unknown type.
func TestStrikeIsUnknownAtSchema1(t *testing.T) {
	raw := wb.Frame(wire.MagicTOB).Schema(1).
		Msg(wire.TypeStrikeInterval, 40, strikeBody(100, 0x02, 0, 0, 0)).Bytes()
	f, sf := wire.Decode(raw, wire.MagicTOB)
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB}, ac)
	e.Process(srcA, f, core.PortRefData, sf)
	e.Flush()
	if len(findingsFor(ac, "MSG.UNKNOWN_TYPE_SKIPPED")) == 0 {
		t.Error("top-of-book schema 1 did not report 0x09 as an unknown type")
	}
	for _, fn := range ac.findings {
		if strings.HasPrefix(fn.RuleID, "STRIKE.") {
			t.Errorf("top-of-book schema 1 reported %s, which its spec version does not define", fn.RuleID)
		}
	}
}

// --- continuity rules ---

const (
	rulePresence   = "STRIKE.PRESENCE_STABLE"
	ruleStaysFixed = "STRIKE.FIXED_STAYS_FIXED"
)

// reasons returns the reasons a rule reported for one instrument, in order.
func reasons(findings []core.Finding, ruleID string, instrID uint32) []string {
	var out []string
	for _, f := range findings {
		if f.RuleID == ruleID && f.InstrumentID == instrID {
			out = append(out, f.Reason)
		}
	}
	return out
}

func wantReasons(t *testing.T, findings []core.Finding, ruleID string, instrID uint32, want ...string) {
	t.Helper()
	if got := reasons(findings, ruleID, instrID); !slices.Equal(got, want) {
		t.Errorf("%s instrument=%d: reasons %q, want %q", ruleID, instrID, got, want)
	}
}

// The lifecycle the message exists for: listed with a pending strike, then fixed
// when the window opens, then retransmitted. Instrument 200 has no strike.
func TestStrikePendingThenFixed(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 2),
		strikePairTOB(100, 1, strikePendingBody(100)),
		buildInstrDefFrameTOB(200, 1),
		strikePairTOB(100, 1, strikePendingBody(100)),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		buildInstrDefFrameTOB(200, 1),
	})
	for _, f := range findings {
		if f.Status == core.Violation {
			t.Errorf("unexpected violation on a conformant strike lifecycle: %s: %s", f.RuleID, f.Detail)
		}
	}
	// One finding per rule per accepted definition.
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Pass, core.Pass, core.Pass)
	wantReasons(t, findings, rulePresence, 100, core.ReasonColdStart, "", "", "")
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.NA, core.NA, core.Pass)
	// An instrument with no strike passes too: its definitions agree with each other.
	wantStatuses(t, findings, rulePresence, 200, core.Unverifiable, core.Pass)
	wantStatuses(t, findings, ruleStaysFixed, 200, core.NA, core.NA)
}

// A change is reported once, at the change. The definitions after it agree with
// each other, so a persistent drop is one violation and not one per cycle.
func TestStrikeDropped(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		buildInstrDefFrameTOB(100, 1), // the strike is gone
		buildInstrDefFrameTOB(100, 1),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Pass, core.Violation, core.Pass)
	// The fixed-strike rule does not name the same defect a second time.
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.Pass, core.Unverifiable, core.NA)
	wantReasons(t, findings, ruleStaysFixed, 100, "", "", core.ReasonSuperseded, "")
}

// The supplement requires the StrikeInterval from the first definition of the
// instrument. A strike that appears on a later definition is the other direction
// of the same rule.
func TestStrikeAddedLate(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		buildInstrDefFrameTOB(100, 1),
		buildInstrDefFrameTOB(100, 1),
		strikePairTOB(100, 1, strikePendingBody(100)), // a strike appears
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Pass, core.Violation)
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.NA, core.NA)
}

func TestStrikeReturnsToPending(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		strikePairTOB(100, 1, strikePendingBody(100)), // fixed, then pending
		strikePairTOB(100, 1, strikePendingBody(100)),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		strikePairTOB(100, 1, strikeFixedBody(100)),
	})
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.Violation, core.NA, core.NA, core.Pass)
	// The message is still there, so the other rule passes.
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Pass, core.Pass, core.Pass, core.Pass)
}

// The supplement lets a venue correct a fixed strike: the last message is the
// current value. A different fixed value is not a violation of either rule.
func TestStrikeCorrectedValueIsNotAViolation(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeBody(100, 0x03, 8394517, 0, 0)),
		strikePairTOB(100, 1, strikeBody(100, 0x03, 8394600, 0, 0)),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Pass)
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.Pass)
}

// A StrikeInterval that a structural rule already rejected is not read as
// "no strike": one defect, one violation.
func TestStrikeRejectedMessageIsNotADrop(t *testing.T) {
	wrongID := wb.Frame(wire.MagicTOB).
		Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
		Msg(wire.TypeStrikeInterval, 40, strikeFixedBody(999)).Bytes()
	short := wb.Frame(wire.MagicTOB).
		Msg(wire.TypeInstrumentDef, 130, instrDefTOBBody(100, 1)).
		Msg(wire.TypeStrikeInterval, 36, func(b *wb.Body) { b.Pad(32) }).Bytes()
	// The same message is rejected by STRIKE.FIELDS or STRIKE.INTERVAL_NOT_EMPTY.
	// A pending strike here would otherwise fail STRIKE.FIXED_STAYS_FIXED as well.
	badFields := strikePairTOB(100, 1, strikeBody(100, 0x02, 0, 0, 0))
	emptyRange := strikePairTOB(100, 1, strikeBody(100, 0x0F, 9279999, 9270000, 0))
	otherSource := strikePairTOB(100, 1, func(b *wb.Body) {
		b.U32(100).U16(2).U8(0xFE).U8(0x03).I64(8394517).I64(0).U64(0).Pad(4)
	})
	for name, bad := range map[string][]byte{
		"different instrument id": wrongID,
		"wrong length":            short,
		"bound flags defect":      badFields,
		"empty interval":          emptyRange,
		"source id differs":       otherSource,
	} {
		t.Run(name, func(t *testing.T) {
			findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
				buildManifestFrame(wire.MagicTOB, 1, 1, 1),
				strikePairTOB(100, 1, strikeFixedBody(100)),
				bad,
				strikePairTOB(100, 1, strikeFixedBody(100)),
			})
			wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Unverifiable, core.Pass)
			wantReasons(t, findings, rulePresence, 100, core.ReasonColdStart, core.ReasonSuperseded, "")
			wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.Unverifiable, core.Pass)
		})
	}
}

// A publisher that drops a strike at the same moment as a set change still
// dropped it: the instrument is in both sets.
func TestStrikeDroppedAcrossManifestSeqChange(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		buildManifestFrame(wire.MagicTOB, 1, 2, 2),
		buildInstrDefFrameTOB(100, 2), // same instrument, next seq, no strike
		buildInstrDefFrameTOB(200, 2),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Violation)
}

// An instrument that leaves a set that completes takes its strike history with
// it, so a later instrument that reuses the Instrument ID starts as new.
func TestStrikeForgottenWhenInstrumentLeavesTheSet(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 2),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		buildInstrDefFrameTOB(200, 1),
		// Instrument 100 leaves: the set is 200 alone, and it completes.
		buildManifestFrame(wire.MagicTOB, 1, 2, 1),
		buildInstrDefFrameTOB(200, 2),
		// Instrument ID 100 returns as a different instrument, with no strike.
		buildManifestFrame(wire.MagicTOB, 1, 3, 2),
		buildInstrDefFrameTOB(200, 3),
		buildInstrDefFrameTOB(100, 3),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Unverifiable)
	wantReasons(t, findings, rulePresence, 100, core.ReasonColdStart, core.ReasonColdStart)
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.NA)
}

// The same departure, but the set without the instrument never completes, so
// nothing saw the instrument leave. The history is two Manifest Seq values old,
// and it is not held against the definition.
func TestStrikeNotComparedAcrossASetThatNeverCompleted(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		// Seq 2 declares two instruments and delivers one: it never completes.
		buildManifestFrame(wire.MagicTOB, 1, 2, 2),
		buildInstrDefFrameTOB(200, 2),
		// Seq 3: Instrument ID 100 is back, with no strike.
		buildManifestFrame(wire.MagicTOB, 1, 3, 2),
		buildInstrDefFrameTOB(200, 3),
		buildInstrDefFrameTOB(100, 3),
		buildInstrDefFrameTOB(100, 3),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Unverifiable, core.Pass)
	wantReasons(t, findings, rulePresence, 100, core.ReasonColdStart, core.ReasonTransition, "")
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.Unverifiable, core.NA)
	wantReasons(t, findings, ruleStaysFixed, 100, "", core.ReasonTransition, "")
}

// A reset starts a new era. Nothing from the era before it binds the publisher.
func TestStrikeForgottenOnReset(t *testing.T) {
	reset := func(raw []byte) []byte { raw[21] = 1; return raw } // Reset Count, datagram header offset 21
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)),
		reset(buildManifestFrame(wire.MagicTOB, 1, 1, 1)),
		reset(buildInstrDefFrameTOB(100, 1)),
	})
	wantStatuses(t, findings, rulePresence, 100, core.Unverifiable, core.Unverifiable)
	wantReasons(t, findings, rulePresence, 100, core.ReasonColdStart, core.ReasonColdStart)
	wantStatuses(t, findings, ruleStaysFixed, 100, core.NA, core.NA)
}

// A definition that the subscriber algorithm discards is not an opportunity:
// the channel is not valid, or the definition carries a stale Manifest Seq.
func TestStrikeDiscardedDefinitionIsNotJudged(t *testing.T) {
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, [][]byte{
		strikePairTOB(100, 1, strikeFixedBody(100)), // before any ManifestSummary
		buildManifestFrame(wire.MagicTOB, 1, 2, 1),
		strikePairTOB(100, 1, strikeFixedBody(100)), // stale seq
	})
	for _, rule := range []string{rulePresence, ruleStaysFixed} {
		if got := statuses(findings, rule, 100); len(got) != 0 {
			t.Errorf("%s reported %v for definitions that were discarded", rule, got)
		}
	}
}

// A gap between the two definitions can hide the removal of the instrument and
// the reuse of its ID, so a drop seen across a gap is not charged to the publisher.
func TestStrikeDroppedAcrossAGapIsUnverifiable(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)
	for _, d := range []struct {
		seq uint64
		raw []byte
	}{
		{1, buildManifestFrame(wire.MagicTOB, 1, 1, 1)},
		{2, strikePairTOB(100, 1, strikeFixedBody(100))},
		// Sequence numbers 3 to 9 never arrive.
		{10, buildInstrDefFrameTOB(100, 1)},
		{11, buildManifestFrame(wire.MagicTOB, 1, 1, 1)},
	} {
		f, sf := wire.Decode(d.raw, wire.MagicTOB)
		f.Header.Sequence = d.seq
		e.Process(srcA, f, core.PortRefData, sf)
	}
	e.Flush()
	wantStatuses(t, ac.findings, rulePresence, 100, core.Unverifiable, core.Unverifiable)
	wantReasons(t, ac.findings, rulePresence, 100, core.ReasonColdStart, core.ReasonLoss)
}

// Equal ends across a gap do not prove a pass: the lost datagrams can hold a drop
// and its reversal. The comparisons after the gap do not span it, so they pass
// again. The window stays dirty for the era; the rule does not stay blind.
func TestStrikeEqualEndsAcrossAGapAreNotAPass(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, ac)
	for _, d := range []struct {
		seq uint64
		raw []byte
	}{
		{1, buildManifestFrame(wire.MagicTOB, 1, 1, 1)},
		{2, strikePairTOB(100, 1, strikeFixedBody(100))},
		// Sequence numbers 3 to 9 never arrive.
		{10, strikePairTOB(100, 1, strikeFixedBody(100))},
		{11, strikePairTOB(100, 1, strikeFixedBody(100))},
		{12, buildManifestFrame(wire.MagicTOB, 1, 1, 1)},
	} {
		f, sf := wire.Decode(d.raw, wire.MagicTOB)
		f.Header.Sequence = d.seq
		e.Process(srcA, f, core.PortRefData, sf)
	}
	e.Flush()
	wantStatuses(t, ac.findings, rulePresence, 100, core.Unverifiable, core.Unverifiable, core.Pass)
	wantReasons(t, ac.findings, rulePresence, 100, core.ReasonColdStart, core.ReasonLoss, "")
	wantStatuses(t, ac.findings, ruleStaysFixed, 100, core.NA, core.Unverifiable, core.Pass)
	wantReasons(t, ac.findings, ruleStaysFixed, 100, "", core.ReasonLoss, "")
}
