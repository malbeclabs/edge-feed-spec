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
// Each of those four outcomes ALSO emits a finding against the rule — Pass,
// Suspected, Violation, Unverifiable respectively — so that every well-formed
// group this oracle is handed appears in checks_total{rule_id=...}. That counter is
// the oracle's denominator; snapshot_audits_total carries no rule_id and cannot
// serve as one. See engine/denominator.go.
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
// cycles must not contribute to or continue a "consecutive clean cycles" run),
// emits the unverifiable audit metric, and reports the opportunity against the
// rule itself.
//
// **That last part is why reason and snapPortSeq are arguments.**
// `snapshot_audits_total` is a rule-less metric: it counts oracle outcomes but
// carries no `rule_id`, so joining it to the rule catalog is not possible and
// `checks_total{rule_id="SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"}` used to stay
// empty however many groups this oracle skipped. A denominator an operator cannot
// join to the rule it belongs to is not a denominator (see engine/denominator.go).
func (e *Engine) unverifiable(key instrTrackerKey, snapPortSeq uint64, reason, detail string) {
	// Reset suspect state so that unverifiable cycles break consecutive runs.
	// Without this, a run of: mismatch(count=1) → unverifiable → mismatch with
	// same sig would increment count to 2 and fire confirmation despite the gap.
	delete(e.mbo.oracleSuspects, key)
	e.rep.SnapshotAudit("unverifiable")
	e.unverified("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", reason, core.PortSnapshot, snapPortSeq,
		key.channelID, key.instrumentID, detail)
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
		e.unverifiable(key, snapPortSeq, core.ReasonSuperseded,
			"snapshot group is structurally invalid; the group's own finding names the defect")
		return
	}

	// --- Gate 1: gateConsumer-equivalent check for the oracle.
	// We need: refdata ready + instrument known + bookTrusted + gapless mktdata.
	// We cannot call gateConsumer (which requires a perInstrSeq argument), so
	// replicate its logic without the perSeq == lastInstrSeq+1 check (handled by
	// gate 2 below).
	if e.refdata == nil {
		e.unverifiable(key, snapPortSeq, core.ReasonColdStart, "no reference data received yet")
		return
	}
	if _, ok := e.refdata.defInfoFor(ch, instrID); !ok {
		e.unverifiable(key, snapPortSeq, core.ReasonColdStart,
			fmt.Sprintf("instrument %d not yet known to channel %d's reference data", instrID, ch))
		return
	}
	dt := e.mbo.tracker(ch, instrID)
	if !dt.bookTrusted {
		// Cold start and a prior gap both land here and the tracker does not record
		// which, so the reason says what is actually known — the history is not
		// established — rather than guessing at a cause.
		e.unverifiable(key, snapPortSeq, core.ReasonUntrusted,
			fmt.Sprintf("instrument %d's order history is not established (mid-stream start or a prior per-instrument gap)", instrID))
		return
	}
	if dt.bookCorruptedByDup {
		// A duplicate/late delta was applied to the live book after reaching some
		// per-instrument seq. The book state at lastInstrSeq==K is no longer equal
		// to the as-of-K state (replayed deltas mutated it without advancing
		// lastInstrSeq). Diffing such a book against the snapshot could produce
		// false-positive mismatches. Emit "unverifiable" instead.
		e.unverifiable(key, snapPortSeq, core.ReasonReorder,
			fmt.Sprintf("instrument %d: a late or duplicate delta mutated the book without advancing its seq", instrID))
		return
	}
	if !e.gateDetector(ch) {
		e.unverifiable(key, snapPortSeq, core.ReasonLoss, "the mktdata port has a sequence gap this era")
		return
	}

	// --- Gate 2: delta book must be exactly at seq K.
	// If lastInstrSeq is nil (no deltas ever seen) or != K, we cannot compare.
	K := snap.lastInstrSeqK
	if dt.lastInstrSeq == nil {
		e.unverifiable(key, snapPortSeq, core.ReasonColdStart,
			fmt.Sprintf("instrument %d: no delta seen yet, so there is no book to compare", instrID))
		return
	}
	if *dt.lastInstrSeq != K {
		e.unverifiable(key, snapPortSeq, core.ReasonPending,
			fmt.Sprintf("instrument %d: book is at per-instrument seq %d, the snapshot was taken at %d",
				instrID, *dt.lastInstrSeq, K))
		return
	}

	// --- Gate 3: snapshot group must be clean (no intra-group gap).
	// A dirty snapshot means we may have lost SnapshotOrder messages, so the
	// snapshot book is incomplete. Never diff on a dirty group.
	if snap.dirty {
		e.unverifiable(key, snapPortSeq, core.ReasonLoss,
			"the snapshot port lost a frame during this group, so the snapshot book may be incomplete")
		return
	}

	// --- Diff the two books ---
	deltaBook := e.mbo.book.book(ch, instrID).Orders() // snapshot copy
	diffs := diffBooks(snap.orders, deltaBook)

	if len(diffs) == 0 {
		// Books agree: clear any suspect state for this instrument.
		delete(e.mbo.oracleSuspects, key)
		e.rep.SnapshotAudit("match")
		// Reported against the rule too, not only into snapshot_audits_total: this is
		// the positive claim the oracle exists to make, and the rule's `pass` series is
		// the only place an operator can read it per rule.
		e.passed("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: delta book matches the snapshot at per-instrument seq %d (%d order(s))",
				instrID, K, len(deltaBook)))
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
		// Emitted as Suspected, which is what that status is for: it does not count
		// as a violation (core.Status.CountsAsViolation) so CI is unaffected, and the
		// opportunity is not lost from the rule's denominator. Before this, the status
		// existed in core and no engine path ever produced it — a divergence awaiting
		// confirmation was visible only in the rule-less audit counter.
		e.Emit("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.Suspected, core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: snapshot book diverges from delta book at per-instrument seq %d "+
				"(%d of %d cycle(s) needed to confirm): %s",
				instrID, K, suspect.count, confirmCycles, sig))
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
