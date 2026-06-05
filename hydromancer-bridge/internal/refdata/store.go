// Package refdata maintains the Instrument ID -> definition mapping the bridge
// needs to turn numeric quotes into named, correctly-scaled Hydromancer
// messages. Definitions arrive continuously on the reference-data port (see the
// Reference Data Distribution supplement).
package refdata

import (
	"sync"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

// Store is a concurrency-safe map of the latest InstrumentDefinition seen for
// each Instrument ID, populated from whichever feed delivers it first.
//
// Because every feed in an arbitrated set shares the same instrument registry,
// a single global map serves all feeds. We keep the latest definition per ID;
// the converter only needs Symbol + exponents, so we do not gate on full
// manifest completeness — a quote whose definition has not yet arrived is
// simply dropped until the next definition cycle fills it in.
type Store struct {
	mu   sync.RWMutex
	defs map[uint32]wire.InstrumentDefinition
}

// New returns an empty Store.
func New() *Store {
	return &Store{defs: make(map[uint32]wire.InstrumentDefinition)}
}

// Put records (or replaces) the definition for its Instrument ID.
func (s *Store) Put(d wire.InstrumentDefinition) {
	s.mu.Lock()
	s.defs[d.InstrumentID] = d
	s.mu.Unlock()
}

// Lookup returns the definition for id and whether it is known.
func (s *Store) Lookup(id uint32) (wire.InstrumentDefinition, bool) {
	s.mu.RLock()
	d, ok := s.defs[id]
	s.mu.RUnlock()
	return d, ok
}

// Len reports how many definitions are currently known.
func (s *Store) Len() int {
	s.mu.RLock()
	n := len(s.defs)
	s.mu.RUnlock()
	return n
}
