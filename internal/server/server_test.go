package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
)

// mockUpstream is an OpenAI-style /chat/completions that records the model it
// was asked for and answers with its tag.
func mockUpstream(t *testing.T, tag string, gotModel *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Extra  string `json:"my_custom_field"`
		}
		_ = json.Unmarshal(body, &req)
		*gotModel = req.Model
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			for _, d := range []string{"hel", "lo " + tag} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", d)
				fl.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":%q,"choices":[{"message":{"role":"assistant","content":"answer from %s extra=%s"}}]}`, req.Model, tag, req.Extra)
	}))
}

func testServer(t *testing.T, cheap, strong string, reg *backend.Registry) *httptest.Server {
	t.Helper()
	yaml := fmt.Sprintf(`
default: gpt-cheap
sticky_ttl: 1m
tiers:
  cheap: gpt-cheap
  strong: gpt-strong
models:
  - name: gpt-cheap
    type: forward
    base_url: %s
    upstream_model: cheap-upstream
  - name: gpt-strong
    type: forward
    base_url: %s
    upstream_model: strong-upstream
  - name: my-agent
    type: custom
    handler: echo
`, cheap, strong)
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

func echoRegistry() *backend.Registry {
	reg := backend.NewRegistry()
	reg.Register("echo", backend.Completer(func(_ context.Context, req *api.ChatRequest) (string, error) {
		return "echo: " + req.LastUserText(), nil
	}))
	return reg
}

func post(t *testing.T, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func TestBypassForwardsVerbatimAndRewritesModel(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"gpt-strong","messages":[{"role":"user","content":"hi"}],"my_custom_field":"kept"}`, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if strongModel != "strong-upstream" {
		t.Fatalf("upstream saw model %q, want strong-upstream", strongModel)
	}
	if cheapModel != "" {
		t.Fatal("bypass must not touch the other upstream")
	}
	if !strings.Contains(string(b), "extra=kept") {
		t.Fatalf("unknown request fields must pass through, got %s", b)
	}
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("X-Selected-Model = %q", got)
	}
}

func TestAutoRoutesCheapVsStrong(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"what time is it?"}]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-cheap" {
		t.Fatalf("simple question routed to %q, want gpt-cheap", got)
	}

	resp = post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"prove that P != NP step by step"}]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("hard question routed to %q, want gpt-strong", got)
	}
}

func TestStickyPinsAndEscalatesOnly(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())
	conv := map[string]string{"X-Conversation-Id": "conv-42"}

	// Turn 1: easy -> cheap, pinned.
	resp := post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`, conv)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-cheap" {
		t.Fatalf("turn1 -> %q, want gpt-cheap", got)
	}
	// Turn 2: hard -> escalates to strong, re-pins.
	resp = post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"debug this race condition please"}]}`, conv)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("turn2 -> %q, want gpt-strong (escalation)", got)
	}
	// Turn 3: easy again -> must STAY strong (never downgrade mid-conversation).
	resp = post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"thanks!"}]}`, conv)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("turn3 -> %q, want gpt-strong (sticky, no downgrade)", got)
	}
}

func TestForwardStreamingPassthrough(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"gpt-cheap","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, `"content":"hel"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("SSE not relayed verbatim:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestCustomBackendEnvelope(t *testing.T) {
	var a, b string
	up1 := mockUpstream(t, "cheap", &a)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &b)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	// Non-streaming: valid chat.completion envelope.
	resp := post(t, ts.URL, `{"model":"my-agent","messages":[{"role":"user","content":"ping"}]}`, nil)
	defer resp.Body.Close()
	var cr api.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cr.Object != "chat.completion" || cr.Model != "my-agent" {
		t.Fatalf("bad envelope: %+v", cr)
	}
	if got := cr.Choices[0].Message.Content; got != "echo: ping" {
		t.Fatalf("content = %q", got)
	}
	if cr.Usage == nil || cr.Usage.TotalTokens == 0 || cr.Usage.CompletionTokens == 0 {
		t.Fatalf("usage missing or zero: %+v", cr.Usage)
	}

	// Streaming: proper chunk frames ending in [DONE].
	resp2 := post(t, ts.URL, `{"model":"my-agent","stream":true,"messages":[{"role":"user","content":"ping"}]}`, nil)
	defer resp2.Body.Close()
	var text string
	sawDone := false
	sc := bufio.NewScanner(resp2.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		var chunk api.ChatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", payload, err)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Fatalf("chunk object = %q", chunk.Object)
		}
		if d := chunk.Choices[0].Delta; d != nil {
			text += d.Content
		}
	}
	if !sawDone || text != "echo: ping" {
		t.Fatalf("stream reassembled to %q (done=%v)", text, sawDone)
	}
}

func TestFingerprintStickinessWithoutExplicitID(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	// Same first user message = same conversation fingerprint. Turn 1 easy.
	first := `{"role":"user","content":"hello there"}`
	resp := post(t, ts.URL, `{"model":"auto","messages":[`+first+`]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-cheap" {
		t.Fatalf("turn1 -> %q", got)
	}
	// Turn 2 has a hard last message but the same first message; escalates.
	resp = post(t, ts.URL, `{"model":"auto","messages":[`+first+`,{"role":"assistant","content":"hi"},{"role":"user","content":"now prove the halting problem is undecidable"}]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("turn2 -> %q", got)
	}
	// Turn 3 easy again, same fingerprint: stays strong.
	resp = post(t, ts.URL, `{"model":"auto","messages":[`+first+`,{"role":"assistant","content":"done"},{"role":"user","content":"cool"}]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("turn3 -> %q, want sticky gpt-strong", got)
	}
}

func TestUnknownModelRejected(t *testing.T) {
	var a, b string
	up1 := mockUpstream(t, "cheap", &a)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &b)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"gpt-nope","messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUpstreamRetryBeforeFirstByte(t *testing.T) {
	fails := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fails < 1 {
			fails++
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"recovered"}}]}`)
	}))
	defer up.Close()
	var x string
	up2 := mockUpstream(t, "strong", &x)
	defer up2.Close()
	ts := testServer(t, up.URL, up2.URL, echoRegistry())

	start := time.Now()
	resp := post(t, ts.URL, `{"model":"gpt-cheap","messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "recovered") {
		t.Fatalf("retry failed: status=%d body=%s (took %s)", resp.StatusCode, b, time.Since(start))
	}
}

// TestProxyManagedConversationMemory: with an explicit conversation id the
// proxy owns the transcript — the client sends only the new message, and the
// upstream must still see the prior turns (carried by toolnexus's store).
func TestProxyManagedConversationMemory(t *testing.T) {
	var msgCounts []int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		msgCounts = append(msgCounts, len(req.Messages))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()
	var x string
	up2 := mockUpstream(t, "strong", &x)
	defer up2.Close()
	ts := testServer(t, up.URL, up2.URL, echoRegistry())
	conv := map[string]string{"X-Conversation-Id": "mem-1"}

	for _, msg := range []string{"first question", "second question"} {
		resp := post(t, ts.URL, fmt.Sprintf(`{"model":"gpt-cheap","messages":[{"role":"user","content":%q}]}`, msg), conv)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, b)
		}
		var cr api.ChatResponse
		if err := json.Unmarshal(b, &cr); err != nil || cr.Choices[0].Message.Content != "ok" {
			t.Fatalf("bad envelope: %s", b)
		}
	}
	if len(msgCounts) != 2 {
		t.Fatalf("upstream hit %d times, want 2", len(msgCounts))
	}
	// Turn 2 must carry more messages than turn 1 (history + new question).
	if msgCounts[1] <= msgCounts[0] {
		t.Fatalf("no memory: upstream saw %v messages per turn", msgCounts)
	}
}

// TestDynamicGroupRoutesByTask: a `dynamic` virtual model classifies each
// request and dispatches to its low/medium/high member; conversations pin
// with escalate-only semantics inside the group.
func TestDynamicGroupRoutesByTask(t *testing.T) {
	mk := func(tag string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"from %s"}}]}`, tag)
		}))
	}
	low, med, high := mk("low"), mk("med"), mk("high")
	defer low.Close()
	defer med.Close()
	defer high.Close()

	yaml := fmt.Sprintf(`
default: m-low
models:
  - name: m-low
    type: forward
    base_url: %s
  - name: m-med
    type: forward
    base_url: %s
  - name: m-high
    type: forward
    base_url: %s
  - name: dynamic-1
    type: dynamic
    levels:
      low: m-low
      medium: m-med
      high: m-high
`, low.URL, med.URL, high.URL)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	ask := func(text string, hdr map[string]string) string {
		resp := post(t, ts.URL, fmt.Sprintf(`{"model":"dynamic-1","messages":[{"role":"user","content":%q}]}`, text), hdr)
		resp.Body.Close()
		return resp.Header.Get("X-Selected-Model")
	}

	// Stateless (no conversation): each request classified on its own.
	if got := ask("hi there", nil); got != "m-low" {
		t.Fatalf("low task -> %q", got)
	}
	if got := ask("explain closures in javascript", nil); got != "m-med" {
		t.Fatalf("medium task -> %q", got)
	}
	if got := ask("debug this race condition", nil); got != "m-high" {
		t.Fatalf("high task -> %q", got)
	}

	// Conversation: pin low, escalate to high, never downgrade.
	conv := map[string]string{"X-Conversation-Id": "dyn-conv"}
	if got := ask("hello", conv); got != "m-low" {
		t.Fatalf("turn1 -> %q", got)
	}
	if got := ask("prove this is correct step by step", conv); got != "m-high" {
		t.Fatalf("turn2 escalation -> %q", got)
	}
	if got := ask("thanks", conv); got != "m-high" {
		t.Fatalf("turn3 must stay pinned high, got %q", got)
	}
}

// TestDynamicGroupLevelFallback: a group missing `medium` serves medium
// tasks from the nearest declared level instead of failing.
func TestDynamicGroupLevelFallback(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()
	yaml := fmt.Sprintf(`
default: only
models:
  - name: only
    type: forward
    base_url: %s
  - name: dyn
    type: dynamic
    levels:
      low: only
`, up.URL)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := post(t, ts.URL, `{"model":"dyn","messages":[{"role":"user","content":"explain generics"}]}`, nil)
	resp.Body.Close()
	if got := resp.Header.Get("X-Selected-Model"); got != "only" {
		t.Fatalf("fallback -> %q, want only", got)
	}
}

// TestDynamicGroupUnknownMemberRejectedAtStartup.
func TestDynamicGroupUnknownMemberRejectedAtStartup(t *testing.T) {
	cfg, err := config.Parse([]byte(`
default: dyn
models:
  - name: dyn
    type: dynamic
    levels:
      low: nope
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, nil, nil); err == nil {
		t.Fatal("want startup error for unknown member")
	}
}

// TestTokenAuthGuardsAPI: with tokens configured, /v1/* and /stats 401 without
// a valid bearer (header or ?token=), /health stays open.
func TestTokenAuthGuardsAPI(t *testing.T) {
	var x string
	up := mockUpstream(t, "cheap", &x)
	defer up.Close()
	up2 := mockUpstream(t, "strong", &x)
	defer up2.Close()
	t.Setenv("ROUTSI_TEST_TOKENS", "tok-alpha,tok-beta")

	yaml := fmt.Sprintf(`
default: gpt-cheap
auth:
  tokens_env: ROUTSI_TEST_TOKENS
models:
  - name: gpt-cheap
    type: forward
    base_url: %s
`, up.URL)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := `{"model":"gpt-cheap","messages":[{"role":"user","content":"hi"}]}`
	// No token -> 401.
	resp := post(t, ts.URL, body, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", resp.StatusCode)
	}
	// Wrong token -> 401.
	resp = post(t, ts.URL, body, map[string]string{"Authorization": "Bearer nope"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", resp.StatusCode)
	}
	// Valid token -> 200.
	resp = post(t, ts.URL, body, map[string]string{"Authorization": "Bearer tok-beta"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good token: status %d", resp.StatusCode)
	}
	// /stats guarded, ?token= works (dashboard path).
	r2, _ := http.Get(ts.URL + "/stats")
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/stats open without token: %d", r2.StatusCode)
	}
	r3, _ := http.Get(ts.URL + "/stats?token=tok-alpha")
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("/stats with query token: %d", r3.StatusCode)
	}
	// /health always open.
	r4, _ := http.Get(ts.URL + "/health")
	if r4.StatusCode != http.StatusOK {
		t.Fatalf("/health guarded: %d", r4.StatusCode)
	}
}
