package engine

// book_test.go — Unit tests for the MBO delta book builder (Task 24).
//
// Tests drive instrBook directly via applyOrderAdd / applyOrderExecute /
// applyOrderCancel and assert remaining-qty tracking, order removal, typed
// error returns, and the Orders() snapshot accessor.

import (
	"errors"
	"testing"
)

// makeBook returns a freshly initialised instrBook for use in tests.
func makeBook() *instrBook {
	return newInstrBook()
}

// TestBookOrderAddAndPartialExecute: add an order, execute partially, assert
// remaining decrements; then full-fill, assert order is removed.
func TestBookOrderAddAndPartialExecute(t *testing.T) {
	bk := makeBook()

	// Add order id=1, bid (side=0), price=100, qty=5, flags=0, enterTS=1000.
	if err := bk.applyOrderAdd(1, 0, 100, 5, 0, 1000); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	// Partial execute: qty=2, fullFill=false.
	if err := bk.applyOrderExecute(1, 2, false); err != nil {
		t.Fatalf("applyOrderExecute partial: unexpected error: %v", err)
	}

	snap := bk.Orders()
	ro, ok := snap[1]
	if !ok {
		t.Fatal("order id=1 should still be live after partial fill")
	}
	if ro.remaining != 3 {
		t.Errorf("remaining: got %d, want 3", ro.remaining)
	}
	if ro.side != 0 {
		t.Errorf("side: got %d, want 0 (bid)", ro.side)
	}
	if ro.price != 100 {
		t.Errorf("price: got %d, want 100", ro.price)
	}
	if ro.orderFlags != 0 {
		t.Errorf("orderFlags: got %d, want 0", ro.orderFlags)
	}
	if ro.enterTS != 1000 {
		t.Errorf("enterTS: got %d, want 1000", ro.enterTS)
	}

	// Full fill: qty=3, fullFill=true → order removed.
	if err := bk.applyOrderExecute(1, 3, true); err != nil {
		t.Fatalf("applyOrderExecute full-fill: unexpected error: %v", err)
	}

	snap = bk.Orders()
	if _, present := snap[1]; present {
		t.Fatal("order id=1 should have been removed after full fill")
	}
}

// TestBookOrderCancel: add an order, cancel it, assert it is removed.
func TestBookOrderCancel(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(42, 1, 200, 10, 0, 2000); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	if err := bk.applyOrderCancel(42); err != nil {
		t.Fatalf("applyOrderCancel: unexpected error: %v", err)
	}

	snap := bk.Orders()
	if _, present := snap[42]; present {
		t.Fatal("order id=42 should have been removed after cancel")
	}
}

// TestBookOverfill: execute more than remaining → errOverfill.
func TestBookOverfill(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(7, 0, 50, 5, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	err := bk.applyOrderExecute(7, 6, false) // 6 > 5
	if err == nil {
		t.Fatal("expected errOverfill, got nil")
	}
	if !errors.Is(err, errOverfill) {
		t.Errorf("expected errors.Is(err, errOverfill), got: %v", err)
	}
}

// TestBookFullFillDisagreement: fullFill=true but remaining != execQty → errFullFillDisagree.
func TestBookFullFillDisagreement(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(9, 1, 75, 10, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	// execQty=3 with fullFill=true, but remaining is 10 → disagreement.
	err := bk.applyOrderExecute(9, 3, true)
	if err == nil {
		t.Fatal("expected errFullFillDisagree, got nil")
	}
	if !errors.Is(err, errFullFillDisagree) {
		t.Errorf("expected errors.Is(err, errFullFillDisagree), got: %v", err)
	}
}

// TestBookHiddenOrderSkipsQtyConservation: hidden orders (OrderFlags bit2=1)
// are exempt from over-fill and full-fill-disagreement checks.
func TestBookHiddenOrderSkipsQtyConservation(t *testing.T) {
	bk := makeBook()

	// Hidden flag = 0x04 (bit 2).
	if err := bk.applyOrderAdd(5, 0, 100, 5, 0x04, 0); err != nil {
		t.Fatalf("applyOrderAdd (hidden): unexpected error: %v", err)
	}

	// Over-fill on hidden order → no error.
	if err := bk.applyOrderExecute(5, 99, false); err != nil {
		t.Errorf("hidden order over-fill: expected nil error, got: %v", err)
	}

	// Re-add (hidden can be re-inserted).
	if err := bk.applyOrderAdd(5, 0, 100, 5, 0x04, 0); err != nil {
		t.Fatalf("applyOrderAdd (hidden re-add): unexpected error: %v", err)
	}

	// Full-fill flag disagreement on hidden order → no error.
	if err := bk.applyOrderExecute(5, 1, true); err != nil {
		t.Errorf("hidden order full-fill disagreement: expected nil error, got: %v", err)
	}
}

// TestBookOrdersSnapshot: Orders() returns a copy, not the live map.
func TestBookOrdersSnapshot(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(1, 0, 100, 5, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}
	if err := bk.applyOrderAdd(2, 1, 200, 3, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	snap := bk.Orders()
	if len(snap) != 2 {
		t.Fatalf("snapshot length: got %d, want 2", len(snap))
	}

	// Mutate snapshot — must not affect the book.
	delete(snap, 1)

	snap2 := bk.Orders()
	if len(snap2) != 2 {
		t.Errorf("book modified by snapshot mutation: got %d orders, want 2", len(snap2))
	}
}

// TestBookOverfillWithFullFillRemovesOrder: when execQty > remaining AND fullFill=true,
// applyOrderExecute returns errOverfill but must still remove the order from live so
// that subsequent ops see it as removed (not live).
func TestBookOverfillWithFullFillRemovesOrder(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(11, 0, 100, 5, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	err := bk.applyOrderExecute(11, 10, true) // 10 > 5, full-fill
	if !errors.Is(err, errOverfill) {
		t.Errorf("expected errOverfill, got: %v", err)
	}

	// Order must be removed even though overfill error was returned.
	snap := bk.Orders()
	if _, present := snap[11]; present {
		t.Error("order id=11 should be removed after full-fill over-fill")
	}
}

// TestBookFullFillDisagreementRemovesOrder: fullFill=true with execQty != remaining
// returns errFullFillDisagree but must still remove the order.
func TestBookFullFillDisagreementRemovesOrder(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(12, 1, 200, 10, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	err := bk.applyOrderExecute(12, 3, true) // 3 != 10, full-fill flag set
	if !errors.Is(err, errFullFillDisagree) {
		t.Errorf("expected errFullFillDisagree, got: %v", err)
	}

	// Order must be removed even though full-fill-disagree error was returned.
	snap := bk.Orders()
	if _, present := snap[12]; present {
		t.Error("order id=12 should be removed after full-fill disagreement")
	}
}

// TestBookRemoveOnZeroRemaining: execute exactly remaining without fullFill=true
// should still remove the order (remaining hits 0).
func TestBookRemoveOnZeroRemaining(t *testing.T) {
	bk := makeBook()

	if err := bk.applyOrderAdd(3, 0, 50, 4, 0, 0); err != nil {
		t.Fatalf("applyOrderAdd: unexpected error: %v", err)
	}

	// Execute all 4 without setting fullFill.
	if err := bk.applyOrderExecute(3, 4, false); err != nil {
		t.Fatalf("applyOrderExecute: unexpected error: %v", err)
	}

	snap := bk.Orders()
	if _, present := snap[3]; present {
		t.Error("order id=3 should be removed when remaining reaches 0")
	}
}
