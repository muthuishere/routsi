// Package queue is the broker for "pull workers": instead of routsi invoking a
// local agent CLI, a remote worker (someone's opencode/codex/claude loop)
// registers a named queue, long-polls for questions, answers them with its own
// agent, and posts answers back. A request routed to a queue blocks until a
// worker answers or the context/timeout ends. Everything is in-memory, bounded,
// single-instance (ADR-001). Additive — nothing else in routsi changes.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/muthuishere/routsi/internal/api"
)

// ErrNoWorker is returned by Submit when no worker is available to answer
// (queue unregistered, or no poll within the freshness window). The server
// maps it to HTTP 503 so callers fail fast instead of blocking (ADR-001).
var ErrNoWorker = errors.New("no worker available for queue")

// ErrJobNotInflight is returned by Answer for an unknown/expired/duplicate job.
var ErrJobNotInflight = errors.New("job not in flight (expired or already answered)")

// Job is one question handed to a worker.
type Job struct {
	ID             string        `json:"id"`
	Model          string        `json:"model"`
	ConversationID string        `json:"conversation_id,omitempty"`
	Messages       []api.Message `json:"messages"`

	answer chan result
}

type result struct {
	content string
	err     error
}

type queue struct {
	pending  chan *Job
	mu       sync.Mutex
	inflight map[string]*Job
	lastPoll time.Time // last worker poll — liveness/heartbeat
	everPoll bool
	served   int64
	errored  int64
	lastErr  string
	lastErrT time.Time
}

// Status is the operational view of a queue (ADR-004). No prompt content.
type Status struct {
	Name        string `json:"name"`
	State       string `json:"state"` // reserved|online|busy|stale
	LastSeenSec int64  `json:"last_seen_sec"`
	InFlight    int    `json:"in_flight"`
	Served      int64  `json:"served"`
	Errored     int64  `json:"errored"`
	LastError   string `json:"last_error,omitempty"`
}

// Broker holds named queues.
type Broker struct {
	mu         sync.RWMutex
	queues     map[string]*queue
	maxWait    time.Duration // cap on how long a request blocks for a worker
	freshness  time.Duration // a worker is "present" if it polled within this
	pendingCap int
	now        func() time.Time
}

// DefaultFreshness/DefaultMaxWait are the broker's built-in operational
// values, used whenever config leaves them at zero (internal/config
// WorkersConfig).
const (
	DefaultFreshness = 30 * time.Second
	DefaultMaxWait   = 5 * time.Minute
)

func New() *Broker {
	return NewWithConfig(DefaultFreshness, DefaultMaxWait)
}

// NewWithConfig builds a broker with an operator-tunable freshness window and
// max-wait cap; a zero value falls back to the package default.
func NewWithConfig(freshness, maxWait time.Duration) *Broker {
	if freshness <= 0 {
		freshness = DefaultFreshness
	}
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}
	return &Broker{
		queues:     map[string]*queue{},
		maxWait:    maxWait,
		freshness:  freshness,
		pendingCap: 256,
		now:        time.Now,
	}
}

// Register creates the queue if absent (idempotent — a worker reconnecting
// registers the same name harmlessly).
func (b *Broker) Register(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.queues[name]; !ok {
		b.queues[name] = &queue{pending: make(chan *Job, b.pendingCap), inflight: map[string]*Job{}}
	}
}

// Exists reports whether a queue is registered (used for routing).
func (b *Broker) Exists(name string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.queues[name]
	return ok
}

// Names lists registered queues.
func (b *Broker) Names() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.queues))
	for n := range b.queues {
		out = append(out, n)
	}
	return out
}

func (b *Broker) get(name string) *queue {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.queues[name]
}

// present reports whether a worker has polled recently enough to accept work.
func (b *Broker) present(q *queue) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.everPoll && b.now().Sub(q.lastPoll) <= b.freshness
}

// Submit enqueues a question and blocks until a worker answers, ctx ends, or
// maxWait elapses. Returns ErrNoWorker fast if no worker is present.
func (b *Broker) Submit(ctx context.Context, name string, req *api.ChatRequest) (string, error) {
	q := b.get(name)
	if q == nil || !b.present(q) {
		return "", ErrNoWorker
	}
	job := &Job{
		ID:             randID(),
		Model:          req.Model,
		ConversationID: req.ConversationID,
		Messages:       req.Messages,
		answer:         make(chan result, 1),
	}
	select {
	case q.pending <- job:
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		return "", ErrNoWorker // pending full ⇒ worker not draining
	}

	tctx, cancel := context.WithTimeout(ctx, b.maxWait)
	defer cancel()
	select {
	case r := <-job.answer:
		if r.err != nil {
			b.bumpErr(q, r.err.Error())
			return "", r.err
		}
		return r.content, nil
	case <-tctx.Done():
		q.drop(job.ID)
		b.bumpErr(q, "no worker answered in time")
		return "", ErrNoWorker
	}
}

// Poll is the worker long-poll: wait up to `wait` for the next job. Returns
// (nil,false) on timeout. A handed-out job is tracked until Answer or expiry.
// The poll itself is the heartbeat (refreshes liveness even when idle).
func (b *Broker) Poll(ctx context.Context, name string, wait time.Duration) (*Job, bool) {
	q := b.get(name)
	if q == nil {
		return nil, false
	}
	q.mu.Lock()
	q.lastPoll = b.now()
	q.everPoll = true
	q.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case job := <-q.pending:
		q.mu.Lock()
		q.inflight[job.ID] = job
		q.mu.Unlock()
		return job, true
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// Answer delivers a worker's answer to the blocked Submit. Idempotent: a
// second answer for the same job (or one that already timed out) returns
// ErrJobNotInflight.
func (b *Broker) Answer(name, jobID, content string) error {
	q := b.get(name)
	if q == nil {
		return ErrJobNotInflight
	}
	q.mu.Lock()
	job, ok := q.inflight[jobID]
	delete(q.inflight, jobID)
	if ok {
		q.served++
	}
	q.mu.Unlock()
	if !ok {
		return ErrJobNotInflight
	}
	job.answer <- result{content: content}
	return nil
}

// Status returns the operational view of one queue.
func (b *Broker) Status(name string) (Status, bool) {
	q := b.get(name)
	if q == nil {
		return Status{}, false
	}
	return b.statusOf(name, q), true
}

// States returns the status of every queue.
func (b *Broker) States() []Status {
	b.mu.RLock()
	names := make([]string, 0, len(b.queues))
	qs := make([]*queue, 0, len(b.queues))
	for n, q := range b.queues {
		names = append(names, n)
		qs = append(qs, q)
	}
	b.mu.RUnlock()
	out := make([]Status, len(qs))
	for i := range qs {
		out[i] = b.statusOf(names[i], qs[i])
	}
	return out
}

func (b *Broker) statusOf(name string, q *queue) Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Status{
		Name:      name,
		InFlight:  len(q.inflight),
		Served:    q.served,
		Errored:   q.errored,
		LastError: q.lastErr,
	}
	switch {
	case !q.everPoll:
		s.State = "reserved"
		s.LastSeenSec = -1
	default:
		s.LastSeenSec = int64(b.now().Sub(q.lastPoll).Seconds())
		switch {
		case b.now().Sub(q.lastPoll) > b.freshness:
			s.State = "stale"
		case len(q.inflight) > 0:
			s.State = "busy"
		default:
			s.State = "online"
		}
	}
	return s
}

func (b *Broker) bumpErr(q *queue, msg string) {
	q.mu.Lock()
	q.errored++
	q.lastErr = msg
	q.lastErrT = b.now()
	q.mu.Unlock()
}

func (q *queue) drop(jobID string) {
	q.mu.Lock()
	delete(q.inflight, jobID)
	q.mu.Unlock()
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
