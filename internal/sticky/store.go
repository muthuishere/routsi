// Package sticky pins conversations to models: route on the first turn, keep
// the pin for the conversation's lifetime, allow escalation only
// (docs/research/README.md #3). In-memory with TTL — OpenRouter's shipped
// recipe uses a ~5 min inactivity cache; state is disposable by design.
package sticky

import (
	"sync"
	"time"
)

type entry struct {
	model string
	seen  time.Time
}

type Store struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]entry
	now func() time.Time
}

func New(ttl time.Duration) *Store {
	s := &Store{ttl: ttl, m: map[string]entry{}, now: time.Now}
	go s.janitor()
	return s
}

// Get returns the pinned model for id, refreshing the TTL on hit.
func (s *Store) Get(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[id]
	if !ok || s.now().Sub(e.seen) > s.ttl {
		delete(s.m, id)
		return "", false
	}
	e.seen = s.now()
	s.m[id] = e
	return e.model, true
}

// Pin binds id to model (also used to re-pin on escalation).
func (s *Store) Pin(id, model string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = entry{model: model, seen: s.now()}
}

func (s *Store) janitor() {
	for range time.Tick(s.ttl) {
		s.mu.Lock()
		for id, e := range s.m {
			if s.now().Sub(e.seen) > s.ttl {
				delete(s.m, id)
			}
		}
		s.mu.Unlock()
	}
}
