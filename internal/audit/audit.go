// Package audit is an in-memory ring buffer of routing decisions, recorded
// for operator visibility (the /audit endpoint + dashboard panel). It stores
// METADATA AND TOKEN COUNTS ONLY — never message/prompt content, and never a
// secret — so it is safe to expose to anyone who can already read /stats.
package audit

import (
	"sync"
)

// capacity bounds the ring so it never grows without limit.
const capacity = 200

// Tokens mirrors the usage the server already computes (api.Usage), kept as
// a small value type so audit doesn't need to import the api package.
type Tokens struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
}

// Decision is one completed chat request's routing outcome. No field here may
// ever carry message content or a secret value.
type Decision struct {
	Time            string `json:"time"`
	RequestedModel  string `json:"requested_model"`
	SelectedModel   string `json:"selected_model"`
	Level           string `json:"level"` // low|medium|high|"" (bypass)
	Source          string `json:"source"`
	Status          int    `json:"status"`
	LatencyMs       int64  `json:"latency_ms"`
	Tokens          Tokens `json:"tokens"`
	ConversationID  string `json:"conversation_id,omitempty"`
}

// Ring is a fixed-size, mutex-guarded circular buffer of the most recent
// decisions. Safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []Decision
	next int
	full bool
}

// New returns an empty ring at the default capacity.
func New() *Ring {
	return &Ring{buf: make([]Decision, capacity)}
}

// Record appends one decision. O(1), never blocks meaningfully and never
// returns an error — recording must not be able to fail a request.
func (r *Ring) Record(d Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = d
	r.next = (r.next + 1) % capacity
	if r.next == 0 {
		r.full = true
	}
}

// Recent returns up to limit most-recent decisions, newest first. limit <= 0
// or larger than what's stored returns everything stored.
func (r *Ring) Recent(limit int) []Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = capacity
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Decision, 0, limit)
	idx := r.next
	for i := 0; i < limit; i++ {
		idx = (idx - 1 + capacity) % capacity
		out = append(out, r.buf[idx])
	}
	return out
}
