package engine

// refdata.go — Reference-data set-state machine (Task 14 + Task 15).
//
// Implements the subscriber algorithm from the edge-feed-spec reference-data
// supplement v0.1.0, plus 9 structural detector rules (Task 14) and 4 timing
// rules (Task 15):
//
//   - REFDATA.MANIFEST_CADENCE          (config-gated, ExpectManifestCadence)
//   - REFDATA.DEFINITION_CYCLE_COVERAGE (config-gated, ExpectDefinitionCycle)
//   - REFDATA.NO_BURST_DEFINITIONS      (config-gated, ExpectDefinitionCycle)
//   - REFDATA.NEVER_REACHES_READY       (config-gated, end-of-run, ExpectDefinitionCycle)
//
// Timing baseline: all timing checks use the frame-level SendTS field (uint64
// nanoseconds since epoch as written by the publisher).  This is deterministic
// in tests: callers set frame.Header.SendTS explicitly to control time without
// any real wall-clock dependency.
//
// State per channel (keyed by channelID):
//
//	valid            bool
//	latest_seq       u16
//	expected_count   u32
//	last_reset_count u8
//	defs             map[instrumentID]manifestSeq
//
// The modular u16 ordering for Manifest Seq is:
//
//	is_later(b, a) = ((b-a) mod 65536) != 0 && ((b-a) mod 65536) < 32768

import (
	"fmt"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// isLaterSeq returns true if b is "later than" a in modular u16 ordering.
// Equivalent to the spec's is_later(b, a):
//
//	return ((b - a) mod 65536) != 0 and ((b - a) mod 65536) < 32768
//
// This is the same wraparound-safe comparison used by TCP (RFC 1323).
func isLaterSeq(b, a uint16) bool {
	d := uint16(b - a) // wraps modulo 65536 in Go's uint16 arithmetic
	return d != 0 && d < 32768
}

// defInfo holds per-instrument metadata extracted from InstrumentDefinition.
// defaultMethod and priceBound are extracted from the feed-specific definition
// layout and consumed by the Midpoint validator (Task 17).
// Stale-seq detection uses the manifestSeq argument directly (not stored here).
type defInfo struct {
	defaultMethod uint8
	priceBound    uint8
}

// validZeroEvent is a ManifestSummary(Valid=0) seen with an established set, held
// until its verdict can be decided.
//
// A spec-mandated shutdown (supplement §4) and a mid-service Valid drop are the
// same message on this port; what tells them apart is EndOfSession, which every
// feed spec puts on mktdata. The two ports have independent reorder windows, so
// which is classified first is not a property of the publisher — deciding inline
// would be a race on the drain order.
type validZeroEvent struct {
	frameSeq uint64
	dirty    bool // the refdata window was gapped when the summary arrived
}

// channelRefdataState is the per-channel reference-data subscriber state.
type channelRefdataState struct {
	// subscriber algorithm state (verbatim from the supplement)
	valid         bool
	latestSeq     uint16
	expectedCount uint32
	// defs maps instrumentID → defInfo for the current epoch.
	defs map[uint32]defInfo

	// seqEverSet is true once latestSeq has been set from a valid summary.
	// Used to detect the first summary vs. subsequent ones (for regress check).
	seqEverSet bool

	// prevSummaryCount is the count field from the last valid ManifestSummary
	// at latestSeq. Used to detect COUNT_CHANGE_NO_SEQ_BUMP.
	prevSummaryCount uint32
	prevSummarySeq   uint16 // the seq at which prevSummaryCount was recorded
	prevSummarySet   bool   // true once any valid summary has been processed

	// setSnapshot holds the frozen set of instrument IDs from when ready() first
	// became true at the current latestSeq.  Used to detect SET_CHANGE_NO_SEQ_BUMP
	// on subsequent def retransmissions.
	setSnapshot    map[uint32]struct{}
	setSnapshotSeq uint16
	setSnapshotSet bool

	// hadNonEmptySet is true once we reached ready() at any point.  Used for
	// REFDATA.VALID_FLAG_WHILE_SERVING.
	hadNonEmptySet bool

	// pendingValidZero holds the undecided REFDATA.VALID_FLAG_WHILE_SERVING verdict.
	// At most one is outstanding: arming it requires hadNonEmptySet, which the same
	// Valid=0 clears, so the slot cannot be re-armed until the channel re-establishes.
	pendingValidZero *validZeroEvent

	// --- Task 15: timing/cadence state ---

	// lastManifestSendTS is the SendTS of the most recent ManifestSummary of any
	// kind, Valid=0 included: the MANIFEST_CADENCE anchor, and only that. The
	// cadence is measured between summaries, so it must not skip the ones a
	// publisher sends while invalid.
	lastManifestSendTS    uint64
	lastManifestSendTSSet bool

	// firstSendTS and lastServingSendTS bound the current serving period: the first
	// and most recent Valid=1 summary of it. NEVER_REACHES_READY measures its
	// observation window between them.
	//
	// lastServingSendTS is deliberately not lastManifestSendTS. Sharing one field
	// charges a serving period with the invalid stretch that follows it, so a
	// publisher that keeps its cadence while invalid turns a 2 s period into a
	// minute-long one and earns a violation on a must rule.
	firstSendTS          uint64
	firstSendTSSet       bool
	lastServingSendTS    uint64
	lastServingSendTSSet bool

	// everReady is true once channelReady has ever been true for this channel.
	// Used by NEVER_REACHES_READY.
	everReady bool

	// readySendTS is the wire SendTS of the datagram at which channelReady first
	// became true in the current serving period. NEVER_REACHES_READY needs the
	// moment, not just the fact: the rule asks whether ready was reached *within*
	// manifest cadence + definition cycle, and everReady alone cannot answer that.
	// Without it a channel that reaches ready long after the window still passes
	// whenever no Valid=1 summary lands between the window closing and readiness,
	// because nothing ever measures a span past the window.
	readySendTS    uint64
	readySendTSSet bool

	// neverReadyDecided is true once REFDATA.NEVER_REACHES_READY has reported this
	// serving period, so the verdict is emitted exactly once whether it was reached
	// mid-run or at end of run. Cleared wherever a period is: a reset, and the start
	// of the Valid=1 period that replaces one a Valid=0 closed. Deliberately NOT
	// cleared by the Valid=0 itself — that summary does not close the period (the
	// next one's start does), so clearing there would let the same period report
	// twice.
	neverReadyDecided bool

	// cycleStartSendTS is the SendTS of the ManifestSummary that opened the
	// current retransmission cycle. A cycle spans from one ManifestSummary to
	// the next (same or newer seq). Reset when a new cycle starts.
	cycleStartSendTS    uint64
	cycleStartSendTSSet bool // true once the tumbling cycle has been opened

	// defsSeenThisCycle tracks which instrument IDs have been transmitted at
	// least once in the current cycle (keyed by instruID). Used for
	// DEFINITION_CYCLE_COVERAGE.
	defsSeenThisCycle map[uint32]struct{}

	// defFramesSeen is the number of distinct frame SendTS values that carried
	// at least one InstrumentDefinition in this cycle. Used to detect a burst
	// (all defs in a single SendTS → burst, not paced).
	defFramesSeen     int
	prevDefFrameTS    uint64 // SendTS of the last def frame
	prevDefFrameTSSet bool
}

func newChannelRefdataState() *channelRefdataState {
	return &channelRefdataState{
		defs:              make(map[uint32]defInfo),
		defsSeenThisCycle: make(map[uint32]struct{}),
	}
}

// refdataState is the engine-level holder of per-channel refdata state.
type refdataState struct {
	e        *Engine
	channels map[uint8]*channelRefdataState
}

// newRefdataState creates a refdataState bound to the given engine.
func newRefdataState(e *Engine) *refdataState {
	return &refdataState{
		e:        e,
		channels: make(map[uint8]*channelRefdataState),
	}
}

func (rs *refdataState) channel(ch uint8) *channelRefdataState {
	s, ok := rs.channels[ch]
	if !ok {
		s = newChannelRefdataState()
		rs.channels[ch] = s
	}
	return s
}

// channelKnown returns two booleans: (ready, known).
//
//   - ready is true when the channel identified by ch has reached ready() state
//     (valid=1 and len(defs)==expectedCount).
//   - known is true when ready is true AND the instrument instrID is present in
//     the channel's current def set.
//
// If the channel has not been seen yet, both return false.
// Used by the TOB validator (tob.go) to gate TOB.QUOTE.REFDATA_KNOWN.
func (rs *refdataState) channelKnown(ch uint8, instrID uint32) (ready bool, known bool) {
	s, ok := rs.channels[ch]
	if !ok {
		return false, false
	}
	if !channelReady(s) {
		return false, false
	}
	_, inDefs := s.defs[instrID]
	return true, inDefs
}

// defInfoFor returns the defInfo for an instrument on a channel, plus ok=true
// when the channel is ready() and the instrument is in the def set.
// Used by the Midpoint validator to gate refdata-consumer rules.
func (rs *refdataState) defInfoFor(ch uint8, instrID uint32) (defInfo, bool) {
	s, ok := rs.channels[ch]
	if !ok || !channelReady(s) {
		return defInfo{}, false
	}
	di, ok := s.defs[instrID]
	return di, ok
}

// ready returns true if any channel's state satisfies ready().
// Exposed as a method on refdataState for Task 15 hooks.
func (rs *refdataState) ready() bool {
	// For unit tests the single-channel case is keyed by channelID 1.
	for _, s := range rs.channels {
		if channelReady(s) {
			return true
		}
	}
	return false
}

// channelReady is the per-channel readiness predicate, verbatim from the supplement:
//
//	ready() = valid AND len(defs) == expected_count
func channelReady(s *channelRefdataState) bool {
	return s.valid && uint32(len(s.defs)) == s.expectedCount
}

// onResetChannel handles a Reset Count change on ONE channel instance. That
// channel's state is discarded per the supplement: "subscribers detect the reset
// by comparing Reset Count against their last-seen value and discard all cached
// state."
//
// **Only that channel.** Reset Count belongs to the channel instance, not to the
// port: two publishers serving one group and one port advance independent Reset
// Counts, so a port-wide reset lets either path erase the other's established set
// on every alternation.
//
// The discard is still by Channel ID, because `channels` is Channel-ID-keyed:
// two instances sharing one Channel ID would share one set and reset each other.
// That is pre-existing, and inert while redundant paths carry distinct ids, but
// it is the reason this is not yet the full `(source address, Channel ID)` key
// that "Redundant Channel Instances" asks for.
func (rs *refdataState) onResetChannel(only uint8) {
	for ch, s := range rs.channels {
		if ch != only {
			continue
		}
		s.valid = false
		s.latestSeq = 0
		s.expectedCount = 0
		s.defs = make(map[uint32]defInfo)
		s.seqEverSet = false
		s.prevSummarySet = false
		s.setSnapshotSet = false
		s.hadNonEmptySet = false
		// Task 15: clear timing state on reset.
		s.lastManifestSendTS = 0
		s.lastManifestSendTSSet = false
		s.firstSendTS = 0
		s.firstSendTSSet = false
		s.lastServingSendTS = 0
		s.lastServingSendTSSet = false
		s.everReady = false
		s.readySendTS = 0
		s.readySendTSSet = false
		s.neverReadyDecided = false
		s.cycleStartSendTS = 0
		s.cycleStartSendTSSet = false
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
		rs.channels[ch] = s
	}
}

// onManifestSummary processes a ManifestSummary message.
//
// sendTS is the frame-level SendTS (nanoseconds) used for cadence checks.
// dirty is true when the refdata port had a gap in the reorder window: checks
// that cannot be proven on gapped data downgrade to Unverifiable.
func (rs *refdataState) onManifestSummary(ch uint8, valid uint8, seq uint16, count uint32, sendTS uint64, dirty bool, frameSeq uint64) {
	s := rs.channel(ch)

	// REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID: a Valid=1 summary must have both
	// Manifest Seq > 0 and Instrument Count > 0.
	//
	// seq==0: incoherent on a cold start / non-wrap (no publisher epoch starts at
	// seq=0). But the spec supports u16 wraparound, so a legitimate 65535→0 bump
	// (valid=1, prior state established) is a normal modular increment, NOT an epoch
	// start — it must flow into the seq-advance + SEQ_BUMP_NOT_BY_ONE checks below
	// rather than be discarded here.
	//
	// count==0: incoherent when the summary would advance or seed subscriber state
	// (i.e., cold start or a bumped seq). A count drop at the current established
	// seq is caught by MANIFEST.STATE_MACHINE below instead, so that the correct
	// rule fires for each context.
	//
	// Discard after emitting: incoherent summaries must not seed subscriber state.
	wouldAdvance := !s.valid || isLaterSeq(seq, s.latestSeq)
	isWrapToZero := s.seqEverSet && s.latestSeq == 0xFFFF // 65535 → 0 modular increment
	if valid == 1 && ((seq == 0 && !isWrapToZero) || (count == 0 && wouldAdvance)) {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		detail := "ManifestSummary valid=1 but Manifest Seq=0"
		if count == 0 {
			detail = fmt.Sprintf("ManifestSummary valid=1 but Instrument Count=0 (seq=%d)", seq)
		}
		rs.e.Emit("REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID", st, core.PortRefData, frameSeq, ch, 0, detail)
		// Do not seed subscriber state from an incoherent summary.
		return
	}

	// REFDATA.VALID_FLAG_WHILE_SERVING: after an established non-empty set, Valid
	// must remain 1 — unless the publisher is ending the session, which the spec
	// requires it to announce exactly this way. Hold the verdict; resolveValidZero
	// decides it once the cross-port signal is in.
	if valid == 0 && s.hadNonEmptySet {
		s.pendingValidZero = &validZeroEvent{frameSeq: frameSeq, dirty: dirty}
	}

	// REFDATA.SEQ_MONOTONIC_NO_REGRESS: seq must be modular-non-decreasing within
	// an era.  A regress is when a is later than b (b comes in, a is the current).
	// Discard regressed summaries after emitting: they must not overwrite tracking
	// state (prevSummarySeq/Count) and thereby mask subsequent COUNT_CHANGE_NO_SEQ_BUMP.
	if s.seqEverSet && valid == 1 {
		if isLaterSeq(s.latestSeq, seq) {
			// s.latestSeq is later than incoming seq → regress.
			st := core.Violation
			if dirty {
				st = core.Unverifiable
			}
			rs.e.Emit("REFDATA.SEQ_MONOTONIC_NO_REGRESS", st, core.PortRefData, frameSeq, ch, 0,
				fmt.Sprintf("Manifest Seq regressed: last=%d incoming=%d", s.latestSeq, seq))
			return // stale summary: do not update any tracking state
		}
	}

	// REFDATA.SEQ_BUMP_NOT_BY_ONE: when the seq advances (active-set change),
	// it must increment by exactly 1 modulo 65536.
	// A bump occurs when the incoming seq is later than latestSeq.
	if s.seqEverSet && valid == 1 && isLaterSeq(seq, s.latestSeq) {
		expectedNext := uint16(s.latestSeq + 1) // wraps modulo 65536 via uint16 arithmetic
		if seq != expectedNext {
			st := core.Violation
			if dirty {
				st = core.Unverifiable
			}
			rs.e.Emit("REFDATA.SEQ_BUMP_NOT_BY_ONE", st, core.PortRefData, frameSeq, ch, 0,
				fmt.Sprintf("Manifest Seq bumped from %d to %d (expected %d)",
					s.latestSeq, seq, expectedNext))
		}
	}

	// REFDATA.COUNT_CHANGE_NO_SEQ_BUMP: if the seq is the same as previously
	// seen but the count changed, that is a violation.
	if s.prevSummarySet && valid == 1 && seq == s.prevSummarySeq && count != s.prevSummaryCount {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("REFDATA.COUNT_CHANGE_NO_SEQ_BUMP", st, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("Instrument Count changed (%d→%d) without Manifest Seq change (seq=%d)",
				s.prevSummaryCount, count, seq))
	}

	// MANIFEST.STATE_MACHINE: overall coherence check.
	// (a) Valid must be 0 or 1; any other value is incoherent — discard after emitting.
	// (b) A Valid=1 summary with count=0 while we had a non-empty set at this seq
	//     is incoherent (count can only go to zero via a seq bump).
	if valid > 1 {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("MANIFEST.STATE_MACHINE", st, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("ManifestSummary valid=%d not in {0,1}", valid))
		// Discard: an out-of-range Valid value must not mutate subscriber state.
		return
	} else if valid == 1 && count == 0 && s.hadNonEmptySet && seq == s.latestSeq {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("MANIFEST.STATE_MACHINE", st, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("Manifest Seq %d: count dropped to 0 without a seq bump (was non-empty)", seq))
	}

	// --- State transitions (verbatim from the supplement) ---

	if valid == 0 {
		// on ManifestSummary(valid=0): clear all state including tracking flags.
		// Clearing seqEverSet/hadNonEmptySet/setSnapshotSet ensures the next
		// valid=1 summary starts fresh without false positives from stale history.
		s.valid = false
		s.latestSeq = 0
		s.expectedCount = 0
		s.defs = make(map[uint32]defInfo)
		s.prevSummarySet = false
		s.seqEverSet = false
		s.hadNonEmptySet = false
		s.setSnapshotSet = false
		// The cadence is between ManifestSummary messages (supplement §3), and a
		// Valid=0 summary is one — so the anchor advances here rather than being
		// cleared. Leaving it at the last Valid=1 measures a publisher's whole
		// invalid period as one gap the moment it resumes.
		s.lastManifestSendTS = sendTS
		s.lastManifestSendTSSet = true
		// everReady and firstSendTS are what this tool observed, not what the
		// publisher published, so a publisher flag does not erase them. The serving
		// period they describe is closed at the *start* of the next one, below.
		//
		// The definition cycle is about the active set, which is now gone.
		s.cycleStartSendTS = 0
		s.cycleStartSendTSSet = false
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
		return
	}

	// valid == 1 below.

	// A Valid=1 summary means the publisher is serving, so any session it ended is
	// over and cannot excuse a later Valid=0. Clear on EVERY Valid=1, not only one
	// that seeds or advances state: in steady state a summary does neither, and two
	// arms feed one channel-keyed state machine, so one arm's shutdown would sit
	// here excusing the other arm's later drop.
	//
	// Reaching here with a verdict outstanding means the channel went invalid and
	// came back, which settles it without waiting for end-of-run.
	if s.pendingValidZero != nil {
		rs.resolveValidZero(ch, s.pendingValidZero, true)
		s.pendingValidZero = nil
	}
	delete(rs.e.sessionEnd, ch)

	// --- Task 15: REFDATA.MANIFEST_CADENCE ---
	// Check cadence between successive ManifestSummary messages.
	// Gate: only when ExpectManifestCadence is configured AND a prior summary was seen.
	if rs.e.cfg.ExpectManifestCadence > 0 && s.lastManifestSendTSSet && sendTS >= s.lastManifestSendTS {
		gap := time.Duration(sendTS-s.lastManifestSendTS) * time.Nanosecond
		if gap > rs.e.cfg.ExpectManifestCadence {
			st := core.Violation
			if dirty {
				st = core.Unverifiable
			}
			rs.e.Emit("REFDATA.MANIFEST_CADENCE", st, core.PortRefData, frameSeq, ch, 0,
				fmt.Sprintf("ManifestSummary gap %v exceeds --expect-manifest-cadence %v",
					gap, rs.e.cfg.ExpectManifestCadence))
		}
	}

	// --- REFDATA.DEFINITION_CYCLE_COVERAGE + REFDATA.NO_BURST_DEFINITIONS ---
	// The retransmission cycle is a TUMBLING window of ExpectDefinitionCycle
	// wall-time, decoupled from manifest arrival: it is closed only once a full
	// cycle has actually elapsed since it opened, then immediately reopened.
	// (Previously the cycle was reset on every ManifestSummary, so the window was
	// the inter-manifest gap; at spec cadence — manifests far more frequent than
	// the cycle — that gap never reached ExpectDefinitionCycle and the coverage
	// check was structurally dead. Manifests now only sample the elapsed time.)
	//
	// At close, every instrument in the frozen active set must have been
	// retransmitted at least once during the window. Gate on dirty.
	if rs.e.cfg.ExpectDefinitionCycle > 0 && s.cycleStartSendTSSet && sendTS >= s.cycleStartSendTS &&
		time.Duration(sendTS-s.cycleStartSendTS)*time.Nanosecond >= rs.e.cfg.ExpectDefinitionCycle {
		// Coverage/burst apply only once the active set is established; before that
		// this is the bootstrap window, not subject to pacing requirements.
		//
		// A closed cycle is one opportunity for each of these two rules, and both
		// account for it — including the bootstrap and complete-coverage cases, which
		// were silent and so left "the publisher paced every definition correctly"
		// looking identical to "the window never closed on an established set". See
		// engine/denominator.go.
		if !s.setSnapshotSet {
			for _, rule := range []string{"REFDATA.DEFINITION_CYCLE_COVERAGE", "REFDATA.NO_BURST_DEFINITIONS"} {
				rs.e.unverified(rule, core.ReasonColdStart, core.PortRefData, frameSeq, ch, 0,
					"cycle closed before the active definition set was established (bootstrap window)")
			}
		} else {
			// Coverage: every active def (from the frozen set snapshot) must have
			// been retransmitted at least once this cycle.  Using setSnapshot (not
			// just cardinality) detects the publisher retransmitting the right
			// count but the wrong instruments.
			coverageOK := false
			missing := 0
			for id := range s.setSnapshot {
				if _, seen := s.defsSeenThisCycle[id]; !seen {
					missing++
				}
			}
			if missing > 0 {
				st, reason := core.Violation, ""
				if dirty {
					st, reason = core.Unverifiable, core.ReasonLoss
				}
				rs.e.Emit("REFDATA.DEFINITION_CYCLE_COVERAGE", st, core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("cycle window %v: %d/%d definitions not retransmitted",
						rs.e.cfg.ExpectDefinitionCycle, missing, len(s.setSnapshot)), reason)
			} else {
				coverageOK = true
				rs.e.passed("REFDATA.DEFINITION_CYCLE_COVERAGE", core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("cycle window %v: all %d definitions retransmitted",
						rs.e.cfg.ExpectDefinitionCycle, len(s.setSnapshot)))
			}
			// Burst: only evaluated when coverage was complete this cycle; otherwise
			// the primary finding is coverage, not burst.
			switch {
			case !coverageOK:
				rs.e.unverified("REFDATA.NO_BURST_DEFINITIONS", core.ReasonSuperseded, core.PortRefData, frameSeq, ch, 0,
					"coverage was incomplete this cycle, which is the finding that matters; pacing is not judged on top of it")
			case s.expectedCount <= 1:
				rs.e.inapplicable("REFDATA.NO_BURST_DEFINITIONS", core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("%d definition(s) in the active set: a single definition cannot be bursted", s.expectedCount))
			case s.defFramesSeen == 1:
				st, reason := core.Violation, ""
				if dirty {
					st, reason = core.Unverifiable, core.ReasonLoss
				}
				rs.e.Emit("REFDATA.NO_BURST_DEFINITIONS", st, core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("all %d definitions emitted in a single frame (burst, not paced)",
						s.expectedCount), reason)
			default:
				rs.e.passed("REFDATA.NO_BURST_DEFINITIONS", core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("%d definitions paced across %d frame(s)", s.expectedCount, s.defFramesSeen))
			}
		}
		// Reopen the next tumbling window starting now.
		s.cycleStartSendTS = sendTS
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
	}

	// A Valid=1 on an invalid channel that has already been observed opens a NEW
	// serving period. Judge the one that just ended and start a fresh observation
	// window, or the next period inherits the previous one's readiness and is
	// credited with the wall-clock time the channel spent invalid.
	if !s.valid && s.firstSendTSSet {
		rs.e.decideNeverReachesReady(ch, s, frameSeq, true)
		s.everReady = false
		s.readySendTS = 0
		s.readySendTSSet = false
		s.firstSendTSSet = false
		s.lastServingSendTSSet = false
		// The period that just decided is closed; the one opening here is a fresh
		// subscriber and gets its own window to reach ready.
		s.neverReadyDecided = false
	}

	// Update previous-summary tracking (for COUNT_CHANGE_NO_SEQ_BUMP).
	s.prevSummarySeq = seq
	s.prevSummaryCount = count
	s.prevSummarySet = true
	s.seqEverSet = true

	if !s.valid || isLaterSeq(seq, s.latestSeq) {
		// New or advancing seq: reset defs and update state.
		s.valid = true
		s.latestSeq = seq
		s.expectedCount = count
		s.defs = make(map[uint32]defInfo)
		// A seq bump also resets the set snapshot; new cycle may have a different set.
		s.setSnapshotSet = false
	}
	// If seq == latestSeq and state was already valid: no-op on state (idempotent).

	// --- timing trackers ---
	if !s.firstSendTSSet {
		s.firstSendTS = sendTS
		s.firstSendTSSet = true
	}
	s.lastManifestSendTS = sendTS
	s.lastManifestSendTSSet = true
	s.lastServingSendTS = sendTS
	s.lastServingSendTSSet = true
	// Open the (single) retransmission cycle on the first valid manifest. It then
	// tumbles every ExpectDefinitionCycle (closed/reopened in the block above),
	// rather than resetting on every manifest.
	if rs.e.cfg.ExpectDefinitionCycle > 0 && !s.cycleStartSendTSSet {
		s.cycleStartSendTS = sendTS
		s.cycleStartSendTSSet = true
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
	}
}

// resolveShutdownVerdicts decides every held Valid=0 verdict at end of run.
//
// It must run after Flush: a publisher emits EndOfSession and then stops, so
// nothing follows to push that last frame out of the reorder window, and before
// the drain the signal reads as absent rather than buffered.
func (rs *refdataState) resolveShutdownVerdicts() {
	for ch, s := range rs.channels {
		if s.pendingValidZero == nil {
			continue
		}
		rs.resolveValidZero(ch, s.pendingValidZero, false)
		s.pendingValidZero = nil
	}
}

// resolveValidZero emits the REFDATA.VALID_FLAG_WHILE_SERVING verdict for one held
// Valid=0 summary. resumed is true when the publisher has since declared the
// channel valid again, which proves the drop was not the end of a session.
//
// Order matters. A witnessed EndOfSession settles it outright; everything below
// turns on the *absence* of one, which loss or an unwatched mktdata port can
// equally explain.
//
// The mark deliberately outranks resumed, even though a resume looks like stronger
// evidence. Under channel-keyed state "EndOfSession, Valid=0, Valid=1" is the same
// byte sequence for a publisher that ended its session and then resumed, and for
// one arm shutting down cleanly while the other keeps serving — the routine shape
// in a two-arm deployment. Ranking resumed first turns that conformant shutdown
// into a must violation; this way round the cost is a missed one. See the README's
// channel-vs-instance limitation.
func (rs *refdataState) resolveValidZero(ch uint8, ev *validZeroEvent, resumed bool) {
	const rule = "REFDATA.VALID_FLAG_WHILE_SERVING"
	if _, ended := rs.e.sessionEnd[ch]; ended {
		rs.e.passed(rule, core.PortRefData, ev.frameSeq, ch, 0,
			"ManifestSummary Valid=0 paired with EndOfSession on mktdata: the shutdown the spec mandates")
		return
	}
	if ev.dirty {
		rs.e.unverified(rule, core.ReasonLoss, core.PortRefData, ev.frameSeq, ch, 0,
			"ManifestSummary Valid=0 on a gapped refdata window")
		return
	}
	if resumed {
		rs.e.Emit(rule, core.Violation, core.PortRefData, ev.frameSeq, ch, 0,
			"ManifestSummary Valid=0 while subscriber has an established set, then Valid=1 again: service resumed, so this was not a session end")
		return
	}
	if !rs.e.mktdataObserved(ch) {
		rs.e.unverified(rule, core.ReasonBoundSubset, core.PortRefData, ev.frameSeq, ch, 0,
			"ManifestSummary Valid=0 with no mktdata observed on this channel, so a session end cannot be told from a mid-service drop")
		return
	}
	if rs.e.dirtyOn(core.PortMktData, ch) {
		rs.e.unverified(rule, core.ReasonLoss, core.PortRefData, ev.frameSeq, ch, 0,
			"ManifestSummary Valid=0 and no EndOfSession, but the mktdata window was gapped and could have carried it")
		return
	}
	rs.e.Emit(rule, core.Violation, core.PortRefData, ev.frameSeq, ch, 0,
		"ManifestSummary Valid=0 while subscriber has an established set, with no EndOfSession on mktdata")
}

// onInstrumentDef processes an InstrumentDefinition message.
//
// sendTS is the frame-level SendTS used for cycle tracking.
// dirty is true when the refdata port had a gap.
// defaultMethod and priceBound are feed-specific fields extracted by the caller;
// they are stored in defInfo for consumption by feed validators (e.g. Task 17).
func (rs *refdataState) onInstrumentDef(ch uint8, instrID uint32, manifestSeq uint16, defaultMethod, priceBound uint8, sendTS uint64, dirty bool, frameSeq uint64) {
	s := rs.channel(ch)

	if !s.valid {
		// No established state: silently discard.
		return
	}

	// REFDATA.STALE_SEQ_TAG_AFTER_BUMP: after a bump, defs must carry the new seq.
	// A def tagged with any seq other than latestSeq is stale.
	if manifestSeq != s.latestSeq {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("REFDATA.STALE_SEQ_TAG_AFTER_BUMP", st, core.PortRefData, frameSeq, ch, instrID,
			fmt.Sprintf("InstrumentDef instrument=%d tagged with seq=%d, current seq=%d",
				instrID, manifestSeq, s.latestSeq))
		// Discard per the supplement: "definitions tagged with any other seq are discarded".
		return
	}

	// REFDATA.SET_CHANGE_NO_SEQ_BUMP: if the set is frozen (ready() was true at
	// this seq) and a new instrument ID appears under the same seq, that is a
	// set membership change without a seq bump.
	if s.setSnapshotSet && s.setSnapshotSeq == manifestSeq {
		if _, known := s.setSnapshot[instrID]; !known {
			st := core.Violation
			if dirty {
				st = core.Unverifiable
			}
			rs.e.Emit("REFDATA.SET_CHANGE_NO_SEQ_BUMP", st, core.PortRefData, frameSeq, ch, instrID,
				fmt.Sprintf("InstrumentDef instrument=%d is new under seq=%d after set was established",
					instrID, manifestSeq))
		}
	}

	// State transition: accept the def (with per-instrument metadata).
	s.defs[instrID] = defInfo{
		defaultMethod: defaultMethod,
		priceBound:    priceBound,
	}

	// REFDATA.COUNT_VS_DISTINCT_DEFS: the number of distinct instrument IDs must
	// not exceed expected_count.  (Too many defs at this seq.)
	if uint32(len(s.defs)) > s.expectedCount {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("REFDATA.COUNT_VS_DISTINCT_DEFS", st, core.PortRefData, frameSeq, ch, instrID,
			fmt.Sprintf("distinct defs (%d) exceeds expected count (%d) at seq=%d",
				len(s.defs), s.expectedCount, manifestSeq))
	}

	// Freeze the set snapshot the first time ready() becomes true at this seq.
	if !s.setSnapshotSet && channelReady(s) {
		s.hadNonEmptySet = true
		s.setSnapshot = make(map[uint32]struct{}, len(s.defs))
		for id := range s.defs {
			s.setSnapshot[id] = struct{}{}
		}
		s.setSnapshotSeq = s.latestSeq
		s.setSnapshotSet = true
		// Task 15: record that this channel has ever become ready, and when — the
		// window NEVER_REACHES_READY grades is a deadline, so the moment is the
		// half that decides pass from violation.
		s.everReady = true
		if !s.readySendTSSet {
			s.readySendTS = sendTS
			s.readySendTSSet = true
		}
		// Task 18: notify the MBO per-instrument tracker gate of the new survivor
		// set so it can drop trackers for instruments removed by the seq bump.
		// Pass ch so only trackers for this refdata channel are pruned.
		if rs.e.mbo != nil {
			rs.e.mbo.onManifestBump(ch, s.setSnapshot)
		}
	}

	// Task 15: track def retransmissions for DEFINITION_CYCLE_COVERAGE and
	// NO_BURST_DEFINITIONS.  Only track when a cycle is open and
	// ExpectDefinitionCycle is configured.
	if rs.e.cfg.ExpectDefinitionCycle > 0 && s.cycleStartSendTSSet {
		s.defsSeenThisCycle[instrID] = struct{}{}
		// Count distinct frame SendTS values carrying defs within this cycle.
		if !s.prevDefFrameTSSet || sendTS != s.prevDefFrameTS {
			s.defFramesSeen++
			s.prevDefFrameTS = sendTS
			s.prevDefFrameTSSet = true
		}
	}
}

// --- Engine integration ---

// processRefdataFrame is called from Engine.classify for frames arriving on
// PortRefData.  It handles the reset-era detection, then routes ManifestSummary
// (0x07) and InstrumentDefinition (0x02) messages to the state machine.
func (e *Engine) processRefdataFrame(f *wire.Frame, pt *portTracker) {
	if e.refdata == nil {
		e.refdata = newRefdataState(e)
	}

	// Detect an era change (Reset Count change → discard that channel's state).
	// Reset Count belongs to the channel instance — (source address, Channel ID,
	// destination port) — so it is tracked on that instance's tracker and resets
	// only its own channel. Tracked per port instead, two publishers on one port
	// read as an era flip on every alternation, and each flip erases both paths'
	// definition sets: the sets never survive to ready(), and the discard is
	// silent because onInstrumentDef drops definitions on an invalid channel
	// without a finding.
	ch := f.Header.ChannelID
	if !pt.refdataEraSeeded {
		// Seed on this instance's first refdata datagram (any Reset Count is valid).
		pt.refdataEra = f.Header.ResetCount
		pt.refdataEraSeeded = true
	} else if f.Header.ResetCount != pt.refdataEra {
		pt.refdataEra = f.Header.ResetCount
		e.refdata.onResetChannel(ch)
	}

	dirty := pt.dirtyWindow
	sendTS := f.Header.SendTS
	frameSeq := f.Header.Sequence

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeManifest:
			valid, seq, count := manifestFields(m)
			e.refdata.onManifestSummary(ch, valid, seq, count, sendTS, dirty, frameSeq)

		case wire.TypeInstrumentDef:
			instrID, manifestSeq, defaultMethod, priceBound := instrDefAllFields(e.cfg.Feed, m)
			e.refdata.onInstrumentDef(ch, instrID, manifestSeq, defaultMethod, priceBound, sendTS, dirty, frameSeq)
		}
	}

	// REFDATA.NEVER_REACHES_READY decides here rather than only at EndRun, so the
	// rule is reachable in a process that never exits (#50). Terminal verdicts only;
	// see decideNeverReachesReady.
	//
	// Look the channel up rather than channel(ch), which creates the entry: a refdata
	// datagram carrying neither a ManifestSummary nor an InstrumentDefinition would
	// otherwise materialize state for a channel that has no reference data at all —
	// a runt decoding to the all-zero header, or a Heartbeat — and EndRun would then
	// report a cold-start Unverified for it.
	if s, ok := e.refdata.channels[ch]; ok {
		e.decideNeverReachesReady(ch, s, f.Header.Sequence, false)
	}
}
