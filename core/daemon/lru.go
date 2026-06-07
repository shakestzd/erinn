package daemon

import "container/list"

// lruSet is a bounded set of op_ids with least-recently-used eviction. It
// is the in-memory dedup index for the socket listener (MVP-2). Not safe
// for concurrent use — the listener guards it with dedupMu.
//
// Durable cross-restart dedup is DEFERRED to slice-3: this set loses all
// state on process exit, which is acceptable for MVP-2 because no caller
// is wired yet and a replayed op across a restart simply re-applies an
// idempotent derived-index mutation.
type lruSet struct {
	cap   int
	ll    *list.List
	index map[string]*list.Element
}

func newLRUSet(capacity int) *lruSet {
	if capacity <= 0 {
		capacity = 1
	}
	return &lruSet{
		cap:   capacity,
		ll:    list.New(),
		index: make(map[string]*list.Element, capacity),
	}
}

// contains reports membership and, on a hit, marks the id most-recently
// used so a steady stream of distinct ids doesn't evict a hot retry key.
func (s *lruSet) contains(id string) bool {
	el, ok := s.index[id]
	if !ok {
		return false
	}
	s.ll.MoveToFront(el)
	return true
}

// add inserts id (idempotent), evicting the least-recently-used entry when
// over capacity.
func (s *lruSet) add(id string) {
	if el, ok := s.index[id]; ok {
		s.ll.MoveToFront(el)
		return
	}
	el := s.ll.PushFront(id)
	s.index[id] = el
	if s.ll.Len() > s.cap {
		oldest := s.ll.Back()
		if oldest != nil {
			s.ll.Remove(oldest)
			delete(s.index, oldest.Value.(string))
		}
	}
}

// len returns the current number of tracked ids (for tests).
func (s *lruSet) len() int { return s.ll.Len() }
