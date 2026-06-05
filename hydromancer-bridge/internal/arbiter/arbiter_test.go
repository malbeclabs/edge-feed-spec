package arbiter

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

func q(id uint32, ts uint64, bid int64) wire.Quote {
	return wire.Quote{InstrumentID: id, SourceTimestamp: ts, BidPrice: bid, AskPrice: bid + 1}
}

func TestFirstReceivedWins(t *testing.T) {
	a := New()

	// First copy of an update is accepted.
	if !a.Accept(q(1, 100, 50)) {
		t.Fatal("first quote should be accepted")
	}
	// Identical copy from a slower feed (same ts, same content) is dropped.
	if a.Accept(q(1, 100, 50)) {
		t.Fatal("duplicate quote should be dropped")
	}
	// A newer venue update passes.
	if !a.Accept(q(1, 101, 51)) {
		t.Fatal("newer quote should be accepted")
	}
	// A stale (older ts) copy is dropped.
	if a.Accept(q(1, 100, 50)) {
		t.Fatal("stale quote should be dropped")
	}
	// Different instrument is independent.
	if !a.Accept(q(2, 100, 50)) {
		t.Fatal("other instrument should be accepted")
	}
}

func TestTieBreakOnContent(t *testing.T) {
	a := New()
	// Same timestamp but different content (e.g. venue ts == 0) still passes.
	if !a.Accept(q(1, 0, 50)) {
		t.Fatal("first should be accepted")
	}
	if a.Accept(q(1, 0, 50)) {
		t.Fatal("identical should be dropped")
	}
	if !a.Accept(q(1, 0, 60)) {
		t.Fatal("changed content at same ts should be accepted")
	}
}
