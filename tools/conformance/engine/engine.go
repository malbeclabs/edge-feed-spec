package engine

import (
	"fmt"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// envelopeRules keep their severity even under an unknown (higher) schema version,
// because the spec guarantees their layout is schema-stable.
var envelopeRules = map[string]bool{
	"FRAME.MAGIC_MISMATCH":     true,
	"FRAME.SCHEMA_VERSION":     true,
	"FRAME.MSG_COUNT_RANGE":    true,
	"FRAME.LENGTH_CONSISTENCY": true,
}

// Engine drives per-frame conformance checking. Construct with New; call Process
// for each decoded frame; call Flush then EndRun before reading results.
type Engine struct {
	cfg              Config
	rep              report.Reporter
	curUnknownSchema bool
	now              func() time.Time
	// per-port reorder buffers and seq trackers (keyed by core.Port).
	ports map[core.Port]*portTracker
	// refdata holds the reference-data set-state machine (Task 14 + 15).
	// Lazily initialized on the first PortRefData frame.
	refdata *refdataState
	// lastHbSendTS is the SendTS of the most-recent Heartbeat message seen on
	// PortMktData. Used for HEARTBEAT.CADENCE (Task 15).
	lastHbSendTS    uint64
	lastHbSendTSSet bool
	// mbo holds per-instrument MBO sequencing state (Task 18+).
	// Lazily initialized on the first MBO mktdata frame.
	mbo *mboState
}

// New constructs an Engine with the given config and reporter.
func New(cfg Config, rep report.Reporter) *Engine {
	if cfg.ReorderWindow <= 0 {
		cfg.ReorderWindow = 8
	}
	return &Engine{
		cfg:   cfg,
		rep:   rep,
		now:   time.Now,
		ports: make(map[core.Port]*portTracker),
	}
}

func (e *Engine) beginFrame(schemaVersion uint8) { e.curUnknownSchema = schemaVersion > 1 }

// Emit resolves severity from the registry, applying two downgrades:
//   - Conditional must* rules downgrade unless their --expect-* config is set.
//   - Under an unknown (higher) schema version, non-envelope checks downgrade.
//
// port and seq are the logical port and frame sequence number of the frame being
// classified; they are recorded on the Finding for logging and debugging.
// reason is an optional bounded enum string (e.g. "loss", "cold_start", "reorder")
// used for the unverifiable_total Prometheus label; at most one value is used.
func (e *Engine) Emit(ruleID string, st core.Status, port core.Port, seq uint64, ch uint8, inst uint32, detail string, reason ...string) {
	meta, ok := core.Lookup(ruleID)
	if !ok {
		panic("unknown rule id: " + ruleID)
	}
	sev := meta.Severity
	downgrade := func() {
		sev = core.Info
		if st == core.Violation {
			st = core.NA
		}
	}
	if meta.Conditional && !e.cfg.Configured(ruleID) {
		downgrade()
	}
	if e.curUnknownSchema && !envelopeRules[ruleID] {
		downgrade()
	}
	var rsn string
	if len(reason) > 0 {
		rsn = reason[0]
	}
	e.rep.Record(core.Finding{RuleID: ruleID, Severity: sev, Status: st, Feed: e.cfg.Feed,
		Port: port, Seq: seq, ChannelID: ch, InstrumentID: inst, Detail: detail, Reason: rsn, At: e.now()})
}

// portTrack returns (or lazily creates) the portTracker for the given port.
func (e *Engine) portTrack(port core.Port) *portTracker {
	pt, ok := e.ports[port]
	if !ok {
		pt = newPortTracker(e.cfg.ReorderWindow)
		e.ports[port] = pt
	}
	return pt
}

// mktdataPending reports whether the mktdata port's reorder buffer still holds
// unclassified frames. Cross-port snapshot checks that compare a snapshot against
// mktdata-derived state (SNAP.ANCHOR_IS_MKTDATA_SEQ, SNAP.LAST_INSTRUMENT_SEQ_
// CONSISTENT_WITH_DELTAS) must downgrade to Unverifiable when this is true, even
// on a gapless channel: a still-buffered mktdata frame could carry the anchor seq
// or the deltas up to K, so the current mktdata state is not yet as-of-anchor (F3).
func (e *Engine) mktdataPending() bool {
	pt, ok := e.ports[core.PortMktData]
	return ok && pt.buf.Len() > 0
}

// snapPortDirty reports whether the snapshot port's verifiability window is
// tainted (a snapshot-seq gap or transport corruption occurred this era). Used
// by the SNAP.TOTAL_ORDERS_COUNT_MATCH under-count gate so a truncated snapshot
// frame — whose header may not decode a channel — still downgrades the
// under-count to Unverifiable rather than a false publisher Violation (F2).
func (e *Engine) snapPortDirty() bool {
	pt, ok := e.ports[core.PortSnapshot]
	return ok && pt.dirtyWindow
}

// Process is called once per decoded frame. It enqueues the intake tuple into
// the per-port reorder buffer. Classification (structural findings + tier1 +
// seq detector rules) runs when items are popped from the buffer in seq order.
func (e *Engine) Process(f *wire.Frame, port core.Port, sf []wire.StructFinding) {
	pt := e.portTrack(port)
	tuple := intakeTuple{frame: f, port: port, structFindings: sf}

	res := pt.enqueue(tuple, e.cfg.ReorderWindow)
	if res.quarantine {
		// Older-era straggler or ambiguous: account as transport loss, never classify.
		e.rep.TransportLoss(port)
		return
	}

	// Step 1: classify old-era drained items BEFORE advancing era. This ensures
	// old-era gap/seq detection (transport loss for forward gaps, SEQ_RESET_GAP
	// for backward motion) fires with the old tracker state still intact.
	for _, item := range res.preDrainItems {
		e.classify(item, pt)
	}

	// Step 2: advance era and enqueue the new-era item (for eraNewer transition).
	if res.advanceEra {
		pt.advanceEra(res.newEra)
		// Reset Count is channel-wide. The FIRST port (mktdata, refdata, OR snapshot)
		// to observe the era advance wipes all per-instrument MBO state, idempotently
		// (onResetCountForEra is a no-op for ports that advance into the same era
		// later). This keeps the three independent ports consistent: new-era frames
		// on ANY port are never checked against stale old-era trackers. Handling only
		// the mktdata-leads case (the prior approach) left a false-positive window
		// when the snapshot port observed the reset first (F4).
		if e.cfg.Feed == core.FeedMBO {
			e.ensureMBO()
			wiped := e.mbo.onResetCountForEra(res.newEra)
			// If a NON-snapshot port led the reset and actually wiped MBO state, any
			// old-era snapshot group was just dropped and old-era SnapshotOrder/End
			// frames may still be in flight on the snapshot port. Taint the snapshot
			// port so orphan-grouping checks (which gate on gateDetectorSnap) treat
			// those stragglers as Unverifiable rather than a false BEGIN_ORDER_END
			// Violation, until the snapshot port observes its own era advance (which
			// clears dirtyWindow). When the snapshot port itself leads the reset it has
			// already advanced its own era, so the `port != PortSnapshot` guard avoids
			// re-tainting it (which would wrongly mark its clean new-era groups). (F4)
			if wiped && port != core.PortSnapshot {
				if snapPT, ok := e.ports[core.PortSnapshot]; ok {
					snapPT.dirtyWindow = true
				}
			}
			// FRAME.MKTDATA_SEQ_START: on a Reset Count change, the mktdata-port
			// Sequence Number must restart at 0 (this is a mktdata-port rule only).
			if port == core.PortMktData && f.Header.Sequence != 0 {
				e.beginFrame(f.Header.SchemaVersion)
				e.Emit("FRAME.MKTDATA_SEQ_START", core.Violation, port, f.Header.Sequence, f.Header.ChannelID, 0,
					fmt.Sprintf("Reset Count changed but first mktdata seq is %d (expected 0)",
						f.Header.Sequence))
			}
		}
		for _, item := range pt.pushAndPop(tuple, e.cfg.ReorderWindow) {
			e.classify(item, pt)
		}
		return
	}

	// Step 3: classify any items popped by window overflow (same-era normal path).
	for _, item := range res.postDrainItems {
		e.classify(item, pt)
	}
}

// classify runs the full classifier pipeline on a single popped buffer item,
// in the order specified by the plan:
//
//  1. observe (seq/ts tracking)
//  2. divergent dup → emit ONLY FRAME.SEQ_DUP_DIVERGENT, drop
//  3. identical dup → silent drop
//  4. accepted → emit buffered non-transport structFindings; gap → transport loss;
//     checkTier1; feed-validator hook (TODO)
func (e *Engine) classify(item *bufferItem, pt *portTracker) {
	f := item.tuple.frame
	port := item.tuple.port
	sf := item.tuple.structFindings

	// Capture previous SendTS before observe() updates it.
	var prevSendTS *uint64
	if pt.lastSendTS != nil {
		cp := *pt.lastSendTS
		prevSendTS = &cp
	}

	res := pt.observe(f.Header.Sequence, f.Header.SendTS, f.Raw, item.era)

	// (2) Divergent duplicate: emit only SEQ_DUP_DIVERGENT, then drop.
	if res.isDivergentDup {
		e.beginFrame(f.Header.SchemaVersion)
		e.Emit("FRAME.SEQ_DUP_DIVERGENT", core.Violation, port, f.Header.Sequence, f.Header.ChannelID, 0,
			fmt.Sprintf("seq %d repeated with different payload (era %d)", f.Header.Sequence, item.era))
		return // early stop: no structural findings, no checkTier1, no state mutation
	}

	// (3) Identical duplicate: silent drop.
	if res.isDup {
		return
	}

	// (4) Accepted frame. By the time classify is called, pt.era matches
	// item.era: old-era items are classified before advanceEra() in Process,
	// and new-era items are classified after. So seq tracking is always active.
	e.beginFrame(f.Header.SchemaVersion)

	// FRAME.SEQ_RESET_GAP: backward seq motion without a reset-count change is
	// a publisher violation. A plain forward gap is transport loss (not a violation).
	// res.gapBefore is true for forward gaps; backward motion (seq < lastSeq)
	// without an era change is flagged here.
	if pt.lastSeq != nil && f.Header.Sequence < *pt.lastSeq {
		e.Emit("FRAME.SEQ_RESET_GAP", core.Violation, port, f.Header.Sequence, f.Header.ChannelID, 0,
			fmt.Sprintf("seq %d < last %d without reset-count change (era %d)",
				f.Header.Sequence, *pt.lastSeq, pt.era))
	}

	// Forward gap: transport loss.
	if res.gapBefore {
		pt.dirtyWindow = true
		e.rep.TransportLoss(port)
	}

	// FRAME.SEND_TS_MONOTONIC: Send Timestamp must be non-decreasing across
	// increasing seq. Only checked when seq is advancing (not backward motion);
	// use prevSendTS captured before observe() updated pt.lastSendTS.
	// Backward-seq frames (SEQ_RESET_GAP) are excluded: the ts rule is
	// "across increasing seq", and observe() only updates lastSendTS on advance.
	if prevSendTS != nil && f.Header.SendTS < *prevSendTS &&
		(pt.lastSeq == nil || f.Header.Sequence >= *pt.lastSeq) {
		e.Emit("FRAME.SEND_TS_MONOTONIC", core.Violation, port, f.Header.Sequence, f.Header.ChannelID, 0,
			fmt.Sprintf("SendTS %d < previous %d at seq %d",
				f.Header.SendTS, *prevSendTS, f.Header.Sequence))
	}

	// Emit buffered structural findings (non-transport first).
	for _, s := range sf {
		if s.Transport {
			e.rep.TransportCorruption(port, s.RuleID)
			// F2: transport corruption taints the port's verifiability window.
			// A corrupted frame on the snapshot port means the snapshot data
			// (e.g. order counts) may be untrustworthy, so the snapshot port is
			// treated the same as a gap — dirtyWindow = true prevents false-positive
			// Violations from snapshot under-count and related rules.
			pt.dirtyWindow = true
			// The SNAP.TOTAL_ORDERS_COUNT_MATCH under-count check gates on the
			// in-flight group's own dirty flag, not the port flag, so also taint the
			// currently-open snapshot group for this channel (if any). A truncated
			// snapshot frame can drop trailing SnapshotOrders without leaving a
			// snapshot-seq gap, so without this the under-count would falsely
			// classify as a publisher Violation rather than Unverifiable (F2).
			if port == core.PortSnapshot && e.mbo != nil {
				if open := e.mbo.openSnaps[f.Header.ChannelID]; open != nil {
					open.dirty = true
				}
			}
			continue
		}
		e.Emit(s.RuleID, core.Violation, port, f.Header.Sequence, f.Header.ChannelID, 0, s.Detail)
	}

	e.checkTier1(f, port) // engine/tier1.go

	// HEARTBEAT.CADENCE (Task 15): on the mktdata port, check that Heartbeat
	// messages are not more than ExpectHeartbeat apart.  Timing uses SendTS
	// (wire nanoseconds).  Gate on mktdata seq contiguity (dirtyWindow).
	if port == core.PortMktData && e.cfg.ExpectHeartbeat > 0 {
		e.checkHeartbeatCadence(f, port, pt)
	}

	// Route refdata-port frames to the reference-data state machine (Task 14+15).
	if port == core.PortRefData {
		e.processRefdataFrame(f, pt)
	}

	// Per-feed validator routing (mktdata port only).
	if port == core.PortMktData {
		switch e.cfg.Feed {
		case core.FeedTOB:
			e.checkTOB(f, port, f.Header.ChannelID)
		case core.FeedMidpoint:
			e.checkMidpoint(f, port, f.Header.ChannelID)
		case core.FeedMBO:
			e.checkMBO(f, f.Header.ChannelID)
		}
	}

	// Snapshot-port routing: MBO snapshot counters rules.
	if port == core.PortSnapshot && e.cfg.Feed == core.FeedMBO {
		e.checkMBOSnapshot(f, f.Header.ChannelID, f.Header.Sequence)
	}
}

// checkHeartbeatCadence checks HEARTBEAT.CADENCE on the mktdata port.
// Called from classify only when port == PortMktData and ExpectHeartbeat > 0.
// Timing uses frame SendTS (nanoseconds). A gap > ExpectHeartbeat between
// consecutive Heartbeat frames is a violation; a seq gap on the mktdata port
// downgrades to Unverifiable.
func (e *Engine) checkHeartbeatCadence(f *wire.Frame, port core.Port, pt *portTracker) {
	for _, m := range f.Messages {
		if m.Type != wire.TypeHeartbeat {
			continue
		}
		sendTS := f.Header.SendTS
		ch := f.Header.ChannelID
		if e.lastHbSendTSSet && sendTS >= e.lastHbSendTS {
			gap := time.Duration(sendTS-e.lastHbSendTS) * time.Nanosecond
			if gap > e.cfg.ExpectHeartbeat {
				st := core.Violation
				reason := ""
				if pt.dirtyWindow {
					st = core.Unverifiable
					reason = "loss"
				}
				e.Emit("HEARTBEAT.CADENCE", st, port, f.Header.Sequence, ch, 0,
					fmt.Sprintf("heartbeat gap %v exceeds --expect-heartbeat %v",
						gap, e.cfg.ExpectHeartbeat), reason)
			}
		}
		e.lastHbSendTS = sendTS
		e.lastHbSendTSSet = true
		break // one heartbeat per frame is the norm; a second would be caught by tier1
	}
}

// Flush drains all per-port reorder buffers in seq order through the classifier.
// run.go calls this at EOF/SIGINT before EndRun.
//
// F3 (determinism): ports are drained in ascending port-index order so that
// findings appear in a consistent, reproducible order regardless of Go's map
// iteration randomness. The canonical order is: PortMktData < PortRefData <
// PortSnapshot, which mirrors the priority of cross-port checks (mktdata state
// is established before snapshot state is evaluated at Flush time).
func (e *Engine) Flush() {
	for _, port := range []core.Port{core.PortMktData, core.PortRefData, core.PortSnapshot} {
		pt, ok := e.ports[port]
		if !ok {
			continue
		}
		for _, item := range pt.drainAll() {
			e.classify(item, pt)
		}
	}
}

// EndRun runs end-of-observation checks.
// run.go calls Flush() then EndRun() before reporting.
//
// SNAP.BEGIN_ORDER_END_GROUPING (Task 21): any snapshot group still open at
// end-of-stream (SnapshotBegin without SnapshotEnd) is a grouping violation.
//
// RESET.SNAPSHOT_FOLLOWS (Task 22): any instrument still awaiting a recovery
// snapshot at end-of-stream fires this rule.
//
// SNAP.ROUND_ROBIN_COVERS_MANIFEST (Task 22): after ≥2 clean snapshot cycles,
// any manifest-ready instrument with no completed snapshots fires this rule.
//
// REFDATA.NEVER_REACHES_READY (Task 15): for each channel that was observed long
// enough (≥ ExpectManifestCadence + ExpectDefinitionCycle of wire time) on a
// gapless refdata port but never reached ready(), emit a violation.
func (e *Engine) EndRun() {
	// Flush any snapshot groups that were opened but never closed.
	e.flushOpenSnaps()

	// Task 22: reset-recovery and round-robin end-of-run checks.
	e.checkResetSnapshotFollows()
	e.checkRoundRobinCoversManifest()

	if e.refdata == nil {
		return
	}
	if e.cfg.ExpectManifestCadence == 0 || e.cfg.ExpectDefinitionCycle == 0 {
		return
	}
	window := e.cfg.ExpectManifestCadence + e.cfg.ExpectDefinitionCycle
	refPT, hasPT := e.ports[core.PortRefData]
	for ch, s := range e.refdata.channels {
		if s.everReady || !s.firstSendTSSet || !s.lastManifestSendTSSet {
			continue
		}
		if s.lastManifestSendTS < s.firstSendTS {
			// Wire timestamps regressed (FRAME.SEND_TS_MONOTONIC fires separately);
			// skip this channel rather than computing a spurious negative/huge span.
			continue
		}
		span := time.Duration(s.lastManifestSendTS-s.firstSendTS) * time.Nanosecond
		if span < window {
			continue
		}
		// Gate: if the refdata port has a dirty window, downgrade.
		dirty := hasPT && refPT.dirtyWindow
		st := core.Violation
		reason := ""
		if dirty {
			st = core.Unverifiable
			reason = "loss"
		}
		e.Emit("REFDATA.NEVER_REACHES_READY", st, core.PortRefData, 0, ch, 0,
			fmt.Sprintf("channel %d: observed %v (≥ window %v) but never reached ready state",
				ch, span, window), reason)
	}
}
