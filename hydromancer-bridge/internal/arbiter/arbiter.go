// Package arbiter implements first-received-wins arbitration across multiple
// redundant top-of-book feeds.
//
// The same venue update is typically published over several feeds (different
// paths / publishers) for resilience and latency. Each carries the venue's own
// Source Timestamp, which is the common denominator across heterogeneous feeds.
// The arbiter forwards the first copy of each distinct venue update it sees and
// suppresses every later duplicate, yielding one clean, de-duplicated stream.
package arbiter

import (
	"sync"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

// last records what was most recently forwarded for an instrument, so we can
// recognise and drop duplicates arriving on slower feeds.
type last struct {
	ts      uint64
	content contentKey
}

// contentKey is the BBO payload, used to break ties when Source Timestamps are
// equal or unpopulated (some venues report 0).
type contentKey struct {
	bidPx, askPx   int64
	bidQty, askQty uint64
	flags          uint8
}

func keyOf(q wire.Quote) contentKey {
	return contentKey{
		bidPx:  q.BidPrice,
		askPx:  q.AskPrice,
		bidQty: q.BidQuantity,
		askQty: q.AskQuantity,
		flags:  q.UpdateFlags & (wire.UpdBidGone | wire.UpdAskGone),
	}
}

// Arbiter tracks the last-forwarded update per instrument.
type Arbiter struct {
	mu   sync.Mutex
	seen map[uint32]last
}

// New returns an empty Arbiter.
func New() *Arbiter {
	return &Arbiter{seen: make(map[uint32]last)}
}

// Accept reports whether q should be forwarded. It returns true for the first
// copy of each distinct update and false for duplicates seen on other feeds.
//
// A quote wins if its Source Timestamp is newer than the last forwarded for the
// instrument, or — when timestamps are equal or zero — if its BBO content
// differs. This makes redundant copies (same timestamp, same content) drop out
// while every genuine book change passes through exactly once.
func (a *Arbiter) Accept(q wire.Quote) bool {
	k := keyOf(q)
	a.mu.Lock()
	defer a.mu.Unlock()

	prev, ok := a.seen[q.InstrumentID]
	if !ok || q.SourceTimestamp > prev.ts ||
		(q.SourceTimestamp == prev.ts && k != prev.content) {
		a.seen[q.InstrumentID] = last{ts: q.SourceTimestamp, content: k}
		return true
	}
	return false
}
