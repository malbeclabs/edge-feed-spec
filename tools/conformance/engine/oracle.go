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
//	delta book. When the delta book's lastInstrSeq is K, it compares the two
//	books. When the delta book is past K and its delta log holds every delta
//	after K (deltaLog), it compares the delta book with the snapshot book with
//	those deltas applied. If the book is behind K, the log lacks a delta after
//	K, or any gating condition fails (gateConsumer, bookTrusted, refdata), the
//	oracle emits "unverifiable" — never a mismatch.
//
// False-positive guarantees:
//
//  1. Only diffs when the log covers K to lastInstrSeq[I] AND gateConsumer holds
//     (gapless per-instrument history since a seed, refdata ready). A mktdata gap
//     makes every book untrusted until a snapshot group seeds it again
//     (syncMktdataGaps). The instance must also have classified mktdata up to the
//     group's Anchor Seq: a lost InstrumentReset in that range shows only as a
//     gap, which a later datagram reveals.
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
	field   string // "missing_in_delta", "extra_in_delta", "qty", "side", "price", "flags", "enterTS", "fields"
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
	// We need: refdata ready + instrument known + bookTrusted.
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
		// This group seeds the book (seedBookFromSnapshot runs after the oracle). A
		// comparison with the book it seeds always matches, so it is not made. This
		// is an instrument adopting its first snapshot baseline: cold_start, the
		// routine reason, and not untrusted.
		if ok, _ := e.seedPlan(ch, snap); ok {
			e.unverifiable(key, snapPortSeq, core.ReasonColdStart,
				fmt.Sprintf("instrument %d: this snapshot group seeds the book, so there is no book to compare it with", instrID))
			return
		}
		// No group has seeded the book yet: no group was complete and clean, or a
		// per-instrument gap after K kept a group from seeding. That is the state the
		// check reads not being established yet, which is cold_start and routine, and
		// not a fault upstream.
		if !dt.seeded {
			e.unverifiable(key, snapPortSeq, core.ReasonColdStart,
				fmt.Sprintf("instrument %d: no snapshot group has seeded the book yet, so there is no book to compare", instrID))
			return
		}
		// The book was seeded, and a gap cleared its trust: a per-instrument gap
		// (applyDeltaSeq) or a gap in the mktdata series (syncMktdataGaps).
		e.unverifiable(key, snapPortSeq, core.ReasonUntrusted,
			fmt.Sprintf("instrument %d: a gap cleared the trust of the book a snapshot seeded, in its per-instrument seq or in the mktdata series", instrID))
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
	// The mktdata series must reach the group's Anchor Seq. A recovery group
	// follows its InstrumentReset at once, and when the datagram that carried the
	// reset is lost, the group can close before a later datagram reveals the gap.
	// Comparing then would grade the book from before the reset.
	if !e.mbo.highestMktdataSeqSet || snap.anchorSeq > e.mbo.highestMktdataSeq {
		e.unverifiable(key, snapPortSeq, core.ReasonPending,
			fmt.Sprintf("instrument %d: the mktdata series has not reached the group's Anchor Seq %d", instrID, snap.anchorSeq))
		return
	}

	// --- Gate 2: the delta book must be at seq K, or past it with every delta
	// after K in its log. The oracle then compares the delta book with the snapshot
	// book with those deltas applied (see deltaLog).
	K := snap.lastInstrSeqK
	if dt.lastInstrSeq == nil {
		e.unverifiable(key, snapPortSeq, core.ReasonColdStart,
			fmt.Sprintf("instrument %d: no delta seen yet, so there is no book to compare", instrID))
		return
	}
	last := *dt.lastInstrSeq
	bk := e.mbo.book.book(ch, instrID)
	if last < K {
		e.unverifiable(key, snapPortSeq, core.ReasonPending,
			fmt.Sprintf("instrument %d: book is at per-instrument seq %d, behind the snapshot taken at %d",
				instrID, last, K))
		return
	}
	if !bk.log.covers(K, last) {
		e.unverifiable(key, snapPortSeq, core.ReasonPending,
			fmt.Sprintf("instrument %d: book is at per-instrument seq %d, the snapshot was taken at %d, "+
				"and the delta log does not hold every delta between them", instrID, last, K))
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
	expected, at := snap.orders, fmt.Sprintf("at per-instrument seq %d", K)
	var replayDiffs []oracleDiff
	if last > K {
		expected, replayDiffs = replayedSnapshot(snap.orders, K, &bk.log)
		at = fmt.Sprintf("at per-instrument seq %d (the snapshot at %d with the %d logged deltas after it)",
			last, K, last-K)
	}
	deltaBook := bk.Orders() // snapshot copy
	diffs := mergeReplayDiffs(diffBooks(expected, deltaBook), replayDiffs)

	if len(diffs) == 0 {
		// Books agree: clear any suspect state for this instrument.
		delete(e.mbo.oracleSuspects, key)
		e.rep.SnapshotAudit("match")
		// Reported against the rule too, not only into snapshot_audits_total: this is
		// the positive claim the oracle exists to make, and the rule's `pass` series is
		// the only place an operator can read it per rule.
		e.passed("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: delta book matches the snapshot %s (%d order(s))",
				instrID, at, len(deltaBook)))
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
			fmt.Sprintf("instrument %d: snapshot book diverges from delta book %s "+
				"(confirmed across %d cycle(s)): %s",
				instrID, at, suspect.count, sig))
		// Keep suspect.count so the next divergence will also fire immediately
		// (i.e. a persistent bug keeps emitting confirmed each cycle).

		// The checker cannot tell whether the delta book or the snapshot is wrong,
		// and the finding above already charges the publisher. Grading the order
		// rules against a book that the publisher's own snapshot contradicts would
		// charge it again on every delta, until a mktdata gap happened to untrust
		// the book (malbeclabs/edge-feed-spec#137). So the book stops being trusted,
		// and seedBookFromSnapshot, which runs after the oracle, seeds it from this
		// group. Only a confirmed mismatch does this: a suspected one keeps the book,
		// so that the next group is compared and a repeating mismatch is confirmed.
		dt.bookTrusted = false
	} else {
		// First (or early) occurrence: suspected, not confirmed.
		e.rep.SnapshotAudit("mismatch_suspected")
		// Emitted as Suspected, which is what that status is for: it does not count
		// as a violation (core.Status.CountsAsViolation) so CI is unaffected, and the
		// opportunity is not lost from the rule's denominator. Before this, the status
		// existed in core and no engine path ever produced it — a divergence awaiting
		// confirmation was visible only in the rule-less audit counter.
		e.Emit("SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", core.Suspected, core.PortSnapshot, snapPortSeq, ch, instrID,
			fmt.Sprintf("instrument %d: snapshot book diverges from delta book %s "+
				"(%d of %d cycle(s) needed to confirm): %s",
				instrID, at, suspect.count, confirmCycles, sig))
	}
}

// replayedSnapshot returns the book that a snapshot group taken at per-instrument
// seq k and the logged deltas after k make, as snapshot records. The hidden
// orders of the snapshot stay in it, as diffBooks expects of a snapshot book.
//
// A delta after k can remove the evidence of a snapshot defect: if the snapshot
// omits order 7, or holds it with a wrong quantity, and a later delta cancels it,
// both books end the same. So it also returns a diff for each logged delta whose
// order differs between the books when the delta applies, with the labels of
// diffBooks: "extra_in_delta" when the delta book had the order and the snapshot
// book did not, "missing_in_delta" for the reverse, and "fields" when both had it
// with other fields.
func replayedSnapshot(orders map[uint64]snapOrderRecord, k uint32, log *deltaLog) (map[uint64]snapOrderRecord, []oracleDiff) {
	scratch := newInstrBook()
	scratch.seed(orders)
	var diffs []oracleDiff
	log.eachAfter(k, func(d *loggedDelta) {
		m := d.message()
		id := deltaOrderID(m)
		// An order at zero remaining stays in the delta book until its OrderCancel,
		// but a snapshot leaves it out, because a SnapshotOrder has Quantity > 0.
		// Its presence decides nothing, as in diffBooks.
		if d.zero {
			_ = scratch.applyDelta(m)
			return
		}
		so, inSnap := scratch.live[id]
		switch {
		case d.found && !inSnap:
			diffs = append(diffs, oracleDiff{orderID: id, field: "extra_in_delta"})
		case !d.found && inSnap:
			diffs = append(diffs, oracleDiff{orderID: id, field: "missing_in_delta"})
		case d.found && orderFieldsHash(so) != d.fields:
			diffs = append(diffs, oracleDiff{orderID: id, field: "fields"})
		}
		_ = scratch.applyDelta(m)
	})
	out := make(map[uint64]snapOrderRecord, len(scratch.live))
	for id, o := range orders {
		if isHidden(o.orderFlags) {
			out[id] = o
		}
	}
	for id, lo := range scratch.live {
		out[id] = snapOrderRecord{side: lo.side, orderFlags: lo.orderFlags, enterTS: lo.enterTS, price: lo.price, qty: lo.remaining}
	}
	return out, diffs
}

// mergeReplayDiffs adds to diffs the replay diffs of orders that diffs does not
// name, one per order. A defect that persists then has one signature in every
// cycle, whether or not a delta after K touched its order, because the end-state
// diff names it; and an order that many deltas touch adds one diff, not one per
// delta. Without that, the signature changed with the deltas, and the defect
// never confirmed.
func mergeReplayDiffs(diffs, replay []oracleDiff) []oracleDiff {
	named := make(map[uint64]bool, len(diffs)+len(replay))
	for _, d := range diffs {
		named[d.orderID] = true
	}
	for _, d := range replay {
		if !named[d.orderID] {
			named[d.orderID] = true
			diffs = append(diffs, d)
		}
	}
	return diffs
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
		// An order an execute left at zero stays in the delta book until its
		// OrderCancel. A snapshot leaves it out, because a SnapshotOrder has
		// Quantity > 0 (SNAP.ORDER_STRUCT_VALID), so its absence is not a
		// divergence.
		if do.remaining == 0 {
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
