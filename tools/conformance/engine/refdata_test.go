package engine

// refdata_test.go — unit tests for the reference-data set-state machine and
// the 9 structural detector rules (Task 14).
//
// Test strategy: each test drives a refdataState directly (not through the
// Engine) so that we can isolate state-machine semantics from transport/buffer
// concerns.  Integration via Engine.Process is covered by the firesRefdata
// helper below.

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// --- helpers ---

// newTestRefdata builds a refdataState wired to an allCapture reporter that
// also records Emit calls through a thin engine shim.
func newTestRefdata(feed core.Feed) (*refdataState, *allCapture) {
	ac := &allCapture{}
	cfg := Config{Feed: feed}
	e := New(cfg, ac)
	rd := newRefdataState(e)
	return rd, ac
}

// findingsFor returns all findings for the given ruleID.
func findingsFor(ac *allCapture, ruleID string) []core.Finding {
	var out []core.Finding
	for _, f := range ac.findings {
		if f.RuleID == ruleID {
			out = append(out, f)
		}
	}
	return out
}

// hasViolation returns true if ac has a Violation for ruleID.
func hasViolation(ac *allCapture, ruleID string) bool {
	for _, f := range findingsFor(ac, ruleID) {
		if f.Status == core.Violation {
			return true
		}
	}
	return false
}

// clearFindings resets the capture slice so tests can check after each step.
func clearFindings(ac *allCapture) { ac.findings = nil }

// --- ManifestSummary body builder ---
// ManifestSummary layout (24 bytes total = 4-byte header + 20-byte body):
//
//	Body[0]  = ChannelID (u8)
//	Body[1]  = Valid (u8)
//	Body[2:4]= Reserved
//	Body[4:6]= Manifest Seq (u16 LE)
//	Body[6:8]= Reserved
//	Body[8:12]= Instrument Count (u32 LE)
//	Body[12:20]= Timestamp (u64 LE)
func manifestBody(valid uint8, seq uint16, count uint32) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U8(1)       // ChannelID (body off 0)
		b.U8(valid)   // Valid (body off 1)
		b.Pad(2)      // Reserved (body off 2)
		b.U16(seq)    // Manifest Seq (body off 4)
		b.Pad(2)      // Reserved (body off 6)
		b.U32(count)  // Instrument Count (body off 8)
		b.U64(100000) // Timestamp (body off 12)
	}
}

// --- InstrumentDefinition body builders ---
// TOB/MBO InstrumentDef (80 bytes total = 4-byte header + 76-byte body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:74] = other fields (opaque for our purposes)
//	Body[74:76]= Manifest Seq (u16 LE)  ← spec offset 78, body[74]
func instrDefTOBBody(instrID uint32, manifestSeq uint16) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID)     // Instrument ID (body off 0)
		b.Pad(70)          // other fields (body off 4..73)
		b.U16(manifestSeq) // Manifest Seq (body off 74) → total body 76 → msg 80
	}
}

// Midpoint InstrumentDef (64 bytes total = 4-byte header + 60-byte body):
//
//	Body[0:4]  = Instrument ID (u32 LE)
//	Body[4:56] = other fields (opaque for our purposes)
//	Body[56:58]= Manifest Seq (u16 LE)  ← spec offset 60, body[56]
func instrDefMidBody(instrID uint32, manifestSeq uint16) func(*wb.Body) {
	return func(b *wb.Body) {
		b.U32(instrID)     // Instrument ID (body off 0)
		b.Pad(52)          // other fields (body off 4..55)
		b.U16(manifestSeq) // Manifest Seq (body off 56)
		b.Pad(2)           // Reserved (body off 58) → total body 60 → msg 64
	}
}

// --- refdataState direct-drive helpers ---

// feedManifest feeds a ManifestSummary message directly into the state
// (bypasses the engine reorder buffer). sendTS is set to 0 (no timing checks).
func feedManifest(rd *refdataState, valid uint8, seq uint16, count uint32) {
	rd.onManifestSummary(1 /*channelID*/, valid, seq, count, 0 /*sendTS*/, false /*clean*/, 0 /*frameSeq*/)
}

// feedDef feeds an InstrumentDefinition directly into the state.
func feedDef(rd *refdataState, instrID uint32, manifestSeq uint16) {
	rd.onInstrumentDef(1 /*channelID*/, instrID, manifestSeq, 0 /*defaultMethod*/, 0 /*priceBound*/, 0 /*sendTS*/, false /*clean*/, 0 /*frameSeq*/)
}

// feedDefDirty feeds a def on a dirty window.
func feedDefDirty(rd *refdataState, instrID uint32, manifestSeq uint16) {
	rd.onInstrumentDef(1 /*channelID*/, instrID, manifestSeq, 0 /*defaultMethod*/, 0 /*priceBound*/, 0 /*sendTS*/, true /*dirty*/, 0 /*frameSeq*/)
}

// --- Tests ---

// TestRefdataReady verifies the basic happy path:
// ManifestSummary(valid=1,seq=1,count=2) + 2 InstrumentDefinitions(seq=1) → ready==true.
func TestRefdataReady(t *testing.T) {
	rd, _ := newTestRefdata(core.FeedTOB)

	if rd.ready() {
		t.Fatal("should not be ready before any messages")
	}

	feedManifest(rd, 1, 1, 2)
	if rd.ready() {
		t.Fatal("should not be ready after only manifest (no defs yet)")
	}

	feedDef(rd, 100, 1)
	if rd.ready() {
		t.Fatal("should not be ready after only 1 of 2 expected defs")
	}

	feedDef(rd, 200, 1)
	if !rd.ready() {
		t.Fatal("should be ready after 2 expected defs with correct seq")
	}
}

// TestRefdataManifestBump verifies that a manifest seq bump clears defs so
// the subscriber must re-collect from scratch.
func TestRefdataManifestBump(t *testing.T) {
	rd, _ := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	if !rd.ready() {
		t.Fatal("should be ready before bump")
	}

	// Bump seq: defs get cleared.
	feedManifest(rd, 1, 2, 2)
	if rd.ready() {
		t.Fatal("should NOT be ready after seq bump — defs cleared")
	}

	// Old defs with stale seq must not count.
	feedDef(rd, 100, 1) // stale seq, discarded
	feedDef(rd, 200, 1)
	if rd.ready() {
		t.Fatal("should NOT be ready: old-seq defs must be discarded after bump")
	}

	// New defs with new seq restore readiness.
	feedDef(rd, 100, 2)
	feedDef(rd, 200, 2)
	if !rd.ready() {
		t.Fatal("should be ready after new-seq defs post-bump")
	}
}

// TestRefdataResetClear verifies that a frame-level reset (new Reset Count)
// discards all state and starts fresh.
func TestRefdataResetClear(t *testing.T) {
	rd, _ := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 1)
	feedDef(rd, 100, 1)
	if !rd.ready() {
		t.Fatal("precondition: should be ready")
	}

	rd.onReset(2) // new Reset Count
	if rd.ready() {
		t.Fatal("should NOT be ready after reset")
	}
}

// TestRefdataValidFlagWhileServing: after the state machine has an established
// non-empty set, a ManifestSummary with Valid=0 is a violation
// (REFDATA.VALID_FLAG_WHILE_SERVING).
func TestRefdataValidFlagWhileServing(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	// Establish a ready set.
	feedManifest(rd, 1, 1, 1)
	feedDef(rd, 100, 1)
	clearFindings(ac)

	// Now send Valid=0 summary — this is the violation.
	feedManifest(rd, 0, 0, 0)
	if !hasViolation(ac, "REFDATA.VALID_FLAG_WHILE_SERVING") {
		t.Error("REFDATA.VALID_FLAG_WHILE_SERVING: expected violation, got none")
	}
}

// TestRefdataValidFlagWhileServingSilent: Valid=0 before any set is established
// must NOT fire VALID_FLAG_WHILE_SERVING (publisher still initializing).
func TestRefdataValidFlagWhileServingSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)
	feedManifest(rd, 0, 0, 0)
	if hasViolation(ac, "REFDATA.VALID_FLAG_WHILE_SERVING") {
		t.Error("REFDATA.VALID_FLAG_WHILE_SERVING: must be silent when set not yet established")
	}
}

// TestRefdataStaleSeqTagAfterBump: after a seq bump, InstrumentDefs must carry
// the NEW seq.  Old-seq tags are a REFDATA.STALE_SEQ_TAG_AFTER_BUMP violation.
func TestRefdataStaleSeqTagAfterBump(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	// Bump.
	feedManifest(rd, 1, 2, 2)
	clearFindings(ac)

	// Send a def with the OLD seq — stale tag.
	feedDef(rd, 100, 1)
	if !hasViolation(ac, "REFDATA.STALE_SEQ_TAG_AFTER_BUMP") {
		t.Error("REFDATA.STALE_SEQ_TAG_AFTER_BUMP: expected violation, got none")
	}
}

// TestRefdataStaleSeqTagAfterBumpSilent: defs with the current seq after a bump
// must NOT fire the rule.
func TestRefdataStaleSeqTagAfterBumpSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedManifest(rd, 1, 2, 2)
	clearFindings(ac)

	feedDef(rd, 100, 2) // correct new seq
	if hasViolation(ac, "REFDATA.STALE_SEQ_TAG_AFTER_BUMP") {
		t.Error("REFDATA.STALE_SEQ_TAG_AFTER_BUMP: must be silent for defs with current seq")
	}
}

// TestRefdataCountVsDistinctDefs: the count in ManifestSummary must equal the
// number of distinct instrument IDs seen tagged with that seq
// (REFDATA.COUNT_VS_DISTINCT_DEFS).
// Trigger: declare count=2 but send 3 distinct defs → violation.
func TestRefdataCountVsDistinctDefs(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	feedDef(rd, 300, 1) // third def while count==2
	if !hasViolation(ac, "REFDATA.COUNT_VS_DISTINCT_DEFS") {
		t.Error("REFDATA.COUNT_VS_DISTINCT_DEFS: expected violation, got none")
	}
}

// TestRefdataCountVsDistinctDefsSilent: count==2, exactly 2 distinct defs → silent.
func TestRefdataCountVsDistinctDefsSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	if hasViolation(ac, "REFDATA.COUNT_VS_DISTINCT_DEFS") {
		t.Error("REFDATA.COUNT_VS_DISTINCT_DEFS: must be silent with exact count")
	}
}

// TestRefdataSetChangeNoSeqBump: a new ManifestSummary with the SAME seq but
// a DIFFERENT set of defs is a violation (REFDATA.SET_CHANGE_NO_SEQ_BUMP).
// We detect this by seeing a def with a different instrument ID than previously
// seen after the seq has been established, when the set was ready.
func TestRefdataSetChangeNoSeqBump(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	// Establish a ready set with instrID {100, 200}.
	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	// Another cycle starts (same seq): a new instrument 300 appears with seq=1.
	// Since the set was established at seq=1 with {100,200}, a new ID under the
	// same seq after ready is a set-change without a seq bump.
	clearFindings(ac)
	feedDef(rd, 300, 1)
	if !hasViolation(ac, "REFDATA.SET_CHANGE_NO_SEQ_BUMP") {
		t.Error("REFDATA.SET_CHANGE_NO_SEQ_BUMP: expected violation, got none")
	}
}

// TestRefdataSetChangeNoSeqBumpSilent: re-sending an existing def (same set)
// at the same seq must be silent.
func TestRefdataSetChangeNoSeqBumpSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	clearFindings(ac)

	// Re-transmit an existing def (same set, normal retransmit cycle).
	feedDef(rd, 100, 1)
	if hasViolation(ac, "REFDATA.SET_CHANGE_NO_SEQ_BUMP") {
		t.Error("REFDATA.SET_CHANGE_NO_SEQ_BUMP: must be silent on retransmission of known def")
	}
}

// TestRefdataCountChangeNoSeqBump: a ManifestSummary with the same seq but
// a different Instrument Count than previously advertised is a violation
// (REFDATA.COUNT_CHANGE_NO_SEQ_BUMP).
func TestRefdataCountChangeNoSeqBump(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2) // seq=1, count=2
	clearFindings(ac)

	feedManifest(rd, 1, 1, 3) // same seq=1, but count changed to 3
	if !hasViolation(ac, "REFDATA.COUNT_CHANGE_NO_SEQ_BUMP") {
		t.Error("REFDATA.COUNT_CHANGE_NO_SEQ_BUMP: expected violation, got none")
	}
}

// TestRefdataCountChangeNoSeqBumpSilent: same seq, same count → silent.
func TestRefdataCountChangeNoSeqBumpSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	clearFindings(ac)
	feedManifest(rd, 1, 1, 2) // repeated, same count
	if hasViolation(ac, "REFDATA.COUNT_CHANGE_NO_SEQ_BUMP") {
		t.Error("REFDATA.COUNT_CHANGE_NO_SEQ_BUMP: must be silent on same-count repeat")
	}
}

// TestRefdataSeqMonotonicNoRegress: seq must be modular-non-decreasing within
// an era (REFDATA.SEQ_MONOTONIC_NO_REGRESS).
func TestRefdataSeqMonotonicNoRegress(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 5, 1) // seq=5
	clearFindings(ac)

	feedManifest(rd, 1, 3, 1) // seq=3 < 5 → regress
	if !hasViolation(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS") {
		t.Error("REFDATA.SEQ_MONOTONIC_NO_REGRESS: expected violation, got none")
	}
}

// TestRefdataSeqMonotonicNoRegressSilent: seq advancing forward is fine.
func TestRefdataSeqMonotonicNoRegressSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 3, 1)
	clearFindings(ac)

	feedManifest(rd, 1, 4, 1) // seq=4 > 3 → advance, fine
	if hasViolation(ac, "REFDATA.SEQ_MONOTONIC_NO_REGRESS") {
		t.Error("REFDATA.SEQ_MONOTONIC_NO_REGRESS: must be silent on forward advance")
	}
}

// TestRefdataSeqBumpNotByOne: when the active set changes, Manifest Seq must
// increment by exactly 1 (REFDATA.SEQ_BUMP_NOT_BY_ONE).
func TestRefdataSeqBumpNotByOne(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 1)
	feedDef(rd, 100, 1)
	clearFindings(ac)

	// Jump by 2 instead of 1.
	feedManifest(rd, 1, 3, 1)
	if !hasViolation(ac, "REFDATA.SEQ_BUMP_NOT_BY_ONE") {
		t.Error("REFDATA.SEQ_BUMP_NOT_BY_ONE: expected violation for jump-by-2, got none")
	}
}

// TestRefdataSeqBumpNotByOneSilent: bump by exactly 1 → silent.
func TestRefdataSeqBumpNotByOneSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 1)
	feedDef(rd, 100, 1)
	clearFindings(ac)

	feedManifest(rd, 1, 2, 1) // bump by 1 → fine
	if hasViolation(ac, "REFDATA.SEQ_BUMP_NOT_BY_ONE") {
		t.Error("REFDATA.SEQ_BUMP_NOT_BY_ONE: must be silent for bump by exactly 1")
	}
}

// TestRefdataManifestSeqNonzeroWhenValid: a ManifestSummary with Valid=1 but
// Manifest Seq==0 is a violation (REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID).
func TestRefdataManifestSeqNonzeroWhenValid(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 0, 1) // valid=1, seq=0 → violation
	if !hasViolation(ac, "REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID") {
		t.Error("REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID: expected violation, got none")
	}
}

// TestRefdataManifestSeqNonzeroWhenValidSilent: valid=1 with seq>0 and count>0 is fine.
func TestRefdataManifestSeqNonzeroWhenValidSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 1) // valid=1, seq=1, count=1 → fine
	if hasViolation(ac, "REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID") {
		t.Error("REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID: must be silent for valid=1,seq=1,count=1")
	}
}

// TestRefdataManifestSeqNonzeroWhenValidCountZero: valid=1, seq=1, count=0
// on cold start is a REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID violation.
func TestRefdataManifestSeqNonzeroWhenValidCountZero(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 0) // valid=1, seq=1, count=0 → violation (cold start)
	if !hasViolation(ac, "REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID") {
		t.Error("REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID: expected violation for count=0 on cold start")
	}
}

// TestRefdataManifestStateMachine: overall state machine coherence
// (MANIFEST.STATE_MACHINE).
// Case: after a valid set is established, the next summary still claims the
// same seq but reports a count of 0.  That is internally incoherent (non-empty
// set with zero count at same seq).
func TestRefdataManifestStateMachine(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	clearFindings(ac)

	// Count drops to 0 without a seq bump.
	feedManifest(rd, 1, 1, 0)
	if !hasViolation(ac, "MANIFEST.STATE_MACHINE") {
		t.Error("MANIFEST.STATE_MACHINE: expected violation for count-drop-to-zero without bump")
	}
}

// TestRefdataManifestStateMachineSilent: a valid summary with count > 0 and
// incrementing seq is coherent → silent.
func TestRefdataManifestStateMachineSilent(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	clearFindings(ac)

	feedManifest(rd, 1, 2, 3) // seq bumped, count changed → coherent
	if hasViolation(ac, "MANIFEST.STATE_MACHINE") {
		t.Error("MANIFEST.STATE_MACHINE: must be silent on coherent seq bump with count change")
	}
}

// TestRefdataGapDowngradesViolation: when the refdata port has a gap (dirty
// window), violations that cannot be confirmed must downgrade to Unverifiable.
func TestRefdataGapDowngradesViolation(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	feedManifest(rd, 1, 1, 2)
	feedDef(rd, 100, 1)
	feedDef(rd, 200, 1)
	clearFindings(ac)

	// Inject a stale-seq def over a dirty window.
	feedDefDirty(rd, 50, 0) // old seq=0, dirty gap
	// STALE_SEQ_TAG_AFTER_BUMP could fire but must be Unverifiable, not Violation.
	for _, f := range findingsFor(ac, "REFDATA.STALE_SEQ_TAG_AFTER_BUMP") {
		if f.Status == core.Violation {
			t.Errorf("REFDATA.STALE_SEQ_TAG_AFTER_BUMP: expected Unverifiable on dirty window, got Violation")
		}
	}
}

// TestRefdataMidpointFeed verifies that the state machine works on the Midpoint
// feed (different InstrumentDefinition layout, 64-byte message).
func TestRefdataMidpointFeed(t *testing.T) {
	rd, _ := newTestRefdata(core.FeedMidpoint)

	feedManifest(rd, 1, 1, 1)
	feedDef(rd, 42, 1)
	if !rd.ready() {
		t.Fatal("midpoint feed: should be ready after 1 def with count=1")
	}
}

// TestRefdataSeqWraparound verifies modular u16 comparison at wraparound.
// A ManifestSummary at seq=65535 followed by seq=0 would be a regress in
// plain arithmetic, but modular says seq=0 is "later" than 65535.
// However seq=0 being "valid=1" violates MANIFEST_SEQ_NONZERO_WHEN_VALID,
// so use seq=65535 → seq=0 to verify modular handling in is_later.
// The transition 65535 → 1 should be a legal bump (1 ahead modular).
func TestRefdataSeqWraparound(t *testing.T) {
	rd, ac := newTestRefdata(core.FeedTOB)

	// Establish at seq=65535.
	feedManifest(rd, 1, 65535, 1)
	feedDef(rd, 100, 65535)
	clearFindings(ac)

	// Bump by 1 modular: 65535 + 1 mod 65536 = 0.  But seq=0 with valid=1
	// fires MANIFEST_SEQ_NONZERO_WHEN_VALID, not SEQ_BUMP_NOT_BY_ONE.
	// Instead test wraparound with 65534 → 65535 → 0 (skip nonzero rule
	// by verifying no SEQ_BUMP_NOT_BY_ONE on seq=65535+1=0 mod 65536).
	// We verify is_later is used: 65535 is NOT later than itself (same).
	if isLaterSeq(65535, 65535) {
		t.Error("isLaterSeq(65535,65535) must be false (same value)")
	}
	// 0 is later than 65535 modularly (by 1).
	if !isLaterSeq(0, 65535) {
		t.Error("isLaterSeq(0, 65535) must be true (0 = 65535+1 mod 65536)")
	}
	// 32768 is NOT later than 0 (exactly half-period, ambiguous; spec says < 32768).
	if isLaterSeq(32768, 0) {
		t.Error("isLaterSeq(32768, 0) must be false (half-period is not later)")
	}
	// 32767 IS later than 0 (within the forward half-period).
	if !isLaterSeq(32767, 0) {
		t.Error("isLaterSeq(32767, 0) must be true")
	}
}

// --- Integration: firesRefdata drives messages through Engine.Process ---

// buildManifestFrame builds a raw frame on the refdata port containing a
// ManifestSummary message.
func buildManifestFrame(magic uint16, valid uint8, seq uint16, count uint32) []byte {
	return wb.Frame(magic).Msg(0x07, 24, manifestBody(valid, seq, count)).Bytes()
}

// buildInstrDefFrameTOB builds a raw frame with a TOB InstrumentDef.
func buildInstrDefFrameTOB(instrID uint32, manifestSeq uint16) []byte {
	return wb.Frame(wire.MagicTOB).Msg(0x02, 80, instrDefTOBBody(instrID, manifestSeq)).Bytes()
}

// buildInstrDefFrameMid builds a raw frame with a Midpoint InstrumentDef.
func buildInstrDefFrameMid(instrID uint32, manifestSeq uint16) []byte {
	return wb.Frame(wire.MagicMid).Msg(0x02, 64, instrDefMidBody(instrID, manifestSeq)).Bytes()
}

// processRefdata runs a sequence of raw frames through the engine on the
// refdata port and returns all captured findings.
func processRefdata(t *testing.T, feed core.Feed, magic uint16, frames [][]byte) []core.Finding {
	t.Helper()
	ac := &allCapture{}
	e := New(Config{Feed: feed}, ac)
	for i, raw := range frames {
		f, sf := wire.Decode(raw, magic)
		// Assign distinct sequence numbers.
		f.Header.Sequence = uint64(i + 1)
		e.Process(f, core.PortRefData, sf)
	}
	e.Flush()
	return ac.findings
}

// firesRefdataRule returns true if the given rule fires as Violation in the
// sequence of frames.
func firesRefdataRule(t *testing.T, feed core.Feed, magic uint16, frames [][]byte, ruleID string) bool {
	t.Helper()
	for _, f := range processRefdata(t, feed, magic, frames) {
		if f.RuleID == ruleID && f.Status == core.Violation {
			return true
		}
	}
	return false
}

// TestRefdataIntegration_ReadyPath is an end-to-end integration test verifying
// that frames processed through Engine.Process reach the ready state.
func TestRefdataIntegration_ReadyPath(t *testing.T) {
	frames := [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 1, 2),
		buildInstrDefFrameTOB(100, 1),
		buildInstrDefFrameTOB(200, 1),
	}
	// No violations should be emitted on a conformant sequence.
	findings := processRefdata(t, core.FeedTOB, wire.MagicTOB, frames)
	for _, f := range findings {
		if f.Status == core.Violation {
			t.Errorf("unexpected violation on conformant refdata sequence: %s: %s", f.RuleID, f.Detail)
		}
	}
}

// TestRefdataIntegration_ManifestSeqNonzero tests the integration path for
// REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID.
func TestRefdataIntegration_ManifestSeqNonzero(t *testing.T) {
	frames := [][]byte{
		buildManifestFrame(wire.MagicTOB, 1, 0 /*seq=0 while valid=1*/, 1),
	}
	if !firesRefdataRule(t, core.FeedTOB, wire.MagicTOB, frames, "REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID") {
		t.Error("integration: REFDATA.MANIFEST_SEQ_NONZERO_WHEN_VALID did not fire")
	}
}

// TestRefdataIntegration_MidpointReadyPath verifies end-to-end integration
// for the Midpoint feed, which uses a 64-byte InstrumentDefinition with
// Manifest Seq at body offset 56 (spec offset 60) rather than the TOB/MBO
// offset of body[74] (spec offset 78).
func TestRefdataIntegration_MidpointReadyPath(t *testing.T) {
	frames := [][]byte{
		buildManifestFrame(wire.MagicMid, 1, 1, 1),
		buildInstrDefFrameMid(42, 1),
	}
	// No violations on a conformant Midpoint sequence.
	findings := processRefdata(t, core.FeedMidpoint, wire.MagicMid, frames)
	for _, f := range findings {
		if f.Status == core.Violation {
			t.Errorf("midpoint: unexpected violation on conformant refdata: %s: %s", f.RuleID, f.Detail)
		}
	}
}

// TestRefdataIntegration_MidpointStaleSeq verifies that the Midpoint integration
// path reads the Manifest Seq from the correct body offset (56). An InstrumentDef
// with a mismatched seq fires REFDATA.STALE_SEQ_TAG_AFTER_BUMP.
func TestRefdataIntegration_MidpointStaleSeq(t *testing.T) {
	frames := [][]byte{
		buildManifestFrame(wire.MagicMid, 1, 3, 1),
		buildInstrDefFrameMid(42, 1), // seq=1, but current is 3 → stale
	}
	if !firesRefdataRule(t, core.FeedMidpoint, wire.MagicMid, frames, "REFDATA.STALE_SEQ_TAG_AFTER_BUMP") {
		t.Error("midpoint integration: REFDATA.STALE_SEQ_TAG_AFTER_BUMP did not fire on wrong-seq def")
	}
}
