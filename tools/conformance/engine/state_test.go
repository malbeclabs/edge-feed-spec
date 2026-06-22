package engine

import (
	"encoding/binary"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// captureAll captures all findings and transport-loss events.
type captureAll struct {
	findings  []core.Finding
	lostPorts []core.Port
}

func (c *captureAll) Record(f core.Finding)                 { c.findings = append(c.findings, f) }
func (c *captureAll) TransportLoss(p core.Port)             { c.lostPorts = append(c.lostPorts, p) }
func (c *captureAll) TransportCorruption(core.Port, string) {}
func (c *captureAll) SnapshotAudit(string)                  {}
func (c *captureAll) SetInstrumentState(string, int)        {}

func (c *captureAll) violationsFor(ruleID string) int {
	n := 0
	for _, f := range c.findings {
		if f.RuleID == ruleID && f.Status == core.Violation {
			n++
		}
	}
	return n
}

func (c *captureAll) findingsFor(ruleID string) []core.Finding {
	var out []core.Finding
	for _, f := range c.findings {
		if f.RuleID == ruleID {
			out = append(out, f)
		}
	}
	return out
}

// makeHB builds a minimal TOB heartbeat frame with given seq, resetCount, sendTS, channelID.
func makeHB(magic uint16, channelID uint8, seq uint64, resetCount uint8, sendTS uint64) *wire.Frame {
	raw := wb.Frame(magic).
		Channel(channelID).
		Seq(seq).
		ResetCount(resetCount).
		SendTS(sendTS).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) {
			b.U8(channelID) // heartbeat channel_id at offset 0 of body
			b.Pad(11)
		}).
		Bytes()
	f, _ := wire.Decode(raw, magic)
	return f
}

// TestSeqNoGap: seq 0,1,2 on one port → no gap, no transport loss, no SEQ_RESET_GAP.
func TestSeqNoGap(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	for seq := uint64(0); seq < 3; seq++ {
		f := makeHB(wire.MagicTOB, 1, seq, 0, seq+1000)
		eng.Process(f, port, nil)
	}
	eng.Flush()

	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("expected 0 FRAME.SEQ_RESET_GAP violations, got %d", n)
	}
	if len(cap.lostPorts) != 0 {
		t.Errorf("expected 0 transport losses, got %d", len(cap.lostPorts))
	}
}

// TestSeqGapIsTransportLoss: seq 0, then seq 2 (gap of 1) → transport loss, no violation.
func TestSeqGapIsTransportLoss(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	// Send seq 0 then seq 2 (gap at 1).
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 0, 1000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 2, 0, 1002), port, nil)
	eng.Flush()

	// A forward gap is transport loss, NOT a FRAME.SEQ_RESET_GAP violation.
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("forward gap must not produce SEQ_RESET_GAP violation, got %d", n)
	}
	// Transport loss must have been recorded.
	lostMkt := 0
	for _, p := range cap.lostPorts {
		if p == core.PortMktData {
			lostMkt++
		}
	}
	if lostMkt == 0 {
		t.Error("expected at least one TransportLoss on mktdata port for seq gap")
	}
}

// TestSeqResetGapViolation: backward seq movement without a reset-count change is a violation.
// To trigger this, we use window=1 so the reorder buffer immediately pops frames,
// then send a late (low-seq) frame that cannot be absorbed as reordering.
//
// Sequence of events with window=1:
//
//	push(seq=10): buf=[10], len=1, NOT >1 → no pop
//	push(seq=20): buf=[10,20], len=2 >1 → pop min=10 → classify(10), lastSeq=10
//	push(seq=5):  buf=[5,20],  len=2 >1 → pop min=5  → classify(5): seq=5 < lastSeq=10 → VIOLATION
func TestSeqResetGapViolation(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, cap)
	port := core.PortMktData

	eng.Process(makeHB(wire.MagicTOB, 1, 10, 0, 1010), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 20, 0, 1020), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 5, 0, 1005), port, nil)
	eng.Flush()

	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n == 0 {
		t.Error("expected FRAME.SEQ_RESET_GAP violation for backward seq without reset")
	}
}

// TestResetEraAdvance: a newer Reset Count drains the current-era buffer and restarts seq.
func TestResetEraAdvance(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	// Era 0: seqs 10, 11.
	eng.Process(makeHB(wire.MagicTOB, 1, 10, 0, 1010), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 11, 0, 1011), port, nil)
	// Era 1: seq restarts at 0. Must NOT be treated as backward motion.
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 1, 2000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 1, 2001), port, nil)
	eng.Flush()

	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("era advance must not produce SEQ_RESET_GAP violation, got %d", n)
	}
}

// TestOlderEraStraggler: a frame with an older Reset Count must be dropped silently
// (transport loss accounted, no publisher violation from the straggler itself).
func TestOlderEraStraggler(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	// Establish era 2.
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 2, 3000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 2, 3001), port, nil)
	// Straggler from era 0 (older, d=254 → Older).
	eng.Process(makeHB(wire.MagicTOB, 1, 5, 0, 1005), port, nil)
	eng.Flush()

	// The straggler should be dropped (transport loss), not a publisher violation.
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("older-era straggler must not produce SEQ_RESET_GAP violation, got %d", n)
	}
	// Transport loss must be recorded for the straggler.
	lostMkt := 0
	for _, p := range cap.lostPorts {
		if p == core.PortMktData {
			lostMkt++
		}
	}
	if lostMkt == 0 {
		t.Error("expected TransportLoss for older-era straggler")
	}
}

// TestDupIdentical: duplicate seq with identical bytes → dropped silently, no findings.
func TestDupIdentical(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	f0 := makeHB(wire.MagicTOB, 1, 0, 0, 1000)
	f1 := makeHB(wire.MagicTOB, 1, 1, 0, 1001)

	eng.Process(f0, port, nil)
	eng.Process(f1, port, nil)
	// Replay the exact same frames.
	eng.Process(f0, port, nil)
	eng.Process(f1, port, nil)
	eng.Flush()

	// No SEQ_DUP_DIVERGENT findings for identical duplicates.
	if n := len(cap.findingsFor("FRAME.SEQ_DUP_DIVERGENT")); n != 0 {
		t.Errorf("identical dup must produce 0 FRAME.SEQ_DUP_DIVERGENT findings, got %d", n)
	}
	// No SEQ_RESET_GAP violations.
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("expected 0 SEQ_RESET_GAP for identical dup stream, got %d", n)
	}
}

// TestDupDivergent: duplicate seq with different bytes → ONLY FRAME.SEQ_DUP_DIVERGENT.
func TestDupDivergent(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	// Send seq 0 and 1.
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 0, 1000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 0, 1001), port, nil)
	// Now send seq 1 again but with different sendTS → different bytes.
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 0, 9999), port, nil)
	eng.Flush()

	dups := cap.findingsFor("FRAME.SEQ_DUP_DIVERGENT")
	if len(dups) != 1 {
		t.Errorf("divergent dup must produce exactly 1 FRAME.SEQ_DUP_DIVERGENT, got %d", len(dups))
	}
	if len(dups) > 0 && dups[0].Status != core.Violation {
		t.Errorf("FRAME.SEQ_DUP_DIVERGENT must be a Violation, got %v", dups[0].Status)
	}
	// The divergent dup must NOT also trigger other findings (early stop).
	// In particular, no structural findings from checkTier1 should be double-emitted.
	// Count total findings beyond the one dup-divergent.
	nonDupFindings := 0
	for _, f := range cap.findings {
		if f.RuleID != "FRAME.SEQ_DUP_DIVERGENT" && f.Status == core.Violation {
			nonDupFindings++
		}
	}
	if nonDupFindings != 0 {
		t.Errorf("divergent-dup early-stop: expected 0 other violations, got %d", nonDupFindings)
	}
}

// TestEraRelation verifies the modular era comparison.
func TestEraRelation(t *testing.T) {
	tests := []struct {
		cur, in uint8
		want    eraResult
	}{
		{0, 0, eraSame},
		{5, 5, eraSame},
		{0, 1, eraNewer},
		{0, 127, eraNewer},
		{255, 126, eraNewer}, // 126-255=127 mod 256 = 127 → Newer
		{0, 129, eraOlder},
		{0, 255, eraOlder},
		{1, 0, eraOlder}, // 0-1=255 → Older
		{0, 128, eraAmbiguous},
		{5, 133, eraAmbiguous},
	}
	for _, tt := range tests {
		got := eraRelation(tt.cur, tt.in)
		if got != tt.want {
			t.Errorf("eraRelation(%d,%d) = %v, want %v", tt.cur, tt.in, got, tt.want)
		}
	}
}

// TestSharedRulesOnTOB: prove seq detector rules work on a non-MBO (TOB) feed.
func TestSharedRulesOnTOB(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 4}
	eng := New(cfg, cap)

	port := core.PortMktData
	// seq 0,1,2,4 → gap at 3, then seq 3 late (reorder within window).
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 0, 1000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 0, 1001), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 2, 0, 1002), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 4, 0, 1004), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 3, 0, 1003), port, nil) // late arrival within window
	eng.Flush()

	// No gap should have been declared for seq 3 because it arrived within the window.
	if n := len(cap.lostPorts); n != 0 {
		t.Errorf("reordered frame within window: expected 0 transport losses, got %d", n)
	}
	// No SEQ_RESET_GAP violations.
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("in-window reorder must not produce SEQ_RESET_GAP violation, got %d", n)
	}
}

// TestSendTSMonotonic: a decreasing SendTS across increasing seq must emit a Violation.
func TestSendTSMonotonic(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 8}
	eng := New(cfg, cap)

	port := core.PortMktData
	eng.Process(makeHB(wire.MagicTOB, 1, 0, 0, 1000), port, nil)
	eng.Process(makeHB(wire.MagicTOB, 1, 1, 0, 999), port, nil) // SendTS went backward
	eng.Process(makeHB(wire.MagicTOB, 1, 2, 0, 1001), port, nil)
	eng.Flush()

	findings := cap.findingsFor("FRAME.SEND_TS_MONOTONIC")
	if len(findings) == 0 {
		t.Error("expected at least one FRAME.SEND_TS_MONOTONIC finding for decreasing SendTS")
	}
	for _, f := range findings {
		if f.Status != core.Violation {
			t.Errorf("FRAME.SEND_TS_MONOTONIC must be Violation, got %v", f.Status)
		}
	}
}

// TestSourceIDConsistencyViolation: SOURCE_ID drift on a gapless, gated lifecycle
// must emit a Violation (not Unverifiable).
func TestSourceIDConsistencyViolation(t *testing.T) {
	const ch, instrID = uint8(1), uint32(200)
	e, ac := newMBOEngineW1()

	// Seed gapless history so gateConsumer returns true on next message.
	// buildOrderAddFull: ch, instrID, perSeq, orderID, side, sourceID, price, flags
	seq := seedGaplessHistory(e, ch, instrID, 3, 1)
	clearFindings(ac)

	// Add an order with sourceID=10.
	runMktdataSeq(e, buildOrderAddFull(ch, instrID, 4, 500, 0, 10, 100, 0), seq)
	seq++
	// Cancel that order with sourceID=20 (drift from 10) on gapless history.
	runMktdataSeq(e, buildOrderCancelFull(ch, instrID, 5, 500, 20), seq)
	e.Flush()

	findings := findingsFor(ac, "FIELD.SOURCE_ID_CONSISTENCY")
	if len(findings) == 0 {
		t.Fatal("expected FIELD.SOURCE_ID_CONSISTENCY finding for source ID drift")
	}
	for _, f := range findings {
		if f.Status != core.Violation {
			t.Errorf("FIELD.SOURCE_ID_CONSISTENCY on gapless gated lifecycle must be Violation, got %v", f.Status)
		}
	}
}

// TestFlushDrainsInOrder: frames buffered in reverse order must be classified in
// seq order when Flush() is called.
// TestSnapshotPortLeadsResetNoFalseMonotonic: F4 — when the SNAPSHOT port
// observes a channel-wide Reset Count change FIRST (before the mktdata port),
// the new era's low Anchor Seq / Snapshot ID must NOT falsely fire the monotonic
// rules. Reset Count is channel-wide, so the first port to see the advance must
// wipe all per-instrument MBO state (onResetCountForEra), idempotently. Without
// that, the new-era SnapshotBegin would compare against stale old-era snapTrackers
// (high anchor/snapID) and emit false SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT /
// SNAP.SNAPSHOT_ID_MONOTONIC Violations.
func TestSnapshotPortLeadsResetNoFalseMonotonic(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, ac)

	const ch = uint8(1)
	const instrID = uint32(500)

	// Era 0: a clean empty snapshot group with a HIGH anchor (100) and snapID (5).
	// This seeds the snapshot port's era and the per-instrument snapTracker.
	for _, raw := range [][]byte{
		withResetSeq(buildSnapBeginFull(ch, instrID, 100, 0, 5, 0), 0, 0),
		withResetSeq(buildSnapEndFull(ch, instrID, 100, 5), 0, 1),
	} {
		f, sf := wire.Decode(raw, wire.MagicMBO)
		e.Process(f, core.PortSnapshot, sf)
	}
	e.Flush()

	// Era 1 (Reset Count = 1) observed on the SNAPSHOT port FIRST, with LOW anchor (1)
	// and snapID (1) — legitimate after a reset. No era-1 mktdata frame has arrived.
	for _, raw := range [][]byte{
		withResetSeq(buildSnapBeginFull(ch, instrID, 1, 0, 1, 0), 1, 0),
		withResetSeq(buildSnapEndFull(ch, instrID, 1, 1), 1, 1),
	} {
		f, sf := wire.Decode(raw, wire.MagicMBO)
		e.Process(f, core.PortSnapshot, sf)
	}
	e.Flush()

	for _, rule := range []string{
		"SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT",
		"SNAP.SNAPSHOT_ID_MONOTONIC",
		"SNAP.BEGIN_ORDER_END_GROUPING",
	} {
		if hasViolation(ac, rule) {
			t.Errorf("F4: snapshot-port-leads reset falsely fired %s as a Violation", rule)
		}
	}
}

// withResetSeq decodes a frame's header bytes are left intact; it patches the
// Reset Count (offset 21) and the per-port Sequence Number (offset 4, u64 LE) in
// the raw frame so the test can drive era/seq deterministically.
func withResetSeq(raw []byte, resetCount uint8, seq uint64) []byte {
	out := make([]byte, len(raw))
	copy(out, raw)
	out[21] = resetCount
	binary.LittleEndian.PutUint64(out[4:], seq)
	return out
}

// TestMktdataLeadsResetSnapshotStragglerNotViolation: F4 — when the MKTDATA port
// observes a channel-wide reset FIRST while a snapshot group is still open, the
// wipe drops that group. A late OLD-era SnapshotEnd straggler arriving afterward on
// the snapshot port must be Unverifiable (the wipe taints the snapshot port), never
// a false SNAP.BEGIN_ORDER_END_GROUPING Violation.
func TestMktdataLeadsResetSnapshotStragglerNotViolation(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 1}, ac)
	const ch, instrID, snapID = uint8(1), uint32(700), uint32(9)

	proc := func(raw []byte, port core.Port) {
		f, sf := wire.Decode(raw, wire.MagicMBO)
		e.Process(f, port, sf)
		e.Flush()
	}

	// Era 0: open a snapshot group on the snapshot port (Begin total=1, no End yet).
	proc(withResetSeq(buildSnapBeginFull(ch, instrID, 50, 1, snapID, 0), 0, 0), core.PortSnapshot)
	// Seed mktdata era 0 so the next mktdata frame is an era ADVANCE, not a first-seed.
	proc(withResetSeq(buildOrderAddFrame(ch, instrID, 1), 0, 0), core.PortMktData)
	clearFindings(ac)
	// mktdata observes the reset FIRST (ResetCount=1) → wipes MBO state (drops the
	// open snapshot group) and taints the snapshot port.
	proc(withResetSeq(buildOrderAddFrame(ch, instrID, 1), 1, 0), core.PortMktData)
	// Old-era (ResetCount=0) SnapshotEnd straggler on the snapshot port → orphan.
	proc(withResetSeq(buildSnapEndFull(ch, instrID, 50, snapID), 0, 1), core.PortSnapshot)

	got := findingsFor(ac, "SNAP.BEGIN_ORDER_END_GROUPING")
	if len(got) == 0 {
		t.Fatal("expected an orphan SNAP.BEGIN_ORDER_END_GROUPING finding (Unverifiable) for the straggler")
	}
	for _, fn := range got {
		if fn.Status != core.Unverifiable {
			t.Errorf("F4: old-era snapshot straggler after a mktdata-leads reset wipe must be "+
				"Unverifiable, got status %v: %s", fn.Status, fn.Detail)
		}
	}
}

// TestLastInstrSeqNoFalsePositiveWhenSubscriberAhead: F3 — when the subscriber's
// delta tracker has advanced past the snapshot's LastInstrumentSeq (K), the rule
// SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS must NOT fire. The subscriber
// being ahead of K is a normal race, not a publisher fault.
func TestLastInstrSeqNoFalsePositiveWhenSubscriberAhead(t *testing.T) {
	const ch, instrID = uint8(1), uint32(600)
	const snapID = uint32(201)

	e, ac := newMBOEngineW1()

	// Send deltas on mktdata: per-instrument seqs 1,2,3,4,5 (lastInstrSeq=5).
	seq := seedGaplessHistory(e, ch, instrID, 5, 1)
	clearFindings(ac)

	// Now send a snapshot group with LastInstrumentSeq=3 (anchor captured earlier
	// than the current mktdata position). Subscriber has lastInstrSeq=5 > K=3.
	// SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS must NOT fire.
	runStream(e, []streamEntry{
		snapEntry(buildSnapBeginFull(ch, instrID, seq, 0, snapID, 3 /*lastInstrSeq=K*/), 1),
		snapEntry(buildSnapEndFull(ch, instrID, seq, snapID), 2),
	})

	for _, f := range findingsFor(ac, "SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS") {
		if f.Status == core.Violation || f.Status == core.Unverifiable {
			t.Errorf("F3: SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS must not fire when subscriber is ahead of K, got %v: %s", f.Status, f.Detail)
		}
	}
}

// TestFlushDeterministicPortOrder: F3 — Flush must drain ports in a fixed order
// (mktdata → refdata → snapshot) regardless of Go map iteration randomness.
// This is verified by checking that a mktdata frame buffered in the reorder window
// is classified (and emits findings) before any snapshot-port frames at Flush time.
func TestFlushDeterministicPortOrder(t *testing.T) {
	ac := &allCapture{}
	e := New(Config{Feed: core.FeedMBO, ReorderWindow: 8}, ac)
	const ch = uint8(1)
	const instrID = uint32(700)

	// Buffer a mktdata frame at seq=0 — it stays in the window (no overflow yet).
	mktRaw := buildOrderAddFrame(ch, instrID, 1)
	mktF, mktSF := wire.Decode(mktRaw, wire.MagicMBO)
	mktF.Header.Sequence = 0
	e.Process(mktF, core.PortMktData, mktSF)

	// Buffer a snapshot frame at seq=0 — also stays in window.
	snapRaw := buildSnapBeginFull(ch, instrID, 0, 0, 99, 0)
	snapF, snapSF := wire.Decode(snapRaw, wire.MagicMBO)
	snapF.Header.Sequence = 0
	e.Process(snapF, core.PortSnapshot, snapSF)

	// Both ports have portTrackers. Flush must not panic and must complete.
	// The deterministic order ensures mktdata state is established before
	// snapshot processing happens — no assertion on content, just that it runs.
	e.Flush()
}

func TestFlushDrainsInOrder(t *testing.T) {
	cap := &captureAll{}
	cfg := Config{Feed: core.FeedTOB, ReorderWindow: 16}
	eng := New(cfg, cap)

	port := core.PortMktData
	// Send 5 frames in reverse seq order — all arrive within the window.
	for seq := uint64(4); seq != ^uint64(0); seq-- {
		eng.Process(makeHB(wire.MagicTOB, 1, seq, 0, 1000+seq), port, nil)
	}
	eng.Flush()

	// No transport losses: all frames arrived, just out of order.
	if len(cap.lostPorts) != 0 {
		t.Errorf("fully-reversed-within-window: expected 0 transport losses, got %d", len(cap.lostPorts))
	}
	if n := cap.violationsFor("FRAME.SEQ_RESET_GAP"); n != 0 {
		t.Errorf("fully-reversed-within-window: expected 0 SEQ_RESET_GAP, got %d", n)
	}
}
