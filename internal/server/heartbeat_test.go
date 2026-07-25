package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
)

// slowRegistry registers a "slow" custom handler that blocks for delay before
// answering — it stands in for any buffered backend (agent, pull-worker,
// translated upstream) that produces nothing for a while inside Stream.
func slowRegistry(delay time.Duration, answer string) *backend.Registry {
	reg := backend.NewRegistry()
	reg.Register("slow", backend.Completer(func(ctx context.Context, req *api.ChatRequest) (string, error) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return answer, nil
	}))
	return reg
}

func heartbeatServer(t *testing.T, heartbeat time.Duration, reg *backend.Registry) *httptest.Server {
	t.Helper()
	yaml := fmt.Sprintf(`
default: my-agent
stream_heartbeat: %s
models:
  - name: my-agent
    type: custom
    handler: slow
`, heartbeat.String())
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	s, err := New(cfg, reg, nil)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestStreamHeartbeatPingsBeforeSlowBackendAnswers: a buffered backend that
// sleeps well past the heartbeat interval must yield at least one SSE
// comment ping BEFORE its real data chunk, then the chunk, then [DONE].
func TestStreamHeartbeatPingsBeforeSlowBackendAnswers(t *testing.T) {
	ts := heartbeatServer(t, 35*time.Millisecond, slowRegistry(120*time.Millisecond, "slow answer"))

	resp := post(t, ts.URL, `{"model":"my-agent","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}

	pingIdx, dataIdx, doneIdx := -1, -1, -1
	for i, l := range lines {
		switch {
		case l == ": ping" && pingIdx == -1:
			pingIdx = i
		case strings.HasPrefix(l, "data: ") && strings.Contains(l, "slow answer") && dataIdx == -1:
			dataIdx = i
		case l == "data: [DONE]":
			doneIdx = i
		}
	}

	if pingIdx == -1 {
		t.Fatalf("no heartbeat ping seen; lines=%v", lines)
	}
	if dataIdx == -1 {
		t.Fatalf("no data chunk with the real answer; lines=%v", lines)
	}
	if doneIdx == -1 {
		t.Fatalf("stream never closed with [DONE]; lines=%v", lines)
	}
	if !(pingIdx < dataIdx && dataIdx < doneIdx) {
		t.Fatalf("wrong order: ping=%d data=%d done=%d, lines=%v", pingIdx, dataIdx, doneIdx, lines)
	}
}

// TestStreamHeartbeatDisabled: stream_heartbeat: 0 means no ping lines ever,
// even across a slow backend — only the real chunk and [DONE].
func TestStreamHeartbeatDisabled(t *testing.T) {
	ts := heartbeatServer(t, 0, slowRegistry(80*time.Millisecond, "quiet answer"))

	resp := post(t, ts.URL, `{"model":"my-agent","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if strings.Contains(text, ": ping") {
		t.Fatalf("heartbeat disabled but got a ping line:\n%s", text)
	}
	if !strings.Contains(text, "quiet answer") || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("missing real content/[DONE]:\n%s", text)
	}
}

// TestStreamHeartbeatDoesNotCorruptNormalChunks: with heartbeats enabled but
// a FAST backend (never idle past the interval), no ping lines appear and the
// normal SSE frames are unaffected — heartbeats never interleave into a
// chunk's bytes.
func TestStreamHeartbeatDoesNotCorruptNormalChunks(t *testing.T) {
	ts := heartbeatServer(t, 200*time.Millisecond, slowRegistry(1*time.Millisecond, "fast answer"))

	resp := post(t, ts.URL, `{"model":"my-agent","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if strings.Contains(text, ": ping") {
		t.Fatalf("unexpected ping on a fast backend:\n%s", text)
	}
	if !strings.Contains(text, "fast answer") || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("missing real content/[DONE]:\n%s", text)
	}
	// Every non-blank line must be a well-formed SSE frame: either a comment
	// ("^:") or a data frame ("^data: ") — never a half-written mix of both.
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, ":") && !strings.HasPrefix(l, "data: ") {
			t.Fatalf("malformed SSE line %q in:\n%s", l, text)
		}
	}
}
