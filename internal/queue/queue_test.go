package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
)

func testReq(text string) *api.ChatRequest {
	c, _ := json.Marshal(text)
	return &api.ChatRequest{Model: "q", Messages: []api.Message{{Role: "user", Content: c}}}
}

// startWorker polls once and answers with reply. Returns when it has answered.
func startWorker(t *testing.T, b *Broker, name, reply string, wg *sync.WaitGroup) {
	t.Helper()
	go func() {
		defer wg.Done()
		job, ok := b.Poll(context.Background(), name, 2*time.Second)
		if !ok {
			t.Errorf("worker got no job")
			return
		}
		if err := b.Answer(name, job.ID, api.Result{Content: reply}); err != nil {
			t.Errorf("answer: %v", err)
		}
	}()
}

func TestSubmitPollAnswerRoundtrip(t *testing.T) {
	b := New()
	b.Register("q")
	// Prime liveness: a poll makes the worker "present".
	var wg sync.WaitGroup
	wg.Add(1)
	startWorker(t, b, "q", "hello back", &wg)
	// Give the worker a moment to register its poll, then submit.
	waitPresent(t, b, "q")

	ans, err := b.Submit(context.Background(), "q", testReq("hi"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ans.Content != "hello back" {
		t.Fatalf("answer = %q", ans.Content)
	}
	wg.Wait()
	if s, _ := b.Status("q"); s.Served != 1 {
		t.Fatalf("served = %d", s.Served)
	}
}

func TestSubmitFastFailsWithNoWorker(t *testing.T) {
	b := New()
	b.Register("q") // registered but never polled
	_, err := b.Submit(context.Background(), "q", testReq("hi"))
	if !errors.Is(err, ErrNoWorker) {
		t.Fatalf("want ErrNoWorker, got %v", err)
	}
	// Unregistered queue also fast-fails.
	if _, err := b.Submit(context.Background(), "nope", testReq("hi")); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("unregistered: want ErrNoWorker, got %v", err)
	}
}

func TestDuplicateAnswerRejected(t *testing.T) {
	b := New()
	b.Register("q")
	// Worker polls (present), we submit, worker answers twice.
	done := make(chan string, 1)
	go func() {
		job, ok := b.Poll(context.Background(), "q", 2*time.Second)
		if !ok {
			done <- "no job"
			return
		}
		first := b.Answer("q", job.ID, api.Result{Content: "one"})
		second := b.Answer("q", job.ID, api.Result{Content: "two"})
		if first != nil {
			done <- "first failed"
			return
		}
		if !errors.Is(second, ErrJobNotInflight) {
			done <- "second not rejected"
			return
		}
		done <- "ok"
	}()
	waitPresent(t, b, "q")
	ans, err := b.Submit(context.Background(), "q", testReq("hi"))
	if err != nil || ans.Content != "one" {
		t.Fatalf("submit = %q, %v", ans.Content, err)
	}
	if got := <-done; got != "ok" {
		t.Fatalf("worker: %s", got)
	}
}

func TestAnswerAfterTimeoutDropped(t *testing.T) {
	b := New()
	b.maxWait = 40 * time.Millisecond
	b.Register("q")
	// Worker polls (present) but never answers within maxWait.
	var jobID string
	polled := make(chan struct{})
	go func() {
		job, ok := b.Poll(context.Background(), "q", 2*time.Second)
		if ok {
			jobID = job.ID
		}
		close(polled)
	}()
	waitPresent(t, b, "q")
	_, err := b.Submit(context.Background(), "q", testReq("hi"))
	if !errors.Is(err, ErrNoWorker) {
		t.Fatalf("want ErrNoWorker on timeout, got %v", err)
	}
	<-polled
	// A late answer for the dropped job must be rejected, not panic.
	if err := b.Answer("q", jobID, api.Result{Content: "late"}); !errors.Is(err, ErrJobNotInflight) {
		t.Fatalf("late answer: want ErrJobNotInflight, got %v", err)
	}
}

func TestNewWithConfigAppliesOverridesAndDefaults(t *testing.T) {
	b := NewWithConfig(5*time.Second, time.Minute)
	if b.freshness != 5*time.Second {
		t.Fatalf("freshness = %v, want 5s", b.freshness)
	}
	if b.maxWait != time.Minute {
		t.Fatalf("maxWait = %v, want 1m", b.maxWait)
	}
	// Zero values fall back to the package defaults.
	b = NewWithConfig(0, 0)
	if b.freshness != DefaultFreshness {
		t.Fatalf("freshness = %v, want default %v", b.freshness, DefaultFreshness)
	}
	if b.maxWait != DefaultMaxWait {
		t.Fatalf("maxWait = %v, want default %v", b.maxWait, DefaultMaxWait)
	}
}

func TestStateTransitions(t *testing.T) {
	b := New()
	now := time.Unix(1000, 0)
	b.now = func() time.Time { return now }
	b.Register("q")

	if s, _ := b.Status("q"); s.State != "reserved" {
		t.Fatalf("fresh queue state = %q, want reserved", s.State)
	}
	// A poll makes it online. Run poll in a goroutine (it blocks for wait).
	go b.Poll(context.Background(), "q", 20*time.Millisecond)
	waitPresent(t, b, "q")
	if s, _ := b.Status("q"); s.State != "online" {
		t.Fatalf("after poll state = %q, want online", s.State)
	}
	// Advance past freshness ⇒ stale.
	now = now.Add(time.Minute)
	if s, _ := b.Status("q"); s.State != "stale" {
		t.Fatalf("after idle state = %q, want stale", s.State)
	}
}

// waitPresent blocks until a worker poll has registered liveness for name.
func waitPresent(t *testing.T, b *Broker, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q := b.get(name); q != nil && b.present(q) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("worker never became present on %q", name)
}
