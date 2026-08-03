package engine

import "github.com/malbeclabs/edge-feed-spec/tools/conformance/core"

// The denominator invariant.
//
// A rule whose *execution* is conditional — one that runs only when its
// preconditions line up — must account for every opportunity it gets, not just
// the ones it failed. Otherwise a rule at full coverage and a rule that never once
// ran produce the same output: nothing. That is not a cosmetic gap. The
// market-by-price reconstruction oracle compared 5 of 38 completed snapshot groups
// on the reference capture and the run reported clean, indistinguishable from a
// tool that had checked all 38 — the only witness to the other 33 was an unexported
// counter no reporter could see.
//
// So each such rule emits exactly one finding per opportunity:
//
//	pass          the check ran and the property held
//	violation     the check ran and the publisher broke it
//	unverifiable  the check could not run; Reason names what stopped it
//	na            the check does not apply to this opportunity at all
//
// `checks_total{rule_id}` is then the rule's denominator and its `result="pass"`
// series the coverage; `unverifiable_total{reason}` breaks the shortfall down by
// cause. `core.ConditionalExec` marks the rules that owe this, and
// `TestConditionalRulesReportADenominator` fails a run where one of them reported
// nothing at all.
//
// **Config-gated rules are a different axis.** A rule that is off because its
// `--expect-*` flag was not passed reports nothing, and should: `RuleMeta.Conditional`
// and the `rule_info` gauge already say so statically, before a frame arrives. This
// invariant is about stream state, not configuration.

// passed reports an opportunity the rule took, with the property holding. Logged at
// DEBUG — the value is in the counter, not the line.
func (e *Engine) passed(ruleID string, port core.Port, seq uint64, ch uint8, inst uint32, detail string) {
	e.Emit(ruleID, core.Pass, port, seq, ch, inst, detail)
}

// unverified reports an opportunity the rule could not take because the stream did
// not (yet) support a decision. reason must be one of core.Reason*: it names the
// cause on a bounded metric label, so "it didn't work" is not an answer.
func (e *Engine) unverified(ruleID, reason string, port core.Port, seq uint64, ch uint8, inst uint32, detail string) {
	e.Emit(ruleID, core.Unverifiable, port, seq, ch, inst, detail, reason)
}

// inapplicable reports an opportunity the rule does not apply to at all — the
// distinction from unverified being that no amount of further traffic would make
// this one decidable (the instrument declares no price bound, so there is nothing
// to bound-check).
func (e *Engine) inapplicable(ruleID string, port core.Port, seq uint64, ch uint8, inst uint32, detail string) {
	e.Emit(ruleID, core.NA, port, seq, ch, inst, detail)
}
