package engine

import (
	"fmt"
	"net/netip"
	"slices"
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
	// reorder buffers and seq trackers, one per channel instance — per (source
	// address, port, channel). Sequencing keys on the instance and never on the
	// channel (GLOSSARY.md): two publishers may serve one channel on one group and
	// one port, each advancing its own sequence space, and merging their series
	// reads as continuous loss on every alternation.
	ports map[instanceKey]*portTracker
	// seenCounter stamps trackers for eviction order; see instanceTrack.
	seenCounter uint64
	// refdata holds the reference-data set-state machine (Task 14 + 15).
	// Lazily initialized on the first PortRefData frame.
	refdata *refdataState
	// mbo holds per-instrument MBO sequencing state (Task 18+).
	// Lazily initialized on the first MBO mktdata frame.
	mbo *mboState
	// Market-by-price consumer state; lazily initialised by ensureMBP.
	mbp *mbpState
	// sessionEnd holds the channels that have announced EndOfSession on mktdata.
	// The refdata state machine reads it to tell the shutdown ManifestSummary the
	// spec mandates from a mid-service Valid drop; see resolveValidZero.
	sessionEnd map[uint8]struct{}
}

// New constructs an Engine with the given config and reporter.
func New(cfg Config, rep report.Reporter) *Engine {
	if cfg.ReorderWindow <= 0 {
		cfg.ReorderWindow = 8
	}
	return &Engine{
		cfg:        cfg,
		rep:        rep,
		now:        time.Now,
		ports:      make(map[instanceKey]*portTracker),
		sessionEnd: make(map[uint8]struct{}),
	}
}

// MagicFor returns the expected frame magic for the given feed.
func MagicFor(feed core.Feed) uint16 {
	switch feed {
	case core.FeedTOB:
		return wire.MagicTOB
	case core.FeedMidpoint:
		return wire.MagicMid
	case core.FeedMBP:
		return wire.MagicMBP
	default: // FeedMBO
		return wire.MagicMBO
	}
}

// beginFrame marks whether this frame's schema is one we implement, which gates
// the downgrade of version-specific rules in Emit.
//
// The test is deliberately > and not !=, i.e. only a *future* schema downgrades.
// A stale lower one (a publisher still emitting Schema Version 1 after its feed
// moved to 2) does produce derived noise: a per-type length violation, and a
// short body that makes Manifest Seq read as 0 and trip the refdata rules. But
// != would suppress every non-envelope rule on such a stream, and those rules
// are still finding real defects — on the bundled pre-2.0 nonconformant_mbp
// capture it silently drops MSG.SNAPSHOT_FLAG_MATCHES_PORT from 6 violations to
// 0. A rule that stops running reports the same thing as a rule that checked
// everything and found nothing, which is the failure mode this tool is built to
// avoid (see "Coverage vs. silence" in the README). Noise is recoverable;
// silence is not. FRAME.SCHEMA_VERSION is an envelope rule, so the actionable
// finding is present at full severity under either choice.
//
// Validating a capture from a prior MAJOR properly needs multi-version decode
// support, which is a feature, not a gate tweak.
func (e *Engine) beginFrame(schemaVersion uint8) {
	e.curUnknownSchema = schemaVersion > wire.ExpectedSchemaVersion(MagicFor(e.cfg.Feed))
}

// Emit resolves severity from the registry, applying two downgrades:
//   - Conditional must* rules downgrade unless their --expect-* config is set.
//   - Under an unknown (higher) schema version, non-envelope checks downgrade.
//
// port and seq are the logical port and frame sequence number of the frame being
// classified; they are recorded on the Finding for logging and debugging.
// reason is an optional core.Reason* value used for the unverifiable_total
// Prometheus label; at most one is used. Prefer the passed/unverified/inapplicable
// helpers in denominator.go, which pick the status and reason together.
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

// maxChannelInstances bounds the tracker map. The source address is part of the
// key and is not under our control: an any-source join accepts datagrams from
// any sender, and a publisher's address is a tunnel lease that gets reassigned
// under a live host. The cap is far above any real deployment (two arms × three
// ports × the 256 channels a u8 allows is 1,536) so only a pathological sender
// reaches it.
//
// Reaching it is not free. dirtyOn scans the map and gateDetector consults it
// several times per message, so a sender that fills the map makes every message
// pay a handful of 4,096-entry scans and every new instance one eviction scan:
// throughput degrades linearly with the flood, while no result changes. A dirty
// count keyed (port, channel) would keep the scans flat and is deliberately not
// here — it denormalises the one flag whose bookkeeping both false-Must bugs above
// came from, and a stale count is either a stuck gate (everything Unverifiable) or
// a cleared one (a Must the publisher never earned). Worth building when a real
// deployment approaches the cap, with the count as the state itself rather than a
// cache of it.
const maxChannelInstances = 4096

// instanceTrack returns (or lazily creates) the tracker for one channel instance.
//
// A first datagram from an unseen source opens a fresh series: no last sequence,
// no era, a clean window. That is the point — an address change (a reassigned
// lease, a redeployed publisher) must read as a new instance and stay silent,
// never as a gap or a backward jump in the old one.
func (e *Engine) instanceTrack(src netip.Addr, port core.Port, ch uint8) *portTracker {
	key := instanceKey{src: src, port: port, ch: ch}
	pt, ok := e.ports[key]
	if !ok {
		if len(e.ports) >= maxChannelInstances {
			e.evictOldestInstance()
		}
		pt = newPortTracker(e.cfg.ReorderWindow)
		e.ports[key] = pt
	}
	e.seenCounter++
	pt.lastSeen = e.seenCounter
	return pt
}

// evictOldestInstance drops the least-recently-seen instance to keep the map at
// its cap. Anything still buffered for it is accounted as transport loss rather
// than dropped quietly, the same as a quarantined straggler: it was received and
// never judged. If the instance returns it starts a fresh series, silently.
func (e *Engine) evictOldestInstance() {
	var oldest instanceKey
	var oldestPT *portTracker
	for k, pt := range e.ports {
		if oldestPT == nil || pt.lastSeen < oldestPT.lastSeen {
			oldest, oldestPT = k, pt
		}
	}
	if oldestPT == nil {
		return
	}
	for range oldestPT.drainAll() {
		e.rep.TransportLoss(oldest.port)
	}
	delete(e.ports, oldest)
}

// instancesOn lists the instances observed on a port, ordered by channel then
// source, so the callers that iterate a whole port do so deterministically (F3).
func (e *Engine) instancesOn(port core.Port) []instanceKey {
	var keys []instanceKey
	for k := range e.ports {
		if k.port == port {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b instanceKey) int {
		if a.ch != b.ch {
			return int(a.ch) - int(b.ch)
		}
		return a.src.Compare(b.src)
	})
	return keys
}

// dirtyOn reports whether any instance of this (port, channel) has a tainted
// verifiability window.
//
// The OR across instances is deliberate and is the counterpart of what this
// change does NOT rescope: per-instrument, book and reference-data state stay
// keyed by channel, so the state a gated rule judges is fed by every instance of
// that channel and any one of them losing datagrams can explain an anomaly in it.
// Gating per instance there would report a Violation the loss could account for.
// HEARTBEAT.CADENCE is the exception and gates on its own instance's window,
// because its baseline is per instance too.
func (e *Engine) dirtyOn(port core.Port, ch uint8) bool {
	for k, pt := range e.ports {
		if k.port == port && k.ch == ch && pt.dirtyWindow {
			return true
		}
	}
	return false
}

// taintOn marks every instance of one channel on one port unverifiable for the
// rest of its era. It is the write counterpart of dirtyOn and has to match it:
// every reader ORs across the channel's instances, so a taint written under one
// exact source address is a taint no reader is looking for when the channel's
// series sits under another one — and nothing about a channel keeps its ports on
// one address. The lease this keying exists for is enough on its own: the mktdata
// series moves to a reassigned address while the lower-rate snapshot series is
// still keyed under the old one (F2).
func (e *Engine) taintOn(port core.Port, ch uint8) {
	for k, pt := range e.ports {
		if k.port == port && k.ch == ch {
			pt.dirtyWindow = true
		}
	}
}

// taintPortWide marks every instance on a port unverifiable for the rest of its
// era. It is what a transport finding calls for, because a transport finding is
// the one case where the channel the datagram belonged to is unknowable:
// wire.Decode returns an all-zero header for a datagram shorter than the frame
// header (wire/decode.go), so the frame carrying the finding names channel 0 and
// in truth names no channel at all. Tainting that frame's own instance writes the
// taint on a phantom instance while dirtyOn — which ORs across the real channel —
// stays clean, and the truncated snapshot that dropped trailing SnapshotOrders is
// then graded as a publisher Violation on two Must rules (F1).
//
// Port-wide is what the one-tracker-per-port state did before frame state was
// keyed per instance, and it is the safe direction: the cost of tainting an
// instance the corruption did not touch is a rule that grades Unverifiable
// instead of pass, never a violation the publisher did not commit.
func (e *Engine) taintPortWide(port core.Port) {
	for k, pt := range e.ports {
		if k.port == port {
			pt.dirtyWindow = true
		}
	}
}

// mktdataPending reports whether any mktdata reorder buffer on this channel still
// holds unclassified frames. Cross-port snapshot checks that compare a snapshot against
// mktdata-derived state (SNAP.ANCHOR_IS_MKTDATA_SEQ, SNAP.LAST_INSTRUMENT_SEQ_
// CONSISTENT_WITH_DELTAS) must downgrade to Unverifiable when this is true, even
// on a gapless channel: a still-buffered mktdata frame could carry the anchor seq
// or the deltas up to K, so the current mktdata state is not yet as-of-anchor (F3).
func (e *Engine) mktdataPending(ch uint8) bool {
	for k, pt := range e.ports {
		if k.port == core.PortMktData && k.ch == ch && pt.buf.Len() > 0 {
			return true
		}
	}
	return false
}

// snapPortDirty reports whether the channel's snapshot verifiability window is
// tainted (a snapshot-seq gap or transport corruption occurred this era). Used
// by the SNAP.TOTAL_ORDERS_COUNT_MATCH under-count gate so a truncated snapshot
// frame — whose header may not decode a channel — still downgrades the
// under-count to Unverifiable rather than a false publisher Violation (F2).
func (e *Engine) snapPortDirty(ch uint8) bool {
	return e.dirtyOn(core.PortSnapshot, ch)
}

// Process is called once per decoded frame. It enqueues the intake tuple into
// the reorder buffer of its (port, channel) series. Classification (structural
// findings + tier1 + seq detector rules) runs when items are popped from the
// buffer in seq order.
func (e *Engine) Process(src netip.Addr, f *wire.Frame, port core.Port, sf []wire.StructFinding) {
	pt := e.instanceTrack(src, port, f.Header.ChannelID)
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
		// MBP handles the reset from classify instead (see mbpObserveEra): it must
		// also see the era a port *seeds* without advancing, which never reaches here.
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
				e.taintOn(core.PortSnapshot, f.Header.ChannelID)
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

	// Channel-wide reset bookkeeping for MBP, driven by every accepted frame on any
	// port rather than by the per-series era advance in Process.
	e.mbpObserveEra(port, f.Header.ChannelID, item.era)

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
			//
			// Port-wide and not this instance: a truncated datagram has no
			// trustworthy Channel ID, so the instance it was keyed under may not
			// exist on the wire at all. See taintPortWide (F1).
			e.taintPortWide(port)
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
		e.observeSessionEnd(f)
		switch e.cfg.Feed {
		case core.FeedTOB:
			e.checkTOB(f, port, f.Header.ChannelID)
		case core.FeedMidpoint:
			e.checkMidpoint(f, port, f.Header.ChannelID)
		case core.FeedMBO:
			e.checkMBO(f, f.Header.ChannelID)
		case core.FeedMBP:
			e.checkMBP(f, f.Header.ChannelID)
		}
	}

	// Snapshot-port routing: MBO snapshot counters rules.
	if port == core.PortSnapshot && e.cfg.Feed == core.FeedMBO {
		e.checkMBOSnapshot(f, f.Header.ChannelID, f.Header.Sequence)
	}
	if port == core.PortSnapshot && e.cfg.Feed == core.FeedMBP {
		e.checkMBPSnapshot(f, f.Header.ChannelID, f.Header.Sequence)
	}
}

// observeSessionEnd records that a channel announced the end of its session.
// EndOfSession is per channel ("no more data on this channel for the current
// session") and carries no channel field of its own, so the frame header names it.
//
// Only a mktdata placement counts. Anywhere else the message is already
// MSG.WRONG_PORT_PLACEMENT, and a misplaced one must not excuse a refdata finding.
func (e *Engine) observeSessionEnd(f *wire.Frame) {
	for _, m := range f.Messages {
		if m.Type == wire.TypeEndOfSession {
			e.sessionEnd[f.Header.ChannelID] = struct{}{}
			return
		}
	}
}

// mktdataObserved reports whether any mktdata frame arrived on this channel — i.e.
// whether an EndOfSession could have been witnessed at all. False under
// --refdata-port with no --mktdata-port, where the absence of a session-end
// signal says nothing about the publisher.
func (e *Engine) mktdataObserved(ch uint8) bool {
	for k := range e.ports {
		if k.port == core.PortMktData && k.ch == ch {
			return true
		}
	}
	return false
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
		if pt.lastHbSendTSSet && sendTS >= pt.lastHbSendTS {
			gap := time.Duration(sendTS-pt.lastHbSendTS) * time.Nanosecond
			if gap > e.cfg.ExpectHeartbeat {
				st := core.Violation
				reason := ""
				if pt.dirtyWindow {
					st = core.Unverifiable
					reason = core.ReasonLoss
				}
				e.Emit("HEARTBEAT.CADENCE", st, port, f.Header.Sequence, ch, 0,
					fmt.Sprintf("heartbeat gap %v exceeds --expect-heartbeat %v",
						gap, e.cfg.ExpectHeartbeat), reason)
			}
		}
		pt.lastHbSendTS, pt.lastHbSendTSSet = sendTS, true
		break // one heartbeat per frame is the norm; a second would be caught by tier1
	}
}

// Flush drains every (port, channel) reorder buffer in seq order through the
// classifier.
// run.go calls this at EOF/SIGINT before EndRun.
//
// F3 (determinism): ports are drained in ascending port-index order so that
// findings appear in a consistent, reproducible order regardless of Go's map
// iteration randomness. The canonical order is: PortMktData < PortRefData <
// PortSnapshot, which mirrors the priority of cross-port checks (mktdata state
// is established before snapshot state is evaluated at Flush time). Within a
// port, instances are drained in (channel, source) order for the same reason.
func (e *Engine) Flush() {
	for _, port := range []core.Port{core.PortMktData, core.PortRefData, core.PortSnapshot} {
		for _, key := range e.instancesOn(port) {
			pt := e.ports[key]
			for _, item := range pt.drainAll() {
				e.classify(item, pt)
			}
		}
	}
}

// EndRun runs end-of-observation checks.
// run.go calls Flush() then EndRun() before reporting.
//
// SNAP.BEGIN_ORDER_END_GROUPING (Task 21): any snapshot group still open at
// end-of-stream (SnapshotBegin without SnapshotEnd) is a grouping violation.
//
// MBP.SNAP.GROUP_STRUCTURE: the market-by-price equivalent, reported as
// Unverifiable — see flushOpenMBPSnaps for why the two differ.
//
// RESET.SNAPSHOT_FOLLOWS (Task 22): any instrument still awaiting a recovery
// snapshot at end-of-stream fires this rule.
//
// SNAP.ROUND_ROBIN_COVERS_MANIFEST (Task 22): after ≥2 clean snapshot cycles,
// any manifest-ready instrument with no completed snapshots fires this rule.
//
// REFDATA.VALID_FLAG_WHILE_SERVING: each channel's held Valid=0 verdict, which
// could not be decided when the summary arrived because the EndOfSession that
// settles it comes from the other port.
//
// REFDATA.NEVER_REACHES_READY (Task 15): the serving period each channel still had
// open at end of stream. Periods a Valid=0 closed were already reported when the
// next one opened, and a period whose window closed mid-run was reported there; see
// decideNeverReachesReady.
func (e *Engine) EndRun() {
	// Flush any snapshot groups that were opened but never closed.
	e.flushOpenSnaps()
	e.flushOpenMBPSnaps()

	// Task 22: reset-recovery and round-robin end-of-run checks.
	e.checkResetSnapshotFollows()
	e.checkRoundRobinCoversManifest()

	if e.refdata == nil {
		return
	}
	e.refdata.resolveShutdownVerdicts()

	for ch, s := range e.refdata.channels {
		// Seq 0: end of stream is not a datagram, so there is nothing to point at.
		e.decideNeverReachesReady(ch, s, 0, true)
	}
}

// decideNeverReachesReady reports REFDATA.NEVER_REACHES_READY for one serving period
// of one channel, measured between its first and last Valid=1 summary, at most once
// per period. Called per refdata datagram, from onManifestSummary for a period a
// Valid=0 closed and a later Valid=1 replaced, and from EndRun for the period still
// open at end of stream.
//
// **It runs during the run, not only at the end.** The rule's own summary is that a
// fresh both-port subscriber reaches ready() within manifest cadence + cycle, and
// that is decidable the moment the window closes — waiting for EndRun makes the rule
// unreachable in the deployment it matters most in, because a `dz-conformance@`
// systemd instance never ends (#50). A checker that only speaks at exit cannot alert.
//
// `final` is true from EndRun and from the close of a superseded period. The three
// non-terminal outcomes — no manifest yet, regressed timestamps, a span still short
// of the window — are answers to "not yet", so mid-period they defer rather than
// report; more wire may still settle them. The two terminal ones report as soon as
// they are true:
//
//   - ready reached inside the window → Pass, at the datagram that reached it.
//   - ready reached past it, or the window closed without ready → Violation, at the
//     datagram that settled it.
//
// Each period is one opportunity, and each of the five ways out below reports it. A
// period that reached ready is the rule *passing* — the reason it was silent is that
// the check is written as a search for failures, which is exactly the shape that
// makes coverage and no-op indistinguishable (engine/denominator.go).
//
// Deciding at the window rather than at exit also fixes a masking bug: a period that
// reached ready long AFTER the window used to report Pass, because EndRun only asked
// whether ready was ever reached, not whether it was reached in time. Closing it takes
// the readiness timestamp, not the span: the span ends at the last Valid=1 summary, so
// where none lands between the window closing and a late readiness there is no span
// past the window to catch it.
func (e *Engine) decideNeverReachesReady(ch uint8, s *channelRefdataState, frameSeq uint64, final bool) {
	if s.neverReadyDecided {
		return
	}
	// An unknown (higher) schema version downgrades every non-envelope rule for the
	// datagram being classified, and neverReadyDecided would latch that downgrade for
	// the whole serving period: one datagram from a mid-upgrade publisher, landing at
	// the moment the window closes, would record the Violation as NA/Info and no later
	// datagram could restate it. Defer instead, so a clean one decides the period.
	// Not when final: EndRun and a closed period are the last word, and deferring
	// there would trade the downgrade for the silence this whole change exists to fix.
	if e.curUnknownSchema && !final {
		return
	}
	if e.cfg.ExpectManifestCadence == 0 || e.cfg.ExpectDefinitionCycle == 0 {
		return
	}
	window := e.cfg.ExpectManifestCadence + e.cfg.ExpectDefinitionCycle
	if s.everReady {
		s.neverReadyDecided = true
		// Ready alone is not a Pass — the rule grades a deadline. Compare the moment
		// readiness was reached against the window, because the span below cannot see
		// a late ready that no later Valid=1 summary follows.
		if s.firstSendTSSet && s.readySendTSSet && s.readySendTS >= s.firstSendTS {
			if elapsed := time.Duration(s.readySendTS-s.firstSendTS) * time.Nanosecond; elapsed > window {
				st := core.Violation
				reason := ""
				if e.dirtyOn(core.PortRefData, ch) {
					st = core.Unverifiable
					reason = core.ReasonLoss
				}
				e.Emit("REFDATA.NEVER_REACHES_READY", st, core.PortRefData, frameSeq, ch, 0,
					fmt.Sprintf("channel %d reached ready after %v, past the %v window", ch, elapsed, window), reason)
				return
			}
		}
		e.passed("REFDATA.NEVER_REACHES_READY", core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("channel %d reached ready state", ch))
		return
	}
	if !s.firstSendTSSet || !s.lastServingSendTSSet {
		if !final {
			return
		}
		s.neverReadyDecided = true
		e.unverified("REFDATA.NEVER_REACHES_READY", core.ReasonColdStart, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("channel %d: no ManifestSummary observed, so the observation span is unknown", ch))
		return
	}
	if s.lastServingSendTS < s.firstSendTS {
		// Wire timestamps regressed (FRAME.SEND_TS_MONOTONIC fires separately);
		// skip this period rather than computing a spurious negative/huge span.
		if !final {
			return
		}
		s.neverReadyDecided = true
		e.unverified("REFDATA.NEVER_REACHES_READY", core.ReasonSuperseded, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("channel %d: wire timestamps regressed, so the span is not measurable (FRAME.SEND_TS_MONOTONIC reports it)", ch))
		return
	}
	span := time.Duration(s.lastServingSendTS-s.firstSendTS) * time.Nanosecond
	if span < window {
		if !final {
			return
		}
		s.neverReadyDecided = true
		e.unverified("REFDATA.NEVER_REACHES_READY", core.ReasonInsufficientWindow, core.PortRefData, frameSeq, ch, 0,
			fmt.Sprintf("channel %d: observed %v, less than the %v a publisher needs to reach ready", ch, span, window))
		return
	}
	// Gate: if any refdata window on this channel is dirty, downgrade.
	dirty := e.dirtyOn(core.PortRefData, ch)
	st := core.Violation
	reason := ""
	if dirty {
		st = core.Unverifiable
		reason = core.ReasonLoss
	}
	s.neverReadyDecided = true
	e.Emit("REFDATA.NEVER_REACHES_READY", st, core.PortRefData, frameSeq, ch, 0,
		fmt.Sprintf("channel %d: observed %v (≥ window %v) but never reached ready state",
			ch, span, window), reason)
}
