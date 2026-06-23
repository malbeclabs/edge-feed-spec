package engine

// gate.go — Per-instrument sequence trackers and the verifiability gate.
//
// The verifiability gate answers a single question for each per-instrument
// anomaly: "is this anomaly definitely a publisher fault, or could transport
// loss explain it?"
//
// The gate consults the mktdata port's dirtyWindow flag (set by the channel-seq
// tracker in state.go when a forward gap is declared in the reorder window). When
// the channel was gapless, a per-instrument seq jump must be the publisher's
// fault. When the channel had a gap, the missing frames could have carried the
// missing deltas, so the anomaly is Unverifiable (loss-explained).
//
// State layout:
//
//	instrTrackerKey  (channelID, instrumentID) → instrTracker
//	instrTracker     { lastInstrSeq *uint32, status }
//
// Era-boundary semantics:
//
//	onResetCount()                  — wipe ALL per-instrument trackers.
//	onManifestBump(ch, survivors)   — drop trackers for removed instruments on
//	                                  channel ch; retain survivors' trackers.
//	onInstrumentReset(ch, instrID)  — reset only that instrument's tracker and
//	                                  mark it as awaiting (first-value check reset).

import (
	"errors"
	"fmt"
	"hash/maphash"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// instrPayloadRing is a small ring buffer mapping per-instrument seq → payload hash,
// used by DELTA.PERINSTR_DUP_DIVERGENT to detect replayed deltas with different payloads.
const instrPayloadRingCap = 16

type instrPayloadRing struct {
	keys [instrPayloadRingCap]uint32 // per-instrument seq
	vals [instrPayloadRingCap]uint64 // payload hash
	pos  int                         // next write position
	size int                         // number of valid entries
}

func (r *instrPayloadRing) record(seq uint32, h uint64) {
	r.keys[r.pos] = seq
	r.vals[r.pos] = h
	r.pos = (r.pos + 1) % instrPayloadRingCap
	if r.size < instrPayloadRingCap {
		r.size++
	}
}

// lookup returns (hash, true) if seq is found in the ring.
func (r *instrPayloadRing) lookup(seq uint32) (uint64, bool) {
	for i := range r.size {
		idx := (r.pos - 1 - i + instrPayloadRingCap) % instrPayloadRingCap
		if r.keys[idx] == seq {
			return r.vals[idx], true
		}
	}
	return 0, false
}

// perDeltaPayloadHash computes a quick hash of the delta message body for dup detection.
var instrHashSeed = maphash.MakeSeed()

func perDeltaPayloadHash(m wire.Message) uint64 {
	var h maphash.Hash
	h.SetSeed(instrHashSeed)
	_, _ = h.Write(m.Body)
	return h.Sum64()
}

// snapTracker holds per-(channel, instrument) snapshot counters state.
type snapTracker struct {
	// lastAnchorSeq is the most recent Anchor Seq observed in a SnapshotBegin for this instrument.
	lastAnchorSeq *uint64
	// lastSnapshotID is the most recent Snapshot ID observed for this instrument on the snapshot port.
	lastSnapshotID *uint32
	// lastSnapPortSeq is the snapshot-port frame seq corresponding to lastSnapshotID.
	lastSnapPortSeq *uint64
	// snapCount is the number of completed snapshot groups (SnapshotEnd received) for
	// this instrument in the current era. Used for SNAP.ROUND_ROBIN_COVERS_MANIFEST (Task 22).
	snapCount int
}

// instrTrackerKey is the composite key for the per-instrument tracker map.
type instrTrackerKey struct {
	channelID    uint8
	instrumentID uint32
}

// instrTracker holds per-(channel, instrument) sequencing state.
type instrTracker struct {
	// lastInstrSeq is nil until the first delta is accepted for this instrument.
	lastInstrSeq *uint32
	// seenReset is true if an InstrumentReset for this instrument was observed
	// in-stream (not just inferred from cold start). When true, the first delta
	// after the reset is expected to have seq==1; a non-1 value is a publisher
	// fault (not cold-start ambiguity).
	seenReset bool
	// payloads is a small ring of recent (perInstrSeq → payload hash) entries
	// used for DELTA.PERINSTR_DUP_DIVERGENT detection.
	payloads instrPayloadRing
	// pendingResetAnchor is set when an InstrumentReset was observed; it is the
	// New Anchor Seq from that reset. The next delta for this instrument must
	// have per-instrument seq == recoveryK+1. pendingResetAnchorSet gates use.
	pendingResetAnchor    uint64
	pendingResetAnchorSet bool
	// recoveryK is the Last Instrument Seq from the recovery snapshot that followed
	// the InstrumentReset. Set when we process the matching SnapshotBegin.
	recoveryK    uint32
	recoveryKSet bool
	// bookTrusted is true when the order-id set for this instrument is considered
	// complete and reliable: we have been tracking continuously without gaps since
	// the beginning of the era (perSeq started at 1) or since a recovery snapshot.
	// A per-instrument gap (DELTA.PERINSTR_DENSITY) clears this flag because missing
	// deltas could have carried OrderAdd messages that would populate the live set.
	// gateConsumer requires bookTrusted=true to elevate referential anomalies to Violation.
	bookTrusted bool
	// bookCorruptedByDup is true if a duplicate/late delta (perSeq <= lastInstrSeq)
	// was applied to the live book after the instrument reached a given seq K.
	// A duplicate mutates the book state without advancing lastInstrSeq, so the
	// delta book may differ from the as-of-K snapshot state even when
	// lastInstrSeq == K. The oracle gates on !bookCorruptedByDup to avoid emitting
	// false-positive mismatches caused by replayed deltas (Task 26).
	// Cleared on InstrumentReset (new book lifecycle).
	bookCorruptedByDup bool
	// awaitingRecovery is true when an InstrumentReset was observed and a matching
	// recovery snapshot group has not yet completed. Cleared when handleSnapEnd
	// confirms the recovery group (Task 22: RESET.SNAPSHOT_FOLLOWS,
	// RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET).
	awaitingRecovery       bool
	awaitingRecoveryAnchor uint64 // New Anchor Seq from the InstrumentReset
}

// snapOrderRecord is the full order record collected from a SnapshotOrder message.
// It mirrors restingOrder but is collected during snapshot accumulation for the
// oracle (Task 26).
type snapOrderRecord struct {
	side       uint8
	orderFlags uint8
	enterTS    uint64
	price      int64
	qty        uint64
}

// openSnapshot holds accumulator state for an in-progress snapshot group
// (SnapshotBegin seen, SnapshotEnd not yet received).
type openSnapshot struct {
	instrID    uint32
	snapshotID uint32
	anchorSeq  uint64
	total      uint32 // TotalOrders from SnapshotBegin
	received   uint32 // SnapshotOrder messages counted so far
	// lastInstrSeqK is the Last Instrument Seq from SnapshotBegin. The oracle
	// uses this as the per-instrument seq K at which the snapshot was taken.
	lastInstrSeqK uint32
	// orderIDs tracks Order IDs seen in this group for dup detection.
	orderIDs map[uint64]struct{}
	// orders holds the full SnapshotOrder records collected for the oracle (Task 26).
	orders map[uint64]snapOrderRecord
	// dirty is true if a snapshot-port seq gap was observed DURING this group
	// (i.e. after this group's SnapshotBegin). Pre-group gaps do not set dirty.
	dirty bool
	// structuralViolation is true if any structural violation was detected during
	// this group (ORDER_SNAPSHOT_ID_MATCH, SNAPSHOT_ORDER_NO_DUP_ORDER_ID,
	// EMPTY_BOOK_WELL_FORMED, etc.). The oracle gates on this flag to avoid
	// diffing a malformed snapshot against the delta book (false positive risk).
	structuralViolation bool
	// lastSnapPortSeq is the snapshot-port frame seq of the most-recent frame
	// processed for this group. Used to detect intra-group gaps.
	lastSnapPortSeq uint64
}

// oracleSuspect records an unconfirmed mismatch for one instrument.
// When the same mismatch signature reproduces across OracleConfirmCycles clean
// snapshot groups, it is promoted to a Violation.
type oracleSuspect struct {
	signature string // sorted canonical diff string; empty means "no suspect"
	count     int    // number of consecutive cycles with this exact signature
}

// mboState holds all per-instrument tracker state for an engine run.
// It is lazily initialized by checkMBO.
type mboState struct {
	trackers map[instrTrackerKey]*instrTracker
	// snapTrackers holds per-(channel, instrument) snapshot counters.
	snapTrackers map[instrTrackerKey]*snapTracker
	// openSnaps holds at most one in-progress snapshot group per channel.
	// Keyed by channelID; nil value means no open group on that channel.
	openSnaps map[uint8]*openSnapshot
	// lastBatchID tracks the most recent BatchBoundary Batch ID for (channel).
	// Key is channelID; value is last seen Batch ID and whether it was set.
	lastBatchID    map[uint8]uint32
	lastBatchIDSet map[uint8]bool
	// highestMktdataSeq is the highest mktdata-port frame Sequence observed.
	// Used for SNAP.ANCHOR_IS_MKTDATA_SEQ.
	highestMktdataSeq    uint64
	highestMktdataSeqSet bool
	// book holds the per-instrument live order-id set (Task 20).
	book *bookState
	// maxSnapCycle is the highest snapCount observed across all instruments in the
	// current era. A "cycle" elapses when any instrument's snapCount increases.
	// Used for SNAP.ROUND_ROBIN_COVERS_MANIFEST (Task 22).
	maxSnapCycle int
	// oracleSuspects holds per-(channel, instrument) oracle suspect state (Task 26).
	oracleSuspects map[instrTrackerKey]*oracleSuspect
	// resetEra/resetEraSet track the Reset Count era that onResetCountForEra last
	// wiped state for, so a channel-wide reset observed on ANY port wipes exactly
	// once even though all three ports advance their era independently (F4).
	resetEra    uint8
	resetEraSet bool
}

// onResetCountForEra wipes all per-instrument MBO state for a new Reset Count
// era, idempotently: the FIRST port to observe the era advance performs the wipe;
// subsequent ports advancing into the same era are no-ops. This keeps the three
// independent ports consistent across a channel-wide reset (F4) — without it, a
// snapshot-port era advance would run new-era snapshots against stale old-era
// trackers and falsely fire monotonic/K/oracle checks.
// Returns true if it actually wiped (i.e. this is the first port to observe the
// new era); false if a prior port already wiped for this era.
func (s *mboState) onResetCountForEra(newEra uint8) bool {
	if s.resetEraSet && s.resetEra == newEra {
		return false
	}
	s.resetEra = newEra
	s.resetEraSet = true
	s.onResetCount()
	return true
}

func newMBOState() *mboState {
	return &mboState{
		trackers:       make(map[instrTrackerKey]*instrTracker),
		snapTrackers:   make(map[instrTrackerKey]*snapTracker),
		openSnaps:      make(map[uint8]*openSnapshot),
		lastBatchID:    make(map[uint8]uint32),
		lastBatchIDSet: make(map[uint8]bool),
		book:           newBookState(),
		oracleSuspects: make(map[instrTrackerKey]*oracleSuspect),
	}
}

// tracker returns (lazily creating) the instrTracker for (ch, instrID).
func (s *mboState) tracker(ch uint8, instrID uint32) *instrTracker {
	key := instrTrackerKey{ch, instrID}
	t, ok := s.trackers[key]
	if !ok {
		t = &instrTracker{}
		s.trackers[key] = t
	}
	return t
}

// snapTrack returns (lazily creating) the snapTracker for (ch, instrID).
func (s *mboState) snapTrack(ch uint8, instrID uint32) *snapTracker {
	key := instrTrackerKey{ch, instrID}
	st, ok := s.snapTrackers[key]
	if !ok {
		st = &snapTracker{}
		s.snapTrackers[key] = st
	}
	return st
}

// onResetCount wipes ALL per-instrument trackers. Called when a new era
// (Reset Count change) is observed on the mktdata port.
func (s *mboState) onResetCount() {
	clear(s.trackers)
	clear(s.snapTrackers)
	clear(s.openSnaps)
	clear(s.lastBatchID)
	clear(s.lastBatchIDSet)
	s.highestMktdataSeq = 0
	s.highestMktdataSeqSet = false
	s.maxSnapCycle = 0
	s.book.onResetCount()
	clear(s.oracleSuspects)
}

// onManifestBump retains trackers for surviving instruments and drops trackers
// for instruments removed from the manifest. ch is the refdata channel whose
// manifest seq bumped; survivingInstrIDs is the new def set for that channel.
// Only trackers whose key.channelID == ch are eligible for pruning: instruments
// on other channels are not affected by this channel's manifest change.
func (s *mboState) onManifestBump(ch uint8, survivingInstrIDs map[uint32]struct{}) {
	for key := range s.trackers {
		if key.channelID != ch {
			continue // different channel — not affected by this manifest bump
		}
		if _, ok := survivingInstrIDs[key.instrumentID]; !ok {
			delete(s.trackers, key)
		}
	}
	for key := range s.snapTrackers {
		if key.channelID != ch {
			continue
		}
		if _, ok := survivingInstrIDs[key.instrumentID]; !ok {
			delete(s.snapTrackers, key)
		}
	}
	// Prune oracle suspects for instruments removed from this channel's manifest.
	// A stale suspect from a previous lifecycle must not contribute to confirmation
	// in a subsequent lifecycle (instrument re-added with a new book state).
	for key := range s.oracleSuspects {
		if key.channelID != ch {
			continue
		}
		if _, ok := survivingInstrIDs[key.instrumentID]; !ok {
			delete(s.oracleSuspects, key)
		}
	}
	// Discard any open snapshot group for this channel: the instrument may have
	// been removed from the manifest, and we cannot trust partial-group state.
	delete(s.openSnaps, ch)
	s.book.onManifestBump(ch, survivingInstrIDs)
}

// onInstrumentReset resets the tracker for one instrument, marking it as
// awaiting a fresh seq=1 start. Called when an InstrumentReset (0x14) message
// is processed on the mktdata stream.
func (s *mboState) onInstrumentReset(ch uint8, instrID uint32, newAnchorSeq uint64) {
	key := instrTrackerKey{ch, instrID}
	// Replace with a fresh tracker that records the reset was observed in-stream,
	// preserving the new anchor seq for RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR.
	// awaitingRecovery=true gates RESET.SNAPSHOT_FOLLOWS and
	// RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET (Task 22).
	s.trackers[key] = &instrTracker{
		seenReset:              true,
		pendingResetAnchor:     newAnchorSeq,
		pendingResetAnchorSet:  true,
		awaitingRecovery:       true,
		awaitingRecoveryAnchor: newAnchorSeq,
	}
	// Clear oracle suspect state: the instrument's book is being reset, so any
	// prior mismatch suspect is stale and must not carry over to the new lifecycle.
	// A stale count could confirm a divergence across book lifecycles (false positive).
	delete(s.oracleSuspects, key)
	// Discard any open snapshot group for this channel: the instrument reset
	// invalidates any in-progress group.
	delete(s.openSnaps, ch)
	// Clear the book for this instrument: its orders were all invalidated by the reset.
	s.book.onInstrumentReset(ch, instrID)
}

// --- Engine integration ---

// gateDetector returns true when the mktdata port's channel-seq series was
// contiguous across the relevant window (no forward gaps observed). When it
// returns false, a per-instrument anomaly should be reported as Unverifiable
// rather than Violation, because the missing frames could have carried the
// missing deltas.
//
// We consult the mktdata port's portTracker.dirtyWindow flag. This flag is set
// in engine.go classify() whenever a forward gap is declared (res.gapBefore).
// It is cleared on era advance (advanceEra), so it reflects only gaps within
// the current era.
func (e *Engine) gateDetector() bool {
	pt, ok := e.ports[core.PortMktData]
	if !ok {
		return true // no mktdata seen yet — treat as clean
	}
	return !pt.dirtyWindow
}

// gateDetectorSnap returns true when the snapshot port's channel-seq series was
// contiguous (no forward gaps). Snapshot-counter rules are gated on snapshot-port
// seq contiguity.
func (e *Engine) gateDetectorSnap() bool {
	pt, ok := e.ports[core.PortSnapshot]
	if !ok {
		return true // no snapshot seen yet — treat as clean
	}
	return !pt.dirtyWindow
}

// snapPortBound returns true when at least one snapshot-port frame has been
// observed (the snapshot portTracker has been lazily created). Rules that
// depend on cross-port snapshot state must downgrade to NA when this returns
// false — the subscriber simply has not seen the snapshot port yet (cold start
// or the port is not part of the capture), and firing a Violation or even
// Unverifiable would be a false positive.
func (e *Engine) snapPortBound() bool {
	_, ok := e.ports[core.PortSnapshot]
	return ok
}

// gateConsumer returns true when referential-integrity checks (REF.*, TRADE.*)
// may be treated as Violations (rather than Unverifiable).
//
// A referential anomaly is only definitely a publisher fault when ALL of the
// following hold:
//  1. The instrument's refdata is ready() and the instrument is known to the channel
//     (so we know the instrument exists and have its metadata).
//  2. The per-instrument history for (ch, instrID) is trusted: we have been tracking
//     continuously without per-instrument gaps since seq=1 or since a recovery
//     snapshot (bookTrusted=true). A prior gap could have carried a missing OrderAdd.
//  3. The incoming perInstrSeq == lastInstrSeq+1 (no gap immediately before this msg).
//  4. The mktdata channel is gapless (gateDetector): a frame gap could have carried
//     the missing delta.
//
// If any condition is false, the anomaly is Unverifiable, never Violation.
func (e *Engine) gateConsumer(ch uint8, instrID uint32, perInstrSeq uint32) bool {
	// Condition 1: refdata ready and instrument known.
	if e.refdata == nil {
		return false
	}
	_, ok := e.refdata.defInfoFor(ch, instrID)
	if !ok {
		return false
	}
	// Conditions 2 + 3: per-instrument history is trusted and gapless to this message.
	t := e.mbo.tracker(ch, instrID)
	if t.lastInstrSeq == nil {
		// No prior delta seen for this instrument — cannot verify.
		return false
	}
	if !t.bookTrusted {
		// History has a prior gap (or cold-started mid-stream): book may be incomplete.
		return false
	}
	if perInstrSeq != *t.lastInstrSeq+1 {
		// There is a per-instrument seq gap: a missing delta could have added the order.
		return false
	}
	// Condition 4: mktdata channel gapless.
	return e.gateDetector()
}

// ensureMBO lazily initialises e.mbo.
func (e *Engine) ensureMBO() {
	if e.mbo == nil {
		e.mbo = newMBOState()
	}
}

// checkMBO is the per-feed MBO validator, invoked from Engine.classify for
// mktdata frames when cfg.Feed == FeedMBO. It processes all delta messages in
// the frame, updating per-instrument trackers and emitting:
//
//   - DELTA.PERINSTR_DENSITY: per-instrument seq must be contiguous (+1). A jump
//     (>+1) is Violation on a gapless channel, Unverifiable on a gapped one.
//
//   - DELTA.PERINSTR_FIRST_VALUE: the first delta for an instrument in an era
//     must have seq==1. On cold start (no prior reset observed) this is
//     Unverifiable, never Violation.
//
//   - DELTA.PERINSTR_NO_SNAPSHOT_RESET: per-instrument seq must not restart at a
//     low value without a Reset Count change. Snapshot emission does NOT reset
//     the per-instrument counter.
//
//   - DELTA.PERINSTR_DUP_DIVERGENT: a replayed per-instrument seq whose payload
//     differs from what was seen at that seq is a publisher fault.
//
//   - DELTA.PERINSTR_WRAP_BEFORE_RESET: u32 per-instrument seq must not wrap
//     (0xFFFFFFFF → low) without a Reset Count bump.
//
//   - FRAME.MKTDATA_SEQ_START: on a Reset Count change the mktdata-port seq
//     must restart at 0.
//
//   - BATCH.ID_MONOTONIC: BatchBoundary Batch ID must be monotonically increasing.
//
//   - RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR: after an InstrumentReset, the
//     first post-reset delta must carry per-instrument seq == recoveryK+1.
//
// InstrumentReset (0x14) messages call onInstrumentReset to wipe the tracker.
func (e *Engine) checkMBO(f *wire.Frame, ch uint8) {
	e.ensureMBO()
	frameSeq := f.Header.Sequence

	// Track the highest mktdata-port frame Sequence for SNAP.ANCHOR_IS_MKTDATA_SEQ.
	if !e.mbo.highestMktdataSeqSet || frameSeq > e.mbo.highestMktdataSeq {
		e.mbo.highestMktdataSeq = frameSeq
		e.mbo.highestMktdataSeqSet = true
	}

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeOrderAdd, wire.TypeOrderCancel, wire.TypeOrderExecute:
			// Skip tracker mutation when the message length is non-canonical:
			// MSG.LENGTH_PER_TYPE already fired, and body reads would return
			// zero/garbage, corrupting tracker state.
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			instrID := deltaInstrumentID(m)
			perSeq := deltaPerInstrumentSeq(m)
			// Evaluate gateConsumer BEFORE applyDeltaSeq advances lastInstrSeq.
			// gateConsumer requires perSeq == lastInstrSeq+1 (no gap) to return true;
			// once applyDeltaSeq runs, lastInstrSeq == perSeq so we'd lose that info.
			gated := e.gateConsumer(ch, instrID, perSeq)
			e.applyDeltaSeq(ch, instrID, perSeq, frameSeq, m)
			// Referential-integrity checks (Task 20).
			e.checkMBORef(ch, instrID, gated, frameSeq, m)

		case wire.TypeTrade:
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			instrID := tradeInstrumentID(m)
			// Trade body has no per-instrument seq (Body[8:16] = SourceTimestamp).
			// Gate on mktdata channel gapless + refdata ready + bookTrusted.
			gatedTrade := false
			if e.gateDetector() && e.refdata != nil {
				if _, ok := e.refdata.defInfoFor(ch, instrID); ok {
					t := e.mbo.tracker(ch, instrID)
					gatedTrade = t.bookTrusted
				}
			}
			e.checkTradeExecGrouping(ch, instrID, gatedTrade, frameSeq, m)

		case wire.TypeInstrReset:
			// Same length guard: only act on a well-formed InstrumentReset.
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			instrID := instrResetInstrumentID(m)
			newAnchor := instrResetNewAnchorSeq(m)
			e.mbo.onInstrumentReset(ch, instrID, newAnchor)

		case wire.TypeBatchBoundary:
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			e.checkBatchIDMonotonic(ch, frameSeq, m)
			e.checkBatchAtomicityConsistency(ch)
		}
	}
}

// checkBatchIDMonotonic implements BATCH.ID_MONOTONIC: BatchBoundary Batch ID
// must be non-decreasing within an era (forward skips are allowed; strictly
// decreasing is the violation). Gated on mktdata seq contiguity.
func (e *Engine) checkBatchIDMonotonic(ch uint8, frameSeq uint64, m wire.Message) {
	batchID := batchBoundaryBatchID(m)
	if e.mbo.lastBatchIDSet[ch] {
		last := e.mbo.lastBatchID[ch]
		if batchID < last {
			st := core.Violation
			if !e.gateDetector() {
				st = core.Unverifiable
			}
			e.Emit("BATCH.ID_MONOTONIC", st, core.PortMktData, frameSeq, ch, 0,
				fmt.Sprintf("batch id decreased: %d → %d", last, batchID))
		}
	}
	e.mbo.lastBatchID[ch] = batchID
	e.mbo.lastBatchIDSet[ch] = true
}

// checkBatchAtomicityConsistency implements BATCH.ATOMICITY_CONSISTENCY: at a
// BatchBoundary (0x13) the reconstructed book must be internally consistent.
// Specifically, no live resting order may have remaining == 0; such an order is
// an orphan that should have been removed by a prior full-fill or zero-remaining
// execute but was not (impossible state). The book builder's applyOrderExecute
// removes on remaining==0 during normal operation, so this is a defensive
// boundary checkpoint — the check is non-redundant only when the book state has
// been corrupted by an extraordinary sequence.
//
// BATCH.ATOMICITY_CONSISTENCY is evaluated only at BatchBoundary, not at every
// intermediate delta, to keep the check precise and false-positive-free.
//
// Gated on mktdata channel contiguity: on a loss-tainted window the book may be
// incomplete (missing OrderAdd deltas), so the anomaly is Unverifiable.
func (e *Engine) checkBatchAtomicityConsistency(ch uint8) {
	if e.mbo == nil || e.mbo.book == nil {
		return
	}
	gated := e.gateDetector()
	for key, bk := range e.mbo.book.books {
		if key.channelID != ch {
			continue
		}
		for orderID, lo := range bk.live {
			if lo.remaining == 0 {
				e.Emit("BATCH.ATOMICITY_CONSISTENCY", statusFor(gated), core.PortMktData, 0, ch, key.instrumentID,
					fmt.Sprintf("instrument %d: live order %d has remaining=0 at BatchBoundary (orphan)",
						key.instrumentID, orderID))
			}
		}
	}
}

// applyDeltaSeq updates the per-instrument tracker for (ch, instrID) with the
// incoming perSeq value, emitting DENSITY, FIRST_VALUE, NO_SNAPSHOT_RESET,
// DUP_DIVERGENT, WRAP_BEFORE_RESET and RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR
// findings as appropriate. frameSeq is the mktdata frame's Sequence Number.
func (e *Engine) applyDeltaSeq(ch uint8, instrID uint32, perSeq uint32, frameSeq uint64, m wire.Message) {
	t := e.mbo.tracker(ch, instrID)
	gapless := e.gateDetector()
	h := perDeltaPayloadHash(m)

	// Task 22: RESET.SNAPSHOT_FOLLOWS — premature delta resumption.
	// If the instrument had an InstrumentReset and no recovery snapshot has
	// completed yet, a new delta is premature. Gate on BOTH mktdata and snapshot
	// port gaplessness.
	// F5: if the snapshot port has never been seen (unbound), the recovery
	// snapshot simply has not arrived yet — downgrade to NA to avoid a false
	// positive on a subscriber that captures mktdata before the snapshot port.
	if t.awaitingRecovery {
		var st core.Status
		if !e.snapPortBound() {
			st = core.NA
		} else {
			gaplessSnap := e.gateDetectorSnap()
			st = core.Violation
			if !gapless || !gaplessSnap {
				st = core.Unverifiable
			}
		}
		e.Emit("RESET.SNAPSHOT_FOLLOWS", st, core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: delta (per-instr seq %d) arrived before recovery snapshot completed after InstrumentReset",
				instrID, perSeq))
		// Clear awaitingRecovery so we don't emit once per delta.
		t.awaitingRecovery = false
	}

	if t.lastInstrSeq == nil {
		// ---- First delta for this instrument in this era ----

		// RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR: if a recovery snapshot
		// has set recoveryK, the first post-reset delta must be recoveryK+1.
		// This takes precedence over FIRST_VALUE: after a reset, the recovery
		// snapshot defines the expected continuation point, not seq=1.
		if t.recoveryKSet {
			if perSeq != t.recoveryK+1 {
				st := core.Violation
				if !gapless {
					st = core.Unverifiable
				}
				e.Emit("RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR", st, core.PortMktData, frameSeq, ch, instrID,
					fmt.Sprintf("instrument %d: first post-reset delta seq is %d (expected recoveryK+1=%d)",
						instrID, perSeq, t.recoveryK+1))
			}
			// When perSeq == recoveryK+1 the recovery sequence is correct and seq
			// continuity is established. We intentionally do NOT set bookTrusted here:
			// we don't process SnapshotOrder messages, so the live order-id set is
			// empty, and setting bookTrusted would cause false REF.* violations for
			// orders that existed in the snapshot. bookTrusted is set true only when
			// we begin a fresh per-instrument sequence from seq=1.
			// Clear seenReset now that the first post-recovery delta has been consumed,
			// so future unauthorized restarts to 1 are not suppressed by this flag.
			t.seenReset = false
		} else {
			// DELTA.PERINSTR_FIRST_VALUE: seq must be 1 on a known-clean start.
			// Cold start = never saw a reset in-stream.
			// If we never saw a reset, we cannot assert what seq=1 should be.
			if perSeq != 1 {
				if !t.seenReset {
					// Cold start: joined mid-stream; cannot distinguish publisher
					// fault from normal mid-stream join. → Unverifiable.
					// bookTrusted remains false: we don't know the complete history.
					e.Emit("DELTA.PERINSTR_FIRST_VALUE", core.Unverifiable, core.PortMktData, frameSeq, ch, instrID,
						fmt.Sprintf("instrument %d: first observed per-instrument seq is %d (cold start, expected 1)",
							instrID, perSeq), "cold_start")
				} else {
					// We observed the reset in-stream (InstrumentReset message seen);
					// the next delta after a reset must be seq==1. Publisher fault.
					st := core.Violation
					if !gapless {
						st = core.Unverifiable
					}
					e.Emit("DELTA.PERINSTR_FIRST_VALUE", st, core.PortMktData, frameSeq, ch, instrID,
						fmt.Sprintf("instrument %d: first per-instrument seq after reset is %d (expected 1)",
							instrID, perSeq))
				}
				// bookTrusted remains false: history is incomplete.
			} else {
				// perSeq == 1: this is a proper start (era-begin or post-reset with seq=1).
				// The book is trusted from this point.
				t.bookTrusted = true
			}
			// Clear seenReset after the first post-reset delta so that future
			// unauthorized restarts to 1 are not suppressed by stale seenReset.
			t.seenReset = false
		}

		// Record the first observed seq regardless (so we can track density forward).
		cp := perSeq
		t.lastInstrSeq = &cp
		t.payloads.record(perSeq, h)
		return
	}

	// ---- Subsequent delta ----
	last := *t.lastInstrSeq

	switch {
	case perSeq == last+1:
		// Exactly the expected next seq — normal.
		// DELTA.PERINSTR_WRAP_BEFORE_RESET: check if last was 0xFFFFFFFF (wrap).
		if last == 0xFFFFFFFF {
			st := core.Violation
			if !gapless {
				st = core.Unverifiable
			}
			e.Emit("DELTA.PERINSTR_WRAP_BEFORE_RESET", st, core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: per-instrument seq wrapped 0xFFFFFFFF→%d without Reset Count bump",
					instrID, perSeq))
		}
		// bookTrusted remains as-is for contiguous seqs.

	case perSeq > last+1:
		// Per-instrument seq jumped forward: publisher skipped some deltas (or
		// transport dropped a frame carrying an intermediate delta).
		// Missing deltas may have carried OrderAdd messages → mark book untrusted.
		t.bookTrusted = false

		// DELTA.PERINSTR_NO_SNAPSHOT_RESET: a drop to a low value (new < last
		// without a reset count change) is handled in the default arm. But a
		// forward jump alone is not a snapshot-reset issue.

		st := core.Violation
		if !gapless {
			// A mktdata channel gap could account for the missing delta(s).
			st = core.Unverifiable
		}
		e.Emit("DELTA.PERINSTR_DENSITY", st, core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: per-instrument seq jumped %d→%d (expected %d)",
				instrID, last, perSeq, last+1))

	default:
		// perSeq <= last: duplicate or late arrival.

		// DELTA.PERINSTR_NO_SNAPSHOT_RESET: per-instrument seq must NOT restart
		// (drop to a low value) without a Reset Count change. A snapshot being
		// emitted does not permit a restart. We detect a suspicious drop: the new
		// seq is significantly lower than last and not just a single-frame dup.
		// We consider perSeq == 1 (or perSeq <= last) without a prior seenReset
		// and without a pending reset anchor as a NO_SNAPSHOT_RESET violation.
		if perSeq == 1 && last > 1 && !t.seenReset && !t.pendingResetAnchorSet {
			st := core.Violation
			if !gapless {
				st = core.Unverifiable
			}
			e.Emit("DELTA.PERINSTR_NO_SNAPSHOT_RESET", st, core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: per-instrument seq restarted at 1 (last=%d) without Reset Count change",
					instrID, last))
		}

		// DELTA.PERINSTR_DUP_DIVERGENT: a replayed per-instrument seq with a
		// different payload is a publisher fault.
		if prev, ok := t.payloads.lookup(perSeq); ok && prev != h {
			st := core.Violation
			if !gapless {
				st = core.Unverifiable
			}
			e.Emit("DELTA.PERINSTR_DUP_DIVERGENT", st, core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: per-instrument seq %d repeated with different payload",
					instrID, perSeq))
		}

		// Mark the book as corrupted by a duplicate/late delta. checkMBORef will
		// still mutate the live book (so REF.* checks operate on the post-mutation
		// state), but lastInstrSeq stays at `last`. The oracle must not diff a
		// book that has been mutated by replayed deltas against the snapshot: the
		// book state at lastInstrSeq==K is no longer equal to the as-of-K state.
		t.bookCorruptedByDup = true
	}

	// Record payload hash for future dup checks.
	t.payloads.record(perSeq, h)

	// Advance the tracker (forward motion only; don't regress on dup/late).
	if perSeq > last {
		cp := perSeq
		t.lastInstrSeq = &cp
	}
}

// checkMBOSnapshot is the snapshot-port MBO validator, invoked from Engine.classify
// for PortSnapshot frames when cfg.Feed == FeedMBO. It processes SnapshotBegin,
// SnapshotOrder, and SnapshotEnd messages, emitting:
//
// Counter-state rules (Begin only):
//   - SNAP.ANCHOR_IS_MKTDATA_SEQ
//   - SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT
//   - SNAP.SNAPSHOT_ID_MONOTONIC
//   - SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS
//
// Snapshot-group rules (Begin→Order→End accumulator):
//   - SNAP.BEGIN_ORDER_END_GROUPING
//   - SNAP.TOTAL_ORDERS_COUNT_MATCH
//   - SNAP.END_FIELDS_MATCH_BEGIN
//   - SNAP.ORDER_SNAPSHOT_ID_MATCH
//   - SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID
//   - SNAP.EMPTY_BOOK_WELL_FORMED
//   - SNAP.ORDER_PRICE_BOUND
func (e *Engine) checkMBOSnapshot(f *wire.Frame, ch uint8, snapPortSeq uint64) {
	e.ensureMBO()

	// Detect intra-group snapshot-port seq gaps by comparing snapPortSeq with
	// the group's lastSnapPortSeq. This is more precise than the era-wide
	// dirtyWindow: only gaps that occur DURING the group's lifetime (after its
	// SnapshotBegin) set the dirty flag.
	if open := e.mbo.openSnaps[ch]; open != nil && open.lastSnapPortSeq != 0 {
		if snapPortSeq > open.lastSnapPortSeq+1 {
			// Forward gap within the group.
			open.dirty = true
		}
		open.lastSnapPortSeq = snapPortSeq
	}

	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeSnapshotBegin:
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			e.handleSnapBegin(m, ch, snapPortSeq)

		case wire.TypeSnapshotOrder:
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			e.handleSnapOrder(m, ch, snapPortSeq)

		case wire.TypeSnapshotEnd:
			if m.Length != expectedMsgLen(e.cfg.Feed, m.Type) {
				continue
			}
			e.handleSnapEnd(m, ch, snapPortSeq)
		}
	}
}

// handleSnapBegin processes a SnapshotBegin (0x20) message:
//   - emits the counter-state Begin rules (ANCHOR_IS_MKTDATA_SEQ etc.)
//   - opens (or forcibly replaces) the open snapshot group for the channel.
func (e *Engine) handleSnapBegin(m wire.Message, ch uint8, snapPortSeq uint64) {
	instrID := snapshotBeginInstrumentID(m)
	anchorSeq := snapshotBeginAnchorSeq(m)
	snapID := snapshotBeginSnapshotID(m)
	lastInstrSeq := snapshotBeginLastInstrumentSeq(m)
	totalOrders := snapshotBeginTotalOrders(m)

	gapless := e.gateDetectorSnap()
	gaplessMkt := e.gateDetector()

	// SNAP.ANCHOR_IS_MKTDATA_SEQ: anchor must be drawn from the mktdata
	// series (≤ the highest mktdata seq observed), never exceed it.
	if e.mbo.highestMktdataSeqSet && anchorSeq > e.mbo.highestMktdataSeq {
		st := core.Violation
		reason := ""
		// Downgrade to Unverifiable when the mktdata view isn't yet as-of-anchor:
		// either a mktdata gap, OR the mktdata reorder buffer still holds frames
		// that could carry seq==anchor (cross-port reorder, F3). Only a Violation
		// when the mktdata stream is gapless AND fully classified past the anchor.
		if !gaplessMkt || e.mktdataPending() {
			st = core.Unverifiable
			reason = "reorder"
		}
		e.Emit("SNAP.ANCHOR_IS_MKTDATA_SEQ", st, core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: SnapshotBegin anchor seq %d > highest mktdata seq %d",
				instrID, anchorSeq, e.mbo.highestMktdataSeq), reason)
	}

	e.snapTrack(ch, instrID, anchorSeq, snapID, lastInstrSeq, snapPortSeq, gapless)

	// SNAP.BEGIN_ORDER_END_GROUPING: if a snapshot group is already open for this
	// channel (no End was seen), emit a grouping violation before opening the new one.
	// Gate: prev.dirty is true if a snapshot-port seq gap was observed during the
	// previous group's lifetime (set by the pre-frame gap check in checkMBOSnapshot).
	if prev := e.mbo.openSnaps[ch]; prev != nil {
		st := core.Violation
		if prev.dirty {
			st = core.Unverifiable
		}
		e.Emit("SNAP.BEGIN_ORDER_END_GROUPING", st, core.PortSnapshot, snapPortSeq, ch, prev.instrID,
			fmt.Sprintf("instrument %d: new SnapshotBegin (snapshot id %d) while previous group (snapshot id %d) still open",
				instrID, snapID, prev.snapshotID))
	}

	// Open a new group. The dirty flag tracks intra-group snapshot-port gaps
	// (from this Begin's frame onward). Pre-group era gaps do not taint this
	// group. lastSnapPortSeq is seeded with snapPortSeq so subsequent frames
	// can detect forward gaps within the group.
	e.mbo.openSnaps[ch] = &openSnapshot{
		instrID:         instrID,
		snapshotID:      snapID,
		anchorSeq:       anchorSeq,
		total:           totalOrders,
		received:        0,
		lastInstrSeqK:   lastInstrSeq,
		orderIDs:        make(map[uint64]struct{}),
		orders:          make(map[uint64]snapOrderRecord),
		dirty:           false,
		lastSnapPortSeq: snapPortSeq,
	}
}

// handleSnapOrder processes a SnapshotOrder (0x21) message against the open group.
func (e *Engine) handleSnapOrder(m wire.Message, ch uint8, snapPortSeq uint64) {
	snapID := snapshotOrderSnapshotID(m)
	orderID := snapshotOrderOrderID(m)
	price := snapshotOrderPrice(m)
	side := snapshotOrderSide(m)
	flags := snapshotOrderFlags(m)
	enterTS := snapshotOrderEnterTimestamp(m)
	qty := snapshotOrderQuantity(m)

	open := e.mbo.openSnaps[ch]

	// SNAP.BEGIN_ORDER_END_GROUPING: order arrived without a preceding Begin.
	if open == nil {
		st := core.Violation
		if !e.gateDetectorSnap() {
			st = core.Unverifiable
		}
		e.Emit("SNAP.BEGIN_ORDER_END_GROUPING", st, core.PortSnapshot, snapPortSeq, ch, 0,
			fmt.Sprintf("SnapshotOrder (snapshot id %d) received without a preceding SnapshotBegin", snapID))
		return
	}

	// SNAP.ORDER_SNAPSHOT_ID_MATCH: SnapshotOrder's snapshot ID must match the
	// open group's snapshot ID.
	if snapID != open.snapshotID {
		st := core.Violation
		if open.dirty {
			st = core.Unverifiable
		}
		// Mark group structurally invalid so the oracle won't diff against it.
		open.structuralViolation = true
		e.Emit("SNAP.ORDER_SNAPSHOT_ID_MATCH", st, core.PortSnapshot, snapPortSeq, ch, open.instrID,
			fmt.Sprintf("instrument %d: SnapshotOrder snapshot id %d != open group snapshot id %d",
				open.instrID, snapID, open.snapshotID))
	}

	// SNAP.EMPTY_BOOK_WELL_FORMED: a TotalOrders==0 group must not have any orders.
	if open.total == 0 {
		st := core.Violation
		if open.dirty {
			st = core.Unverifiable
		}
		// Mark group structurally invalid so the oracle won't diff against it.
		open.structuralViolation = true
		e.Emit("SNAP.EMPTY_BOOK_WELL_FORMED", st, core.PortSnapshot, snapPortSeq, ch, open.instrID,
			fmt.Sprintf("instrument %d: SnapshotOrder received but SnapshotBegin declared TotalOrders=0",
				open.instrID))
	}

	// SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID: order IDs within a group must be unique.
	if _, dup := open.orderIDs[orderID]; dup {
		st := core.Violation
		if open.dirty {
			st = core.Unverifiable
		}
		// Mark group structurally invalid so the oracle won't diff against it.
		open.structuralViolation = true
		e.Emit("SNAP.SNAPSHOT_ORDER_NO_DUP_ORDER_ID", st, core.PortSnapshot, snapPortSeq, ch, open.instrID,
			fmt.Sprintf("instrument %d: duplicate order id %d in snapshot group (snapshot id %d)",
				open.instrID, orderID, open.snapshotID))
	}

	open.orderIDs[orderID] = struct{}{}
	// Collect full order record for the oracle (Task 26). Only store once per
	// order ID (first occurrence wins; dup detection fires separately above).
	if _, already := open.orders[orderID]; !already {
		open.orders[orderID] = snapOrderRecord{
			side:       side,
			orderFlags: flags,
			enterTS:    enterTS,
			price:      price,
			qty:        qty,
		}
	}
	open.received++

	// SNAP.ORDER_PRICE_BOUND: gated on refdata ready(); price must not be negative
	// when priceBound is 1 (Bounded[0,1]) or 2 (Non-negative).
	if e.refdata != nil {
		if di, ok := e.refdata.defInfoFor(ch, open.instrID); ok {
			if (di.priceBound == 1 || di.priceBound == 2) && price < 0 {
				e.Emit("SNAP.ORDER_PRICE_BOUND", core.Violation, core.PortSnapshot, snapPortSeq, ch, open.instrID,
					fmt.Sprintf("instrument %d: SnapshotOrder price %d < 0 violates Price Bound=%d",
						open.instrID, price, di.priceBound))
			}
		}
	}
}

// handleSnapEnd processes a SnapshotEnd (0x22) message, closing the open group
// and emitting count-match / fields-match findings.
func (e *Engine) handleSnapEnd(m wire.Message, ch uint8, snapPortSeq uint64) {
	endInstrID := snapshotEndInstrumentID(m)
	endAnchorSeq := snapshotEndAnchorSeq(m)
	endSnapID := snapshotEndSnapshotID(m)

	open := e.mbo.openSnaps[ch]

	// SNAP.BEGIN_ORDER_END_GROUPING: End arrived without a preceding Begin.
	if open == nil {
		st := core.Violation
		if !e.gateDetectorSnap() {
			st = core.Unverifiable
		}
		e.Emit("SNAP.BEGIN_ORDER_END_GROUPING", st, core.PortSnapshot, snapPortSeq, ch, endInstrID,
			fmt.Sprintf("instrument %d: SnapshotEnd (snapshot id %d) received without a preceding SnapshotBegin",
				endInstrID, endSnapID))
		return
	}

	// SNAP.END_FIELDS_MATCH_BEGIN: End's InstrumentID, AnchorSeq, SnapshotID must
	// all match the open Begin's fields.
	if endInstrID != open.instrID || endAnchorSeq != open.anchorSeq || endSnapID != open.snapshotID {
		st := core.Violation
		if open.dirty {
			st = core.Unverifiable
		}
		// Mark group structurally invalid so the oracle won't diff against it.
		open.structuralViolation = true
		e.Emit("SNAP.END_FIELDS_MATCH_BEGIN", st, core.PortSnapshot, snapPortSeq, ch, open.instrID,
			fmt.Sprintf("instrument %d: SnapshotEnd fields (instr=%d anchor=%d snapID=%d) do not match Begin (instr=%d anchor=%d snapID=%d)",
				open.instrID, endInstrID, endAnchorSeq, endSnapID,
				open.instrID, open.anchorSeq, open.snapshotID))
	}

	// SNAP.TOTAL_ORDERS_COUNT_MATCH: received order count must equal TotalOrders.
	if open.received != open.total {
		if open.received > open.total {
			// Over-count: always a Violation — cannot be caused by transport loss.
			// Mark group structurally invalid so the oracle won't diff against it.
			open.structuralViolation = true
			e.Emit("SNAP.TOTAL_ORDERS_COUNT_MATCH", core.Violation, core.PortSnapshot, snapPortSeq, ch, open.instrID,
				fmt.Sprintf("instrument %d: received %d SnapshotOrders but TotalOrders=%d (over-count)",
					open.instrID, open.received, open.total))
		} else {
			// Under-count: Unverifiable if the snapshot port had a gap OR transport
			// corruption during this era (loss/truncation could have dropped the
			// missing SnapshotOrder frames); else Violation. `open.dirty` catches an
			// intra-group snapshot-seq gap; `e.snapPortDirty()` catches transport
			// corruption on the snapshot port (whose truncated frame header may not
			// even decode a channel, so it can't reliably taint the group directly).
			// Either way, mark group structurally invalid for the oracle.
			st := core.Violation
			reason := ""
			if open.dirty || e.snapPortDirty() {
				st = core.Unverifiable
				reason = "loss"
			}
			open.structuralViolation = true
			e.Emit("SNAP.TOTAL_ORDERS_COUNT_MATCH", st, core.PortSnapshot, snapPortSeq, ch, open.instrID,
				fmt.Sprintf("instrument %d: received %d SnapshotOrders but TotalOrders=%d (under-count)",
					open.instrID, open.received, open.total), reason)
		}
	}

	// Task 22: cross-port reset-recovery check.
	// A completed snapshot group satisfies (or fails) the recovery requirement for
	// an instrument that had an InstrumentReset with no matching recovery yet.
	e.onSnapGroupComplete(ch, open.instrID, open.anchorSeq, open.dirty, snapPortSeq)

	// Task 26: snapshot-vs-delta reconstruction oracle.
	// Run AFTER onSnapGroupComplete (which may clear awaitingRecovery) so that
	// the oracle sees correct tracker state. Pass the full snapshot record.
	e.runOracleForGroup(ch, open, snapPortSeq)

	// Close the group.
	delete(e.mbo.openSnaps, ch)
}

// onSnapGroupComplete is called when a snapshot group is successfully closed
// (SnapshotEnd received). It handles:
//
//  1. RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET: if the instrument was
//     awaiting a recovery snapshot, the group's anchor must exactly match the
//     InstrumentReset's New Anchor Seq.
//
//  2. SNAP.ROUND_ROBIN_COVERS_MANIFEST: increment per-instrument snapCount and
//     advance the era-wide maxSnapCycle.
//
// dirty is the per-group dirty flag (snapshot-port gap observed during the group).
func (e *Engine) onSnapGroupComplete(ch uint8, instrID uint32, anchorSeq uint64, dirty bool, snapPortSeq uint64) {
	dt := e.mbo.tracker(ch, instrID)

	// RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET: if the instrument had an
	// InstrumentReset and was awaiting recovery, check anchor exact match.
	// Gate: requires BOTH the snapshot port to be gapless (dirty==false) AND the
	// mktdata port to be gapless (gateDetector). If either has a gap → Unverifiable.
	if dt.awaitingRecovery {
		gapless := !dirty && e.gateDetector()
		if anchorSeq == dt.awaitingRecoveryAnchor {
			// Correct recovery anchor — recovery satisfied. Clear the flag.
			dt.awaitingRecovery = false
		} else {
			st := core.Violation
			if !gapless {
				st = core.Unverifiable
			}
			e.Emit("RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET", st, core.PortSnapshot, snapPortSeq, ch, instrID,
				fmt.Sprintf("instrument %d: recovery snapshot anchor seq %d != expected %d from InstrumentReset",
					instrID, anchorSeq, dt.awaitingRecoveryAnchor))
			// Clear anyway: we've reported the mismatch; don't keep reporting on
			// subsequent groups. The instrument's recovery state is now ambiguous.
			dt.awaitingRecovery = false
		}
	}

	// SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING (info, observability):
	// For a periodic snapshot of an already-ready instrument (not cold-start,
	// not a recovery snapshot following InstrumentReset), note whether the
	// snapshot anchor is ahead of or at/behind the subscriber's last-applied
	// per-instrument seq (dt.lastInstrSeq — a u32 monotone counter of deltas for
	// this instrument). Note that anchorSeq is a mktdata FRAME sequence (u64) and
	// lastInstrSeq is a PER-INSTRUMENT sequence (u32); they are different domains
	// and will diverge when there are multiple instruments on the channel or when
	// frames carry non-delta messages. The comparison is intentionally cross-domain:
	// it measures whether the snapshot anchor has numerically advanced past the
	// count of per-instrument deltas the subscriber has applied.
	//
	// Both cases are legitimate by spec:
	//   anchor > lastApplied → snapshot anchor ahead of per-instr progress.
	//   anchor <= lastApplied → snapshot anchor at or behind per-instr progress.
	// This is never a Violation (status always Pass / info-level).
	//
	// Guards:
	//   dt.lastInstrSeq != nil → at least one delta seen (not cold-start). After
	//     onInstrumentReset this is always nil on the fresh tracker, so recovery
	//     snapshots are silenced by this guard without needing an explicit
	//     awaitingRecovery check.
	//   st.snapCount > 0 → at least one prior snapshot was completed. Avoids firing
	//     for the very first (bootstrap) snapshot of an instrument even if deltas
	//     arrived on the mktdata channel before that first snapshot was seen.

	// Get the snapTracker now; its snapCount is used both for the guard below and
	// for the SNAP.ROUND_ROBIN_COVERS_MANIFEST increment that follows.
	st := e.mbo.snapTrack(ch, instrID)

	if dt.lastInstrSeq != nil && st.snapCount > 0 {
		lastApplied := uint64(*dt.lastInstrSeq)
		detail := fmt.Sprintf("instrument %d: periodic snapshot anchor=%d last_applied_per_instr_seq=%d (re-bootstrap: anchor>last_applied)",
			instrID, anchorSeq, lastApplied)
		if anchorSeq <= lastApplied {
			detail = fmt.Sprintf("instrument %d: periodic snapshot anchor=%d last_applied_per_instr_seq=%d (consistency: anchor<=last_applied)",
				instrID, anchorSeq, lastApplied)
		}
		e.Emit("SNAP.ANCHOR_LE_OR_GT_LAST_APPLIED_HANDLING", core.Pass, core.PortSnapshot, snapPortSeq, ch, instrID, detail)
	}

	// SNAP.ROUND_ROBIN_COVERS_MANIFEST: increment this instrument's snapCount and
	// advance the era-wide maxSnapCycle.
	st.snapCount++
	if st.snapCount > e.mbo.maxSnapCycle {
		e.mbo.maxSnapCycle = st.snapCount
	}
}

// flushOpenSnaps emits SNAP.BEGIN_ORDER_END_GROUPING for any snapshot groups
// that are still open at end-of-run (SnapshotBegin seen, SnapshotEnd never
// received). Called from Engine.EndRun after the reorder buffer is drained.
//
// An unclosed group at EOF is a grouping violation. Gate on dirty flag: if the
// snapshot port had a gap during the group, it is Unverifiable (loss could have
// carried the missing SnapshotEnd); otherwise it is a Violation.
func (e *Engine) flushOpenSnaps() {
	if e.mbo == nil {
		return
	}
	for ch, open := range e.mbo.openSnaps {
		if open == nil {
			continue
		}
		st := core.Violation
		if open.dirty {
			st = core.Unverifiable
		}
		e.Emit("SNAP.BEGIN_ORDER_END_GROUPING", st, core.PortSnapshot, 0, ch, open.instrID,
			fmt.Sprintf("instrument %d: SnapshotBegin (snapshot id %d) never followed by SnapshotEnd (end of stream)",
				open.instrID, open.snapshotID))
	}
	clear(e.mbo.openSnaps)
}

// checkResetSnapshotFollows emits RESET.SNAPSHOT_FOLLOWS for any instrument that
// had an InstrumentReset but whose recovery snapshot never completed before
// end-of-stream. Called from Engine.EndRun.
//
// Gate: if the snapshot port has a dirty window (gap observed), the anomaly is
// Unverifiable — the missing recovery snapshot could have been in the gap.
// Both the mktdata port and snapshot port must be gapless for Violation.
func (e *Engine) checkResetSnapshotFollows() {
	if e.mbo == nil {
		return
	}
	// F5: if the snapshot port has never been seen (unbound), we cannot determine
	// whether a recovery snapshot was emitted. Downgrade to NA rather than firing
	// Violation or Unverifiable — the subscriber simply has not observed the
	// snapshot port (cold start or not part of the capture).
	snapBound := e.snapPortBound()
	gaplessMkt := e.gateDetector()
	gaplessSnap := e.gateDetectorSnap()
	for key, dt := range e.mbo.trackers {
		if !dt.awaitingRecovery {
			continue
		}
		var st core.Status
		if !snapBound {
			st = core.NA
		} else {
			st = core.Violation
			if !gaplessMkt || !gaplessSnap {
				st = core.Unverifiable
			}
		}
		e.Emit("RESET.SNAPSHOT_FOLLOWS", st, core.PortMktData, 0, key.channelID, key.instrumentID,
			fmt.Sprintf("instrument %d: InstrumentReset (anchor seq %d) was not followed by a recovery snapshot before end of stream",
				key.instrumentID, dt.awaitingRecoveryAnchor))
		dt.awaitingRecovery = false // mark consumed so we don't emit again on repeated EndRun
	}
}

// checkRoundRobinCoversManifest emits SNAP.ROUND_ROBIN_COVERS_MANIFEST for any
// manifest-ready instrument that was never snapshotted after ≥2 clean snapshot
// cycles. Called from Engine.EndRun.
//
// A "cycle" is gauged by maxSnapCycle: the highest snapCount observed across all
// instruments. When maxSnapCycle ≥ 2, other instruments have been snapshotted at
// least twice, so the snapshot port has been active long enough that a missing
// instrument is a publisher fault.
//
// Gate: if the snapshot port has a dirty window, downgrade to Unverifiable.
// Conservative: only fires when refdata is ready and the instrument is in the
// manifest (channelKnown).
func (e *Engine) checkRoundRobinCoversManifest() {
	if e.mbo == nil || e.refdata == nil {
		return
	}
	if e.mbo.maxSnapCycle < 2 {
		// Fewer than 2 clean cycles observed — conservative; don't fire.
		return
	}
	gaplessSnap := e.gateDetectorSnap()
	// Iterate over all ready channels and check each instrument in the manifest.
	for ch, cs := range e.refdata.channels {
		if !channelReady(cs) {
			continue
		}
		for instrID := range cs.defs {
			key := instrTrackerKey{ch, instrID}
			st := e.mbo.snapTrackers[key]
			if st != nil && st.snapCount > 0 {
				// This instrument was snapshotted at least once — OK.
				continue
			}
			// Instrument is in the manifest but has never been snapshotted.
			status := core.Violation
			if !gaplessSnap {
				status = core.Unverifiable
			}
			e.Emit("SNAP.ROUND_ROBIN_COVERS_MANIFEST", status, core.PortSnapshot, 0, ch, instrID,
				fmt.Sprintf("instrument %d: in manifest but never snapshotted after %d cycle(s)",
					instrID, e.mbo.maxSnapCycle))
		}
	}
}

// snapTrack applies per-(channel, instrument) snapshot state updates and emits
// SNAP rules for a single SnapshotBegin message.
func (e *Engine) snapTrack(ch uint8, instrID uint32, anchorSeq uint64, snapID uint32, lastInstrSeqK uint32, snapPortSeq uint64, gapless bool) {
	st := e.mbo.snapTrack(ch, instrID)
	gaplessMkt := e.gateDetector()

	// SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT: successive snapshots for the same
	// instrument must have non-decreasing Anchor Seq.
	if st.lastAnchorSeq != nil && anchorSeq < *st.lastAnchorSeq {
		status := core.Violation
		if !gapless {
			status = core.Unverifiable
		}
		e.Emit("SNAP.ANCHOR_MONOTONIC_PER_INSTRUMENT", status, core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: anchor seq decreased %d→%d",
				instrID, *st.lastAnchorSeq, anchorSeq))
	}

	// SNAP.SNAPSHOT_ID_MONOTONIC: Snapshot ID must be strictly non-decreasing
	// (forward skips OK; strictly-decreasing/equal at higher snap-port seq is
	// the violation).
	if st.lastSnapshotID != nil && st.lastSnapPortSeq != nil {
		if snapPortSeq > *st.lastSnapPortSeq && snapID <= *st.lastSnapshotID {
			status := core.Violation
			if !gapless {
				status = core.Unverifiable
			}
			e.Emit("SNAP.SNAPSHOT_ID_MONOTONIC", status, core.PortSnapshot, snapPortSeq, ch, instrID,
				fmt.Sprintf("instrument %d: snapshot id %d not greater than last %d (snap port seq %d>%d)",
					instrID, snapID, *st.lastSnapshotID, snapPortSeq, *st.lastSnapPortSeq))
		}
	}

	// SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS: SnapshotBegin's Last
	// Instrument Seq (K) must equal the per-instrument seq of the last delta
	// applied at or before Anchor Seq. We use the current lastInstrSeq as a
	// proxy: if deltas have been applied up to the mktdata stream, the
	// per-instrument tracker should reflect the state at Anchor Seq.
	// We only check this if the instrument's delta tracker has been initialized.
	//
	// F3 (as-of-anchor correctness): only fire when the subscriber's delta tracker
	// is BEHIND the snapshot's K (subscriber has not yet received all deltas that
	// the snapshot claims were applied). When the subscriber is AHEAD of K
	// (lastInstrSeq > K), that is a normal race — deltas arrived and were applied
	// between the publisher's snapshot anchor time and the snapshot frame's delivery.
	// Firing on the ahead case would be a false positive.
	dt := e.mbo.tracker(ch, instrID)
	if dt.lastInstrSeq != nil && *dt.lastInstrSeq < lastInstrSeqK {
		status := core.Violation
		reason := ""
		// The "behind K" case is only a genuine violation when the missing deltas
		// truly never arrived. If the mktdata channel has a gap, or its reorder
		// buffer still holds unclassified frames (the deltas up to K may simply be
		// buffered, not missing — cross-port reorder), downgrade to Unverifiable (F3).
		if !gaplessMkt || !gapless || e.mktdataPending() {
			status = core.Unverifiable
			reason = "reorder"
		}
		e.Emit("SNAP.LAST_INSTRUMENT_SEQ_CONSISTENT_WITH_DELTAS", status, core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: SnapshotBegin LastInstrumentSeq=%d but delta tracker lastSeq=%d",
				instrID, lastInstrSeqK, *dt.lastInstrSeq), reason)
	}

	// If this is a recovery snapshot following an InstrumentReset, record K.
	// Only exact anchor match counts: a snapshot with anchorSeq != pendingResetAnchor
	// cannot be the recovery snapshot (it will be caught by
	// RESET.RECOVERY_SNAPSHOT_ANCHOR_MATCHES_RESET at SnapshotEnd). Using >= here
	// would incorrectly seed recoveryK from a wrong-anchor recovery snapshot,
	// causing RESET.NO_DANGLING_DELTAS_AT_OR_BELOW_ANCHOR to incorrectly pass.
	if dt.pendingResetAnchorSet && anchorSeq == dt.pendingResetAnchor {
		dt.recoveryK = lastInstrSeqK
		dt.recoveryKSet = true
		dt.pendingResetAnchorSet = false
	}

	// Update snapshot tracker.
	ac := anchorSeq
	st.lastAnchorSeq = &ac
	sid := snapID
	st.lastSnapshotID = &sid
	sps := snapPortSeq
	st.lastSnapPortSeq = &sps
}

// --- Task 20: MBO referential-integrity checks ---

// checkMBORef dispatches per-message referential checks for OrderAdd, OrderCancel,
// and OrderExecute messages.  gated is the pre-computed gateConsumer result
// (true = gapless history, anomalies are Violations; false = Unverifiable).
func (e *Engine) checkMBORef(ch uint8, instrID uint32, gated bool, frameSeq uint64, m wire.Message) {
	bk := e.mbo.book.book(ch, instrID)

	switch m.Type {
	case wire.TypeOrderAdd:
		e.checkOrderAdd(ch, instrID, gated, frameSeq, m, bk)
	case wire.TypeOrderCancel:
		e.checkOrderCancel(ch, instrID, gated, frameSeq, m, bk)
	case wire.TypeOrderExecute:
		e.checkOrderExecute(ch, instrID, gated, frameSeq, m, bk)
	}

	// Price-bound checks: gated only on refdata ready (not per-instrument gapless).
	e.checkPriceBound(ch, instrID, frameSeq, m)
}

// statusFor returns Violation when gated==true, else Unverifiable.
func statusFor(gated bool) core.Status {
	if gated {
		return core.Violation
	}
	return core.Unverifiable
}

// checkOrderAdd processes REF.DUPLICATE_LIVE_ORDERADD and updates the live set.
func (e *Engine) checkOrderAdd(ch uint8, instrID uint32, gated bool, frameSeq uint64, m wire.Message, bk *instrBook) {
	orderID := orderAddOrderID(m)
	flags := orderAddOrderFlags(m)
	side := orderAddSide(m)
	sourceID := orderAddSourceID(m)
	price := orderAddPrice(m)
	qty := orderAddQuantity(m)
	enterTS := orderAddEnterTimestamp(m)

	// Hidden orders (OrderFlags bit 2) share ID slots by spec.
	// Do not track hidden orders in the live set: they are exempt from duplicate
	// detection and their presence would cause false REF.DUPLICATE_LIVE_ORDERADD
	// for subsequent visible adds with the same ID.
	// applyOrderAddFull records the ID in hiddenIDs (via the isHidden branch) so
	// that execute/cancel messages for this ID are not falsely reported as dangling.
	if isHidden(flags) {
		_ = bk.applyOrderAddFull(orderID, sourceID, side, price, qty, flags, enterTS)
		return
	}

	if _, live := bk.live[orderID]; live {
		e.Emit("REF.DUPLICATE_LIVE_ORDERADD", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: OrderAdd reuses live order id %d", instrID, orderID))
		// Still insert: let the newer add replace the old entry so subsequent ops work.
	}

	// Insert into the live set via the book builder; clears any prior removed entry.
	_ = bk.applyOrderAddFull(orderID, sourceID, side, price, qty, flags, enterTS)
}

// checkOrderCancel processes REF.CANCEL_DANGLING_ORDER, REF.OPERATION_AFTER_REMOVAL,
// FIELD.SOURCE_ID_CONSISTENCY, and moves the order from live → removed.
func (e *Engine) checkOrderCancel(ch uint8, instrID uint32, gated bool, frameSeq uint64, m wire.Message, bk *instrBook) {
	orderID := orderCancelOrderID(m)
	sourceID := orderCancelSourceID(m)

	if lo, live := bk.live[orderID]; live {
		// FIELD.SOURCE_ID_CONSISTENCY: sourceID must not change across lifecycle.
		if sourceID != 0 && lo.sourceID != 0 && sourceID != lo.sourceID {
			e.Emit("FIELD.SOURCE_ID_CONSISTENCY", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: OrderCancel source id %d != OrderAdd source id %d (order %d)",
					instrID, sourceID, lo.sourceID, orderID))
		}
		// Move to removed set via book builder.
		_ = bk.applyOrderCancel(orderID)
		return
	}

	// Not in live. Before checking removed/dangling, allow hidden-tainted IDs to
	// pass silently. Hidden orders share ID slots by spec; an ID slot can be reused
	// by a hidden order after a visible order with the same ID was removed, making
	// the ID appear in both hiddenIDs and removed. Checking hiddenIDs here (after
	// live) ensures visible live-order lifecycle is unaffected while preventing
	// false REF.OPERATION_AFTER_REMOVAL and REF.CANCEL_DANGLING_ORDER.
	if _, hidden := bk.hiddenIDs[orderID]; hidden {
		return
	}

	if _, removed := bk.removed[orderID]; removed {
		e.Emit("REF.OPERATION_AFTER_REMOVAL", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: OrderCancel on already-removed order id %d", instrID, orderID))
		return
	}

	// Not in live, removed, or hidden: dangling.
	e.Emit("REF.CANCEL_DANGLING_ORDER", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
		fmt.Sprintf("instrument %d: OrderCancel for unknown order id %d", instrID, orderID))
}

// checkOrderExecute processes REF.EXEC_DANGLING_ORDER, REF.OPERATION_AFTER_REMOVAL,
// REF.SIDE_PRICE_CONSISTENCY, FIELD.SOURCE_ID_CONSISTENCY, and manages live/removed sets.
func (e *Engine) checkOrderExecute(ch uint8, instrID uint32, gated bool, frameSeq uint64, m wire.Message, bk *instrBook) {
	orderID := orderExecuteOrderID(m)
	aggressorSide := orderExecuteAggressorSide(m)
	execFlags := orderExecuteExecFlags(m)
	sourceID := orderExecuteSourceID(m)
	tradeID := orderExecuteTradeID(m)

	// Record Trade ID in the exec set (for TRADE.EXEC_GROUPING cross-reference).
	if tradeID != 0 {
		bk.execTradeIDs[tradeID] = struct{}{}
	}

	if lo, live := bk.live[orderID]; live {
		// FIELD.SOURCE_ID_CONSISTENCY: sourceID must not drift.
		if sourceID != 0 && lo.sourceID != 0 && sourceID != lo.sourceID {
			e.Emit("FIELD.SOURCE_ID_CONSISTENCY", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: OrderExecute source id %d != OrderAdd source id %d (order %d)",
					instrID, sourceID, lo.sourceID, orderID))
		}

		// REF.SIDE_PRICE_CONSISTENCY: aggressor side vs resting side.
		// Aggressor 0 = unknown → skip.
		// Buy aggressor (1) hits resting ask (side=1).
		// Sell aggressor (2) hits resting bid (side=0).
		if aggressorSide != 0 {
			restingSide := lo.side
			// mismatch: buy aggressor should hit side=1 (ask), sell should hit side=0 (bid).
			badSide := (aggressorSide == 1 && restingSide != 1) ||
				(aggressorSide == 2 && restingSide != 0)
			if badSide {
				e.Emit("REF.SIDE_PRICE_CONSISTENCY", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
					fmt.Sprintf("instrument %d: aggressor side %d vs resting side %d mismatch (order %d)",
						instrID, aggressorSide, restingSide, orderID))
			}
		}

		// Apply the book-builder mutation (quantity decrement + removal on full-fill or
		// zero remaining). Map typed errors to quantity-conservation findings.
		execQty := orderExecuteExecQuantity(m)
		err := bk.applyOrderExecute(orderID, execQty, isFullFill(execFlags))
		switch {
		case errors.Is(err, errOverfill):
			e.Emit("REF.EXEC_OVERFILL", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: OrderExecute qty %d exceeds remaining qty (order %d)",
					instrID, execQty, orderID))
		case errors.Is(err, errFullFillDisagree):
			e.Emit("REF.FULLFILL_FLAG_DISAGREEMENT", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: full-fill flag set but exec qty %d != remaining qty (order %d)",
					instrID, execQty, orderID))
		}
		return
	}

	// Not in live. Before checking removed/dangling, allow hidden-tainted IDs to
	// pass silently. Hidden orders share ID slots by spec; an ID slot can be reused
	// by a hidden order after a visible order with the same ID was removed, making
	// the ID appear in both hiddenIDs and removed. Checking hiddenIDs here (after
	// live) ensures visible live-order lifecycle is unaffected while preventing
	// false REF.OPERATION_AFTER_REMOVAL, REF.SIDE_PRICE_CONSISTENCY, and
	// REF.EXEC_DANGLING_ORDER for hidden-order IDs.
	if _, hidden := bk.hiddenIDs[orderID]; hidden {
		return
	}

	if _, removed := bk.removed[orderID]; removed {
		e.Emit("REF.OPERATION_AFTER_REMOVAL", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: OrderExecute on already-removed order id %d", instrID, orderID))
		return
	}

	// Not in live, removed, or hidden: dangling.
	e.Emit("REF.EXEC_DANGLING_ORDER", statusFor(gated), core.PortMktData, frameSeq, ch, instrID,
		fmt.Sprintf("instrument %d: OrderExecute for unknown order id %d", instrID, orderID))
}

// checkTradeExecGrouping processes TRADE.EXEC_GROUPING: a Trade with non-zero TradeID
// must have at least one matching OrderExecute with that TradeID in the gapless window.
// TradeID==0 is exempt.
// gated is the pre-computed gateConsumer result (per-instrument gapless context).
func (e *Engine) checkTradeExecGrouping(ch uint8, instrID uint32, gated bool, frameSeq uint64, m wire.Message) {
	tradeID := tradeTradeID(m)
	if tradeID == 0 {
		return // TradeID==0 exempt
	}

	bk := e.mbo.book.book(ch, instrID)
	if _, seen := bk.execTradeIDs[tradeID]; !seen {
		// No matching OrderExecute seen with this TradeID.
		st := core.Unverifiable
		if gated {
			st = core.Violation
		}
		e.Emit("TRADE.EXEC_GROUPING", st, core.PortMktData, frameSeq, ch, instrID,
			fmt.Sprintf("instrument %d: Trade id %d has no matching OrderExecute in window", instrID, tradeID))
	}
}

// checkPriceBound implements FIELD.ORDERADD_PRICE_BOUND and REF.EXEC_PRICE_BOUND.
// Gated on refdata ready() (instrument's priceBound available).
// Silent (NA) when refdata not ready; no false positives.
func (e *Engine) checkPriceBound(ch uint8, instrID uint32, frameSeq uint64, m wire.Message) {
	if e.refdata == nil {
		return
	}
	di, ok := e.refdata.defInfoFor(ch, instrID)
	if !ok {
		return // refdata not ready or instrument unknown → silent
	}
	// Only enforce known priceBound values (1=Bounded[0,1], 2=Non-negative).
	// Reserved or future values are silently ignored to avoid false positives.
	if di.priceBound != 1 && di.priceBound != 2 {
		return
	}

	switch m.Type {
	case wire.TypeOrderAdd:
		price := orderAddPrice(m)
		// priceBound 1 (Bounded[0,1]) or 2 (Non-negative): flag price < 0.
		// Upper-bound check for priceBound==1 omitted (requires price exponent) to avoid false positives.
		if price < 0 {
			e.Emit("FIELD.ORDERADD_PRICE_BOUND", core.Violation, core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: OrderAdd price %d < 0 violates Price Bound=%d",
					instrID, price, di.priceBound))
		}
	case wire.TypeOrderExecute:
		price := orderExecuteExecPrice(m)
		if price < 0 {
			e.Emit("REF.EXEC_PRICE_BOUND", core.Violation, core.PortMktData, frameSeq, ch, instrID,
				fmt.Sprintf("instrument %d: OrderExecute exec price %d < 0 violates Price Bound=%d",
					instrID, price, di.priceBound))
		}
	}
}
