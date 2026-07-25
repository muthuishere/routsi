// Package server — scenarios_test.go. These integration-style tests double
// as executable examples backing the docs "Scenarios" page: each subtest
// name is a scenario title, driven end-to-end through httptest + mock
// upstreams, reusing the helpers defined in server_test.go and
// heartbeat_test.go (mockUpstream, testServer, post, slowRegistry, ...).
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// Scenario 1: token auth — /v1/* and /stats require a bearer token when
// auth.tokens_env is set; /health and the dashboard's ?token= path stay
// reachable.
func TestScenario_TokenAuth(t *testing.T) {
	var x string
	up := mockUpstream(t, "cheap", &x)
	defer up.Close()
	t.Setenv("ROUTSI_SCENARIO_TOKENS", "sc-tok-1,sc-tok-2")

	yaml := `
default: gpt-cheap
auth:
  tokens_env: ROUTSI_SCENARIO_TOKENS
models:
  - name: gpt-cheap
    type: forward
    base_url: ` + up.URL + `
`
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := `{"model":"gpt-cheap","messages":[{"role":"user","content":"hi"}]}`

	t.Run("without_authorization_401", func(t *testing.T) {
		resp := post(t, ts.URL, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("with_bearer_token_200", func(t *testing.T) {
		resp := post(t, ts.URL, body, map[string]string{"Authorization": "Bearer sc-tok-2"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("health_open_without_token", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("dashboard_via_query_token", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/?token=sc-tok-1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// Scenario 2: auto routing — model:"auto" classifies the task and routes to
// the cheap or strong tier, disclosing the choice via X-Selected-Model.
func TestScenario_AutoRouting(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	t.Run("simple_question_routes_cheap", func(t *testing.T) {
		resp := post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"what time is it?"}]}`, nil)
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Selected-Model"); got != "gpt-cheap" {
			t.Fatalf("X-Selected-Model = %q, want gpt-cheap", got)
		}
	})

	t.Run("hard_question_routes_strong", func(t *testing.T) {
		resp := post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"prove that P != NP step by step"}]}`, nil)
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
			t.Fatalf("X-Selected-Model = %q, want gpt-strong", got)
		}
	})
}

// Scenario 3: concrete bypass — naming a real model skips the router
// entirely; the other configured upstream is never touched.
func TestScenario_ConcreteBypass(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"gpt-strong","messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Selected-Model"); got != "gpt-strong" {
		t.Fatalf("X-Selected-Model = %q, want gpt-strong", got)
	}
	if strongModel != "strong-upstream" {
		t.Fatalf("upstream saw model %q, want strong-upstream", strongModel)
	}
	if cheapModel != "" {
		t.Fatalf("bypass must not touch the cheap upstream, saw %q", cheapModel)
	}
}

// Scenario 4: streaming + heartbeat — a slow buffered backend under
// stream:true yields one or more SSE ping comments before the real chunk,
// then the chunk, then [DONE] — the client never sees a stalled connection.
func TestScenario_StreamingWithHeartbeat(t *testing.T) {
	ts := heartbeatServer(t, 25*time.Millisecond, slowRegistry(100*time.Millisecond, "scenario answer"))

	resp := post(t, ts.URL, `{"model":"my-agent","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	var sawPing, sawData, sawDone bool
	pingBeforeData := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == ": ping":
			sawPing = true
			if !sawData {
				pingBeforeData = true
			}
		case line == "data: [DONE]":
			sawDone = true
		case strings.HasPrefix(line, "data: ") && strings.Contains(line, "scenario answer"):
			sawData = true
		}
	}
	if !sawPing {
		t.Fatal("expected at least one heartbeat ping")
	}
	if !pingBeforeData {
		t.Fatal("expected the ping to precede the real data chunk")
	}
	if !sawData {
		t.Fatal("expected the real answer chunk")
	}
	if !sawDone {
		t.Fatal("expected a terminating [DONE]")
	}
}

// Scenario 5: pull-worker — a remote worker registers a queue, long-polls
// for jobs, answers one, and the answer round-trips back to the chat client
// exactly like any other backend.
func TestScenario_PullWorker(t *testing.T) {
	var x string
	up := mockUpstream(t, "u", &x)
	defer up.Close()
	up2 := mockUpstream(t, "u2", &x)
	defer up2.Close()
	ts := testServer(t, up.URL, up2.URL, echoRegistry())

	rr, err := http.Post(ts.URL+"/v1/workers/register", "application/json", strings.NewReader(`{"name":"scenario-worker"}`))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	rr.Body.Close()
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", rr.StatusCode)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := http.Get(ts.URL + "/v1/workers/scenario-worker/jobs?wait=5")
		if err != nil || r.StatusCode != http.StatusOK {
			t.Errorf("poll: err=%v status=%v", err, r)
			return
		}
		var job struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()
		ab, _ := json.Marshal(map[string]string{"content": "worker answered the scenario"})
		pr, err := http.Post(ts.URL+"/v1/workers/scenario-worker/jobs/"+job.ID, "application/json", bytes.NewReader(ab))
		if err != nil {
			t.Errorf("answer post: %v", err)
			return
		}
		pr.Body.Close()
	}()

	var got string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := post(t, ts.URL, `{"model":"scenario-worker","messages":[{"role":"user","content":"question"}]}`, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var cr api.ChatResponse
			json.Unmarshal(b, &cr)
			got = cr.Choices[0].Message.Content
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done

	if got != "worker answered the scenario" {
		t.Fatalf("client got %q, want the worker's answer", got)
	}
}
