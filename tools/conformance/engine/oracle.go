package engine

// oracle.go — Snapshot-vs-delta reconstruction oracle (Task 26).
//
// Rule: SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT (Must, full_book, MBO).
//
// When a snapshot group completes well-formed (onSnapGroupComplete called) and
// the delta book is provably reconstructable as-of per-instrument seq K
// (SnapshotBegin's Last Instrument Seq), this oracle diffs the snapshot book
// against the delta book and:
//
//   - Emits snapshot_audits_total{match}          when books agree.
//   - Emits snapshot_audits_total{mismatch_suspected}   on the first divergence (no CI fail).
//   - Emits snapshot_audits_total{mismatch_confirmed} + SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT
//     Violation when the SAME divergence signature reproduces across
//     cfg.OracleConfirmCycles consecutive clean cycles.
//   - Emits snapshot_audits_total{unverifiable}   when the oracle cannot prove
//     the diff is not explained by loss/cold-start.
//
// As-of-K correctness:
//
//	The oracle compares the snapshot (taken at per-instrument seq K) against the
//	delta book. Comparing is only provably correct when the delta book's
//	lastInstrSeq == K at the time the group completes (the common idle/paused
//	case). If lastInstrSeq != K (book advanced past K, or is behind K), or if
//	any gating condition fails (gateConsumer, bookTrusted, refdata), the oracle
//	emits "unverifiable" — never a mismatch.
//
// False-positive guarantees:
//
//  1. Only diffs when lastInstrSeq[I] == K AND gateConsumer holds (gapless
//     per-instrument history, gapless mktdata channel, refdata ready).
//  2. Snapshot group must be structurally valid (no ORDER_SNAPSHOT_ID_MATCH,
//     SNAPSHOT_ORDER_NO_DUP_ORDER_ID, EMPTY_BOOK_WELL_FORMED,
//     END_FIELDS_MATCH_BEGIN, or TOTAL_ORDERS_COUNT_MATCH violations). A
//     structurally-invalid group emits "unverifiable" (see openSnapshot.structuralViolation).
//  3. A one-off divergence is Suspected, not Violation.
//  4. Confirmation requires the SAME signature (same set of order-level diffs)
//     across OracleConfirmCycles consecutive CLEAN cycles. Unverifiable cycles
//     reset the suspect state (via the unverifiable helper) so they cannot
//     contribute to "consecutive clean cycles" confirmation.
//  5. Hidden orders (OrderFlags bit 2): skipped entirely on both snapshot and
//     delta sides to avoid false positives.
//  6. Duplicate/late deltas (perSeq <= lastInstrSeq) mutate the live book without
//     advancing lastInstrSeq. The oracle gates on instrTracker.bookCorruptedByDup
//     to avoid diffing a post-duplicate-mutated book at lastInstrSeq==K (which
//     would not reflect the true as-of-K state). Cleared on InstrumentReset.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// oracleDiff represents a single order-level discrepancy between the snapshot
// book and the delta (delta-derived) book.
type oracleDiff struct {
	orderID uint64
	field   string // "missing_in_delta", "extra_in_delta", "qty", "side", "price", "flags", "enterTS"
}

// unverifiable is a helper that clears suspect state (Finding 2: unverifiable
// cycles must not contribute to or continue a "consecutive clean cycles" run)
// and emits the unverifiable audit metric.
func (e *Engine) unverifiable(key instrTrackerKey) {
	// Reset suspect state so that unverifiable cycles break consecutive runs.
	// Without this, a run of: mismatch(count=1) → unverifiable → mismatch with
	// same sig would increment count to 2 and fire confirmation despite the gap.
	delete(e.mbo.oracleSuspects, key)
	e.rep.SnapshotAudit("unverifiable")
}

// runOracleForGroup is called from handleSnapEnd after onSnapGroupComplete for
// each well-formed snapshot group. It performs the as-of-K diff.
func (e *Engine) runOracleForGroup(ch uint8, snap *openSnapshot, snapPortSeq uint64) {
	if e.mbo == nil {
		return
	}
	instrID := snap.instrID
	key := instrTrackerKey{ch, instrID}

	// --- Gate 0: snapshot group must be structurally valid.
	// If any structural violation fired during this group (ORDER_SNAPSHOT_ID_MATCH,
	// SNAPSHOT_ORDER_NO_DUP_ORDER_ID, EMPTY_BOOK_WELL_FORMED, END_FIELDS_MATCH_BEGIN,
	// TOTAL_ORDERS_COUNT_MATCH), the snapshot book may be malformed. Diffing it
	// against the delta book could produce false-positive mismatches.
	if snap.structuralViolation {
		e.unverifiable(key)
		return
	}

	// --- Gate 1: gateConsumer-equivalent check for the oracle.
	// We need: refdata ready + instrument known + bookTrusted + gapless mktdata.
	// We cannot call gateConsumer (which requires a perInstrSeq argument), so
	// replicate its logic without the perSeq == lastInstrSeq+1 check (handled by
	// gate 2 below).
	if e.refdata == nil {
		e.unverifiable(key)
		return
	}
	if _, ok := e.refdata.defInfoFor(ch, instrID); !ok {
		e.unverifiable(key)
		return
	}
	dt := e.mbo.tracker(ch, instrID)
	if !dt.bookTrusted {
		e.unverifiable(key)
		return
	}
	if dt.bookCorruptedByDup {
		// A duplicate/late delta was applied to the live book after reaching some
		// per-instrument seq. The book state at lastInstrSeq==K is no longer equal
		// to the as-of-K state (replayed deltas mutated it without advancing
		// lastInstrSeq). Diffing such a book against the snapshot could produce
		// false-positive mismatches. Emit "unverifiable" instead.
		e.unverifiable(key)
		return
	}
	if !e.gateDetector() {
		e.unverifiable(key)
		return
	}

	// --- Gate 2: delta book must be exactly at seq K.
	// If lastInstrSeq is nil (no deltas ever seen) or != K, we cannot compare.
	K := snap.lastInstrSeqK
	if dt.lastInstrSeq == nil || *dt.lastInstrSeq != K {
		e.unverifiable(key)
		return
	}

	// --- Gate 3: snapshot group must be clean (no intra-group gap).
	// A dirty snapshot means we may have lost SnapshotOrder messages, so the
	// snapshot book is incomplete. Never diff on a dirty group.
	if snap.dirty {
		e.unverifiable(key)
		return
	}

	// --- Diff the two books ---
	deltaBook := e.mbo.book.book(ch, instrID).Orders() // snapshot copy
	diffs := diffBooks(snap.orders, deltaBook)

	if len(diffs) == 0 {
		// Books agree: clear any suspect state for this instrument.
		delete(e.mbo.oracleSuspects, key)
		e.rep.SnapshotAudit("match")
		return
	}

	// Books disagree: compute a canonical signature for the diff set.
	sig := diffSignature(diffs)

	// Look up / initialise suspect state.
	suspect := e.mbo.oracleSuspects[key]
	if suspect == nil {
		suspect = &oracleSuspect{}
		e.mbo.oracleSuspects[key] = suspect
	}

	confirmCycles := e.cfg.OracleConfirmCycles
	if confirmCycles <= 0 {
		confirmCycles = 2 // default
	}

	if suspect.signature == sig {
		// Same divergence as last time: increment the cycle counter.
		suspect.count++
	} else {
		// Different (or first) divergence: reset to 1.
		suspect.signature = sig
		suspect.count = 1
	}

	if suspect.count >= confirmCycles {
		// Promoted to confirmed mismatch.
		e.rep.SnapshotAudit("mismatch_confirmed")
		e.Emit("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", statusFor(true), core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: snapshot book diverges from delta book at per-instrument seq %d "+
				"(confirmed across %d cycle(s)): %s",
				instrID, K, suspect.count, sig))
		// Keep suspect.count so the next divergence will also fire immediately
		// (i.e. a persistent bug keeps emitting confirmed each cycle).
	} else {
		// First (or early) occurrence: suspected, not confirmed.
		e.rep.SnapshotAudit("mismatch_suspected")
		// Do NOT emit a Finding — Suspected does not fail CI.
	}
}

// diffBooks compares the snapshot book (from the completed group) against the
// delta book (from instrBook.Orders()). Returns a slice of oracleDiff describing
// every order-level discrepancy.
//
// Hidden orders (OrderFlags bit 2 in the snapshot OR hiddenIDs in the delta
// book) are skipped entirely to avoid false positives.
//
// Note on hidden-order ID masking (Finding 3, deferred):
//
//	snap.orders stores ALL snapshot orders including hidden ones. The first pass
//	skips hidden snapshot entries. The second pass (checking for delta orders not
//	in snapshot) uses snapOrders as a presence check, so a hidden snapshot record
//	for ID X prevents a visible delta order at the same ID from being flagged as
//	"extra_in_delta". This is a false-negative risk (missed detection), not a
//	false-positive risk. By spec, hidden orders occupy ID slots exclusively;
//	REF.DUPLICATE_LIVE_ORDERADD enforcement makes visible+hidden coexistence at
//	the same ID a separately detected violation. The false-negative is accepted.
func diffBooks(snapOrders map[uint64]snapOrderRecord, deltaOrders map[uint64]restingOrder) []oracleDiff {
	var diffs []oracleDiff

	// Check every order in the snapshot against the delta book.
	for id, so := range snapOrders {
		// Skip hidden orders from the snapshot entirely.
		if isHidden(so.orderFlags) {
			continue
		}
		do, found := deltaOrders[id]
		if !found {
			diffs = append(diffs, oracleDiff{orderID: id, field: "missing_in_delta"})
			continue
		}
		// Compare individual fields.
		if so.side != do.side {
			diffs = append(diffs, oracleDiff{orderID: id, field: "side"})
		}
		if so.price != do.price {
			diffs = append(diffs, oracleDiff{orderID: id, field: "price"})
		}
		// Qty: skip for hidden orders (neither snapshot side nor delta side would
		// be hidden here since we already skipped snapshot-side hidden above; also
		// skip if the delta book order is hidden).
		if !isHidden(do.orderFlags) {
			if so.qty != do.remaining {
				diffs = append(diffs, oracleDiff{orderID: id, field: "qty"})
			}
		}
		if so.orderFlags != do.orderFlags {
			diffs = append(diffs, oracleDiff{orderID: id, field: "flags"})
		}
		if so.enterTS != do.enterTS {
			diffs = append(diffs, oracleDiff{orderID: id, field: "enterTS"})
		}
	}

	// Check for orders in the delta book not present in the snapshot.
	for id, do := range deltaOrders {
		// Skip hidden orders from the delta book entirely.
		if isHidden(do.orderFlags) {
			continue
		}
		if _, found := snapOrders[id]; !found {
			diffs = append(diffs, oracleDiff{orderID: id, field: "extra_in_delta"})
		}
	}

	return diffs
}

// diffSignature computes a canonical, sorted, low-cardinality string
// representation of a diff set. Two runs with identical divergences (same order
// IDs with the same field mismatches) produce the same signature.
func diffSignature(diffs []oracleDiff) string {
	parts := make([]string, len(diffs))
	for i, d := range diffs {
		parts[i] = fmt.Sprintf("%d:%s", d.orderID, d.field)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
