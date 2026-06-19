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

	// --- Task 15: timing/cadence state ---

	// lastManifestSendTS is the SendTS of the most-recently accepted ManifestSummary
	// (valid=1 that seeds or advances state). Set from frame SendTS (nanoseconds).
	lastManifestSendTS    uint64
	lastManifestSendTSSet bool

	// firstSendTS is the SendTS of the first ManifestSummary seen on this channel.
	// Used by NEVER_REACHES_READY to compute the observation window.
	firstSendTS    uint64
	firstSendTSSet bool

	// everReady is true once channelReady has ever been true for this channel.
	// Used by NEVER_REACHES_READY.
	everReady bool

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
	// portEra is the last Reset Count seen on the refdata port. Reset Count is
	// a port/frame-level concept (not per-channel), so we track it here and
	// reset ALL channels when it changes. This ensures that a new era observed
	// on any channel triggers a full reset of all channel states.
	portEra       uint8
	portEraSeeded bool // false until the first refdata frame is processed
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

// onReset handles a frame-level Reset Count change.  All per-channel state is
// discarded per the supplement: "subscribers detect the reset by comparing
// Reset Count against their last-seen value and discard all cached state."
func (rs *refdataState) onReset(newResetCount uint8) {
	// Reset all channels.  A reset is frame-wide, not channel-specific, but since
	// a channel's frames all carry the same Reset Count, we reset all channels.
	for ch, s := range rs.channels {
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
		s.everReady = false
		s.cycleStartSendTS = 0
		s.cycleStartSendTSSet = false
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
		rs.channels[ch] = s
	}
	_ = newResetCount // Reset Count is now tracked at the port level (rs.portEra).
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

	// REFDATA.VALID_FLAG_WHILE_SERVING: after an established non-empty set,
	// Valid must remain 1.  Valid=0 while serving is a publisher violation.
	if valid == 0 && s.hadNonEmptySet {
		st := core.Violation
		if dirty {
			st = core.Unverifiable
		}
		rs.e.Emit("REFDATA.VALID_FLAG_WHILE_SERVING", st, core.PortRefData, frameSeq, ch, 0,
			"ManifestSummary Valid=0 while subscriber has an established set")
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
		// Task 15: clear timing state when publisher signals valid=0.
		s.lastManifestSendTS = 0
		s.lastManifestSendTSSet = false
		s.firstSendTSSet = false
		s.everReady = false
		s.cycleStartSendTS = 0
		s.cycleStartSendTSSet = false
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
		return
	}

	// valid == 1 below.

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
		if s.setSnapshotSet {
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
				st := core.Violation
				if dirty {
					st = core.Unverifiable
				}
				rs.e.Emit("REFDATA.DEFINITION_CYCLE_COVERAGE", st, core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("cycle window %v: %d/%d definitions not retransmitted",
						rs.e.cfg.ExpectDefinitionCycle, missing, len(s.setSnapshot)))
			} else {
				coverageOK = true
			}
			// Burst: only evaluated when coverage was complete this cycle; otherwise
			// the primary finding is coverage, not burst.
			if coverageOK && s.expectedCount > 1 && s.defFramesSeen == 1 {
				st := core.Violation
				if dirty {
					st = core.Unverifiable
				}
				rs.e.Emit("REFDATA.NO_BURST_DEFINITIONS", st, core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("all %d definitions emitted in a single frame (burst, not paced)",
						s.expectedCount))
			}
		}
		// Reopen the next tumbling window starting now.
		s.cycleStartSendTS = sendTS
		s.defsSeenThisCycle = make(map[uint32]struct{})
		s.defFramesSeen = 0
		s.prevDefFrameTS = 0
		s.prevDefFrameTSSet = false
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
		// Task 15: record that this channel has ever become ready.
		s.everReady = true
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

	// Detect era change (Reset Count change → full state reset).
	// Reset Count is a port/frame-level concept, not per-channel.  We track
	// the last-seen Reset Count at the refdataState level so that a new era
	// observed on any channel triggers a reset of ALL channels, not just the
	// one whose frame happened to arrive first.
	ch := f.Header.ChannelID
	if !e.refdata.portEraSeeded {
		// Seed on the very first refdata frame (any Reset Count is valid).
		e.refdata.portEra = f.Header.ResetCount
		e.refdata.portEraSeeded = true
	} else if f.Header.ResetCount != e.refdata.portEra {
		e.refdata.portEra = f.Header.ResetCount
		e.refdata.onReset(f.Header.ResetCount)
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
}
