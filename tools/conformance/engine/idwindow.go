package engine

// idWindow is a set of uint64 IDs with a fixed maximum size.
//
// The market-by-order book keeps IDs that left the book, so that a later
// message for one of them gets the correct finding. Without a limit, these sets
// grow for the full length of a Reset Count era, and a busy feed adds millions
// of IDs each hour. When an idWindow is full, add evicts the oldest ID.
//
// An evicted ID is unknown again, so a miss is not proof that the ID was never
// added. The window records the lowest and highest evicted ID. A miss in that
// range is a possible eviction, and the caller reports Unverifiable with
// ReasonOverflow. A miss out of that range is a true miss. Order IDs and Trade
// IDs usually increase, so a dangling ID above every evicted ID still gets a
// decision.
type idWindow struct {
	limit int
	// members maps each ID to the insertion number of its newest queue entry. An
	// older queue entry for the same ID is stale and eviction skips it.
	members map[uint64]uint64
	// queue holds the entries in insertion order, oldest at head. A re-add or a
	// remove leaves a stale entry behind, so the queue can hold more entries than
	// members. compact drops the stale ones before the queue passes twice the
	// limit.
	queue []idEntry
	head  int
	next  uint64

	evicted    bool
	evLo, evHi uint64
}

type idEntry struct {
	id uint64
	n  uint64
}

func newIDWindow(limit int) *idWindow {
	if limit < 1 {
		limit = 1
	}
	return &idWindow{limit: limit, members: make(map[uint64]uint64)}
}

// add inserts id as the newest entry. If id is already a member, add makes it
// the newest entry again.
//
// The window evicts only when it holds more than limit members. A re-add of a
// member does not add a member, so it evicts nothing: with limit 2, adds 7, 8, 8
// keep both 7 and 8. An earlier version evicted on every add once a fixed ring
// was full, so each repeated ID pushed out another one.
func (w *idWindow) add(id uint64) {
	w.next++
	w.members[id] = w.next
	w.queue = append(w.queue, idEntry{id, w.next})
	for len(w.members) > w.limit {
		old := w.queue[w.head]
		w.head++
		if cur, ok := w.members[old.id]; ok && cur == old.n {
			delete(w.members, old.id)
			w.noteEviction(old.id)
		}
	}
	w.compact()
}

// compact drops the stale entries once the queue holds more than twice the
// limit, so its size stays bounded and the cost of add stays constant on average.
func (w *idWindow) compact() {
	if len(w.queue)-w.head <= 2*w.limit {
		return
	}
	kept := make([]idEntry, 0, len(w.members))
	for _, e := range w.queue[w.head:] {
		if cur, ok := w.members[e.id]; ok && cur == e.n {
			kept = append(kept, e)
		}
	}
	w.queue, w.head = kept, 0
}

func (w *idWindow) noteEviction(id uint64) {
	if !w.evicted {
		w.evicted, w.evLo, w.evHi = true, id, id
		return
	}
	w.evLo = min(w.evLo, id)
	w.evHi = max(w.evHi, id)
}

// remove deletes id. A remove is not an eviction: the caller knows the ID's
// state, so the evicted range does not change.
func (w *idWindow) remove(id uint64) {
	delete(w.members, id)
}

func (w *idWindow) has(id uint64) bool {
	_, ok := w.members[id]
	return ok
}

// mayHaveEvicted reports whether id is in the range of evicted IDs. It is true
// for an evicted ID, and for some IDs that were never added.
func (w *idWindow) mayHaveEvicted(id uint64) bool {
	return w.evicted && id >= w.evLo && id <= w.evHi
}

func (w *idWindow) size() int { return len(w.members) }
