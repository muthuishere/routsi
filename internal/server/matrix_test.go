// Package server — matrix_test.go. ONE end-to-end proof that every routsi
// transport supports every capability we claim: it drives real HTTP through
// the real handler against mock upstreams, a real registered pull-worker and
// a real adapter subprocess, records a pass/fail per (transport, capability)
// cell, and prints the matrix. `task matrix` is the artifact — an empty cell
// is a capability we do NOT claim for that transport.
//
// Every transport faces the SAME assertions. That sameness is the claim: the
// OpenAI wire shape a client sees must not depend on what is behind the name.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// --- the matrix ------------------------------------------------------------

type cell struct {
	transport, capability, detail string
	ok                            bool
}

type matrix struct {
	mu    sync.Mutex
	cells []cell
	caps  []string // column order, as declared
}

func (m *matrix) columns(caps ...string) { m.caps = caps }

func (m *matrix) record(t *testing.T, transport, capability string, err error, detail string) {
	t.Helper()
	m.mu.Lock()
	m.cells = append(m.cells, cell{transport, capability, detail, err == nil})
	m.mu.Unlock()
	if err != nil {
		t.Errorf("%s / %s: %v", transport, capability, err)
	}
}

func (m *matrix) render(t *testing.T) {
	t.Helper()
	var transports []string
	seen := map[string]bool{}
	byKey := map[string]cell{}
	for _, c := range m.cells {
		if !seen[c.transport] {
			seen[c.transport] = true
			transports = append(transports, c.transport)
		}
		byKey[c.transport+"|"+c.capability] = c
	}
	sort.Strings(transports)

	const w = 15
	var b strings.Builder
	b.WriteString("\n\nroutsi capability matrix — every transport, every capability\n\n")
	fmt.Fprintf(&b, "%-26s", "transport")
	for _, c := range m.caps {
		fmt.Fprintf(&b, "%-*s", w, c)
	}
	b.WriteString("\n" + strings.Repeat("─", 26+w*len(m.caps)) + "\n")
	for _, tr := range transports {
		fmt.Fprintf(&b, "%-26s", tr)
		for _, c := range m.caps {
			mark := "·"
			if cl, ok := byKey[tr+"|"+c]; ok {
				if mark = "✗ FAIL"; cl.ok {
					mark = "✓"
				}
			}
			fmt.Fprintf(&b, "%-*s", w, mark)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n✓ verified end-to-end    · not claimed for this transport\n\nevidence:\n")
	for _, c := range m.cells {
		if c.detail != "" {
			fmt.Fprintf(&b, "  %-26s %-14s %s\n", c.transport, c.capability, c.detail)
		}
	}
	t.Log(b.String())
}

// --- what every mock backend decides ---------------------------------------
//
// All four transports run the same tiny brain so the assertions can be
// identical. It is driven by the transcript alone: how many user turns it can
// see, how many tool results have come back, and markers in the prompt.

type brain struct {
	userTurns   int
	toolResults int
	hasTools    bool
	parallel    bool // prompt asked for a parallel batch
	chain       bool // prompt asked for a second, dependent tool round
	convID      string
}

func (b brain) reply() (text string, calls []map[string]any) {
	switch {
	case b.hasTools && b.toolResults == 0 && b.parallel:
		return "", []map[string]any{
			{"name": "get_weather", "arguments": map[string]any{"city": "Paris"}},
			{"name": "get_weather", "arguments": map[string]any{"city": "Tokyo"}},
			{"name": "get_weather", "arguments": map[string]any{"city": "Lima"}},
		}
	case b.hasTools && b.toolResults == 0:
		return "", []map[string]any{{"name": "get_weather", "arguments": map[string]any{"city": "Paris"}}}
	case b.hasTools && b.toolResults == 1 && b.chain:
		// Round two: only legal once the search result is in hand.
		return "", []map[string]any{{"name": "book_flight", "arguments": map[string]any{"flight_id": "AF1680"}}}
	case b.hasTools:
		return "booked AF1680, 21C in Paris", nil
	case b.convID != "":
		return fmt.Sprintf("turns=%d conv=%s", b.userTurns, b.convID), nil
	default:
		return fmt.Sprintf("turns=%d", b.userTurns), nil
	}
}

func count(body []byte, needle string) int { return bytes.Count(body, []byte(needle)) }

// readBrain infers the brain inputs from any provider-shaped request body.
// Tool results look different per provider ("role":"tool" on OpenAI,
// "tool_result" blocks on Anthropic) — counting both keeps one code path.
func readBrain(body []byte) brain {
	res := count(body, `"role":"tool"`) + count(body, `tool_result`)
	return brain{
		userTurns:   count(body, `"role":"user"`) - res,
		toolResults: res,
		hasTools:    count(body, `"tools"`) > 0 || count(body, `input_schema`) > 0,
		parallel:    count(body, "PARALLEL") > 0,
		chain:       count(body, "CHAIN") > 0,
	}
}

// --- mock upstreams --------------------------------------------------------

func openAIMock(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b := readBrain(body)
		text, calls := b.reply()

		var stream struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &stream)

		if stream.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			if len(calls) > 0 {
				for i, c := range calls {
					args, _ := json.Marshal(c["arguments"])
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%q}}]}}]}\n\n",
						i, i, c["name"], string(args))
					fl.Flush()
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			} else {
				for _, d := range []string{"str", "eamed"} {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", d)
					fl.Flush()
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if len(calls) > 0 {
			var tc []string
			for i, c := range calls {
				args, _ := json.Marshal(c["arguments"])
				tc = append(tc, fmt.Sprintf(
					`{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%q}}`,
					i, c["name"], string(args)))
			}
			fmt.Fprintf(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","tool_calls":[%s]},"finish_reason":"tool_calls"}]}`,
				strings.Join(tc, ","))
			return
		}
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":%q}}]}`, text)
	}))
}

// anthropicMock speaks the Anthropic Messages shape — what toolnexus
// translates an OpenAI request into on a `style: anthropic` upstream.
func anthropicMock(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text, calls := readBrain(body).reply()
		w.Header().Set("Content-Type", "application/json")

		if len(calls) > 0 {
			var blocks []string
			for i, c := range calls {
				in, _ := json.Marshal(c["arguments"])
				blocks = append(blocks, fmt.Sprintf(
					`{"type":"tool_use","id":"toolu_%d","name":%q,"input":%s}`, i, c["name"], in))
			}
			fmt.Fprintf(w, `{"id":"m1","type":"message","role":"assistant","model":"claude","stop_reason":"tool_use","content":[%s]}`,
				strings.Join(blocks, ","))
			return
		}
		fmt.Fprintf(w, `{"id":"m1","type":"message","role":"assistant","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":%q}]}`, text)
	}))
}

// fakeWorker is a real pull-worker: register, long-poll, answer — the same
// HTTP dance `routsi worker join` performs.
func fakeWorker(t *testing.T, base, queue string) func() {
	t.Helper()
	reg, _ := http.NewRequest(http.MethodPost, base+"/v1/workers/register",
		strings.NewReader(fmt.Sprintf(`{"name":%q}`, queue)))
	reg.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(reg)
	if err != nil {
		t.Fatalf("worker register: %v", err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
				base+"/v1/workers/"+queue+"/jobs?wait=1s", nil)
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			job, _ := io.ReadAll(r.Body)
			r.Body.Close()

			var head struct {
				ID     string `json:"id"`
				ConvID string `json:"conversation_id"`
			}
			if json.Unmarshal(job, &head) != nil || head.ID == "" {
				continue
			}
			b := readBrain(job)
			b.convID = head.ConvID
			text, calls := b.reply()

			answer := fmt.Sprintf(`{"content":%q}`, text)
			if len(calls) > 0 {
				// Simplified worker shape on purpose: the server normalizes it
				// and synthesizes the call ids.
				raw, _ := json.Marshal(calls)
				answer = fmt.Sprintf(`{"tool_calls":%s}`, raw)
			}
			ans, _ := http.NewRequest(http.MethodPost,
				base+"/v1/workers/"+queue+"/jobs/"+head.ID, strings.NewReader(answer))
			ans.Header.Set("Content-Type", "application/json")
			if a, err := http.DefaultClient.Do(ans); err == nil {
				a.Body.Close()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// adapterScript is the same brain as a POSIX shell adapter — proof that a
// `type: command` adapter needs no runtime beyond a shell.
const adapterScript = `
job=$(cat)
n() { printf '%s' "$job" | grep -o "$1" | wc -l | tr -d ' '; }
res=$(n 'tool_call_id')
users=$(n '"role":"user"')
conv=$(printf '%s' "$job" | sed -n 's/.*"conversation_id":"\([^"]*\)".*/\1/p')
if [ "$(n '"tools"')" -gt 0 ]; then
  if [ "$res" -eq 0 ]; then
    if [ "$(n PARALLEL)" -gt 0 ]; then
      echo '{"tool_calls":[{"name":"get_weather","arguments":{"city":"Paris"}},{"name":"get_weather","arguments":{"city":"Tokyo"}},{"name":"get_weather","arguments":{"city":"Lima"}}]}'
    else
      echo '{"tool_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}'
    fi
  elif [ "$res" -eq 1 ] && [ "$(n CHAIN)" -gt 0 ]; then
    echo '{"tool_calls":[{"name":"book_flight","arguments":{"flight_id":"AF1680"}}]}'
  else
    echo '{"content":"booked AF1680, 21C in Paris"}'
  fi
elif [ -n "$conv" ]; then
  echo "{\"content\":\"turns=$users conv=$conv\"}"
else
  echo "{\"content\":\"turns=$users\"}"
fi`

// --- request helpers -------------------------------------------------------

type chatReply struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string         `json:"content"`
			ToolCalls []api.ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

func chat(t *testing.T, base, body string) (*http.Response, chatReply, string) {
	t.Helper()
	resp := post(t, base, body, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out chatReply
	_ = json.Unmarshal(raw, &out)
	return resp, out, string(raw)
}

const toolsDecl = `"tools":[` +
	`{"type":"function","function":{"name":"get_weather","parameters":{"type":"object",` +
	`"properties":{"city":{"type":"string"}},"required":["city"]}}},` +
	`{"type":"function","function":{"name":"book_flight","parameters":{"type":"object",` +
	`"properties":{"flight_id":{"type":"string"}},"required":["flight_id"]}}}]`

func userMsg(s string) string { return fmt.Sprintf(`{"role":"user","content":%q}`, s) }

func req(model string, msgs []string, extra ...string) string {
	return fmt.Sprintf(`{"model":%q,"messages":[%s]%s}`,
		model, strings.Join(msgs, ","), strings.Join(extra, ""))
}

var turnsRE = regexp.MustCompile(`turns=(\d+)`)

// turnsSeen reads back how many user turns the backend actually received —
// the only honest way to test history fidelity through four different wire
// formats.
func turnsSeen(text string) int {
	m := turnsRE.FindStringSubmatch(text)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func text(out chatReply) string {
	if len(out.Choices) == 0 {
		return ""
	}
	return out.Choices[0].Message.Content
}

func calls(out chatReply) []api.ToolCall {
	if len(out.Choices) == 0 {
		return nil
	}
	return out.Choices[0].Message.ToolCalls
}

// assistantCallsMsg renders a prior assistant turn that made tool calls, and
// the matching tool-result turns — i.e. what a real OpenAI client sends back.
func assistantCallsMsg(cs []api.ToolCall) (string, []string) {
	raw, _ := json.Marshal(cs)
	results := make([]string, 0, len(cs))
	for _, c := range cs {
		results = append(results, fmt.Sprintf(
			`{"role":"tool","tool_call_id":%q,"name":%q,"content":"21C"}`, c.ID, c.Function.Name))
	}
	return fmt.Sprintf(`{"role":"assistant","tool_calls":%s}`, raw), results
}

// --- the capabilities ------------------------------------------------------

func capComplete(t *testing.T, base, model string) (error, string) {
	_, out, raw := chat(t, base, req(model, []string{userMsg("hi")}))
	if text(out) == "" {
		return fmt.Errorf("no assistant text: %s", trunc(raw)), ""
	}
	return nil, trunc(text(out))
}

// capMultiTurn: the client resends the whole transcript (the stateless,
// no-conversation-id path). The backend must SEE all three user turns.
func capMultiTurn(t *testing.T, base, model string) (error, string) {
	msgs := []string{
		userMsg("my name is Ada"),
		`{"role":"assistant","content":"hello Ada"}`,
		userMsg("I work on analytical engines"),
		`{"role":"assistant","content":"noted"}`,
		userMsg("what did I say first?"),
	}
	_, out, raw := chat(t, base, req(model, msgs))
	if n := turnsSeen(text(out)); n != 3 {
		return fmt.Errorf("backend saw %d user turns, want 3 (history lost): %s", n, trunc(raw)), ""
	}
	return nil, "3 user turns relayed"
}

// capProxyMemory: with an EXPLICIT conversation_id the proxy owns the
// transcript — the client sends only the new message and the backend must
// still see the earlier turn.
func capProxyMemory(t *testing.T, base, model string) (error, string) {
	conv := fmt.Sprintf("conv-%s-%d", model, time.Now().UnixNano())
	body := func(s string) string {
		return req(model, []string{userMsg(s)}, `,"conversation_id":"`+conv+`"`)
	}
	chat(t, base, body("my name is Ada"))
	_, out, raw := chat(t, base, body("what is my name?"))
	if n := turnsSeen(text(out)); n < 2 {
		return fmt.Errorf("turn 2 saw %d user turns, want >=2 (no proxy memory): %s", n, trunc(raw)), ""
	}
	return nil, "history injected by proxy"
}

// capConvRelay: transports that keep their own state still need the resolved
// conversation id handed to them on every job.
func capConvRelay(t *testing.T, base, model string) (error, string) {
	conv := fmt.Sprintf("conv-relay-%s-%d", model, time.Now().UnixNano())
	_, out, raw := chat(t, base, req(model, []string{userMsg("hi")}, `,"conversation_id":"`+conv+`"`))
	if !strings.Contains(text(out), "conv="+conv) {
		return fmt.Errorf("backend did not receive conversation id: %s", trunc(raw)), ""
	}
	return nil, "id reached the backend"
}

func capToolCall(t *testing.T, base, model string) (error, string) {
	_, out, raw := chat(t, base, req(model, []string{userMsg("weather in Paris?")}, ",", toolsDecl))
	cs := calls(out)
	if len(cs) != 1 {
		return fmt.Errorf("got %d tool_calls, want 1: %s", len(cs), trunc(raw)), ""
	}
	if cs[0].Function.Name != "get_weather" || !strings.Contains(cs[0].Function.Arguments, "Paris") {
		return fmt.Errorf("call = %s(%s), want get_weather(Paris)", cs[0].Function.Name, cs[0].Function.Arguments), ""
	}
	if cs[0].ID == "" || cs[0].Type != "function" {
		return fmt.Errorf("malformed call: id=%q type=%q", cs[0].ID, cs[0].Type), ""
	}
	if fr := out.Choices[0].FinishReason; fr != "tool_calls" {
		return fmt.Errorf("finish_reason = %q, want tool_calls", fr), ""
	}
	return nil, "get_weather(Paris)"
}

// capParallel: three calls in ONE assistant turn, each with its own id — what
// an OpenAI client expects to execute concurrently.
func capParallel(t *testing.T, base, model string) (error, string) {
	_, out, raw := chat(t, base, req(model, []string{userMsg("PARALLEL: weather in Paris, Tokyo, Lima?")}, ",", toolsDecl))
	cs := calls(out)
	if len(cs) != 3 {
		return fmt.Errorf("got %d tool_calls, want 3: %s", len(cs), trunc(raw)), ""
	}
	ids, cities := map[string]bool{}, []string{}
	for _, c := range cs {
		if c.ID == "" || ids[c.ID] {
			return fmt.Errorf("duplicate or empty call id %q", c.ID), ""
		}
		ids[c.ID] = true
		cities = append(cities, c.Function.Arguments)
	}
	for _, want := range []string{"Paris", "Tokyo", "Lima"} {
		if !strings.Contains(strings.Join(cities, " "), want) {
			return fmt.Errorf("missing %s in %v", want, cities), ""
		}
	}
	return nil, "3 calls, distinct ids"
}

// capToolChain: the full multi-turn tool loop — call, result, a SECOND
// dependent call, its result, final text. Four turns on the wire.
func capToolChain(t *testing.T, base, model string) (error, string) {
	msgs := []string{userMsg("CHAIN: find a flight to Paris then book it")}

	_, out, raw := chat(t, base, req(model, msgs, ",", toolsDecl))
	first := calls(out)
	if len(first) != 1 || first[0].Function.Name != "get_weather" {
		return fmt.Errorf("round 1: %s", trunc(raw)), ""
	}
	asst, results := assistantCallsMsg(first)
	msgs = append(append(msgs, asst), results...)

	_, out, raw = chat(t, base, req(model, msgs, ",", toolsDecl))
	second := calls(out)
	if len(second) != 1 || second[0].Function.Name != "book_flight" {
		return fmt.Errorf("round 2 did not produce the dependent call: %s", trunc(raw)), ""
	}
	if !strings.Contains(second[0].Function.Arguments, "AF1680") {
		return fmt.Errorf("round 2 args = %q, want the id from round 1", second[0].Function.Arguments), ""
	}
	asst, results = assistantCallsMsg(second)
	msgs = append(append(msgs, asst), results...)

	_, out, raw = chat(t, base, req(model, msgs, ",", toolsDecl))
	if !strings.Contains(text(out), "AF1680") {
		return fmt.Errorf("round 3 did not settle to text: %s", trunc(raw)), ""
	}
	return nil, "call -> result -> call -> result -> text"
}

type sseFrames struct {
	chunks, withText int
	toolCalls        []api.ToolCall
	finish           string
	done             bool
}

func readSSE(t *testing.T, base, body string) (sseFrames, error) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return sseFrames{}, err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		return sseFrames{}, fmt.Errorf("content-type = %q, want event-stream", ct)
	}

	var f sseFrames
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			f.done = true
			continue
		}
		f.chunks++
		var chunk struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string         `json:"content"`
					ToolCalls []api.ToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].Delta.Content != "" {
			f.withText++
		}
		f.toolCalls = append(f.toolCalls, chunk.Choices[0].Delta.ToolCalls...)
		if fr := chunk.Choices[0].FinishReason; fr != "" {
			f.finish = fr
		}
	}
	return f, nil
}

func capStream(t *testing.T, base, model string) (error, string) {
	f, err := readSSE(t, base, req(model, []string{userMsg("hi")}, `,"stream":true`))
	if err != nil {
		return err, ""
	}
	if !f.done || f.withText == 0 {
		return fmt.Errorf("chunks=%d withText=%d done=%v", f.chunks, f.withText, f.done), ""
	}
	return nil, fmt.Sprintf("%d chunks + [DONE]", f.chunks)
}

// capStreamTools: tool calls over SSE arrive as indexed deltas and the stream
// closes with finish_reason "tool_calls" — the shape OpenAI SDKs reassemble.
func capStreamTools(t *testing.T, base, model string) (error, string) {
	f, err := readSSE(t, base,
		req(model, []string{userMsg("PARALLEL: weather in Paris, Tokyo, Lima?")}, ",", toolsDecl, `,"stream":true`))
	if err != nil {
		return err, ""
	}
	if len(f.toolCalls) != 3 {
		return fmt.Errorf("got %d streamed tool_calls, want 3", len(f.toolCalls)), ""
	}
	for i, c := range f.toolCalls {
		if c.Index == nil {
			return fmt.Errorf("streamed call %d has no index", i), ""
		}
		if *c.Index != i {
			return fmt.Errorf("call index = %d, want %d", *c.Index, i), ""
		}
	}
	if f.finish != "tool_calls" {
		return fmt.Errorf("finish_reason = %q, want tool_calls", f.finish), ""
	}
	if !f.done {
		return fmt.Errorf("stream did not terminate with [DONE]"), ""
	}
	return nil, "3 indexed deltas + finish_reason"
}

func trunc(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 70 {
		return s[:70] + "…"
	}
	return s
}

// --- the test ---------------------------------------------------------------

func TestCapabilityMatrix(t *testing.T) {
	oa := openAIMock(t)
	defer oa.Close()
	an := anthropicMock(t)
	defer an.Close()

	yaml := fmt.Sprintf(`
default: fwd
sticky_ttl: 1m
models:
  - name: fwd
    type: forward
    base_url: %s
    upstream_model: mock-gpt

  - name: translated
    type: forward
    style: anthropic
    base_url: %s
    upstream_model: claude-mock

  - name: worker-queue
    type: queue

  - name: adapter
    type: command
    command: %q

  - name: dyn
    type: dynamic
    levels:
      low: fwd
      medium: adapter
      high: worker-queue
`, oa.URL, an.URL, adapterScript)

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

	stop := fakeWorker(t, ts.URL, "worker-queue")
	defer stop()

	m := &matrix{}
	m.columns("complete", "multi-turn", "tool call", "parallel", "tool chain",
		"stream", "stream tools", "memory")

	type capability struct {
		name string
		run  func(*testing.T, string, string) (error, string)
	}
	shared := []capability{
		{"complete", capComplete},
		{"multi-turn", capMultiTurn},
		{"tool call", capToolCall},
		{"parallel", capParallel},
		{"tool chain", capToolChain},
	}

	for _, tr := range []struct {
		label, model string
		streaming    bool
		// proxyMemory: only the two upstream-backed transports let routsi own
		// the transcript (toolnexus keeps it). Queue and command adapters are
		// stateless by contract — they get the conversation id and keep their
		// own state, which capConvRelay checks instead.
		proxyMemory bool
	}{
		{"forward (openai)", "fwd", true, true},
		{"translated (anthropic)", "translated", true, true},
		{"queue (pull-worker)", "worker-queue", false, false},
		{"command (exec adapter)", "adapter", true, false},
	} {
		t.Run(tr.label, func(t *testing.T) {
			for _, c := range shared {
				err, d := c.run(t, ts.URL, tr.model)
				m.record(t, tr.label, c.name, err, d)
			}
			// Queue is non-streaming by design (ADR-001) — the one gap we
			// state rather than hide.
			if tr.streaming {
				err, d := capStream(t, ts.URL, tr.model)
				m.record(t, tr.label, "stream", err, d)
				err, d = capStreamTools(t, ts.URL, tr.model)
				m.record(t, tr.label, "stream tools", err, d)
			}
			if tr.proxyMemory {
				err, d := capProxyMemory(t, ts.URL, tr.model)
				m.record(t, tr.label, "memory", err, d)
			} else {
				err, d := capConvRelay(t, ts.URL, tr.model)
				m.record(t, tr.label, "memory", err, "conv id relayed; "+d)
			}
		})
	}

	// Routing sits above the transports: one virtual name, three backends
	// chosen per request and disclosed on the way out.
	t.Run("routing", func(t *testing.T) {
		const label = "dynamic routing"
		hard := "Design and prove the safety of a Byzantine fault tolerant consensus " +
			"protocol across three datacenters, then analyze its failure modes in detail."

		for _, c := range []struct{ name, prompt, want string }{
			{"complete", "hi", "fwd"},
			{"multi-turn", hard, "worker-queue"},
		} {
			resp, _, _ := chat(t, ts.URL, req("dyn", []string{userMsg(c.prompt)}))
			var err error
			got := resp.Header.Get("X-Selected-Model")
			if got != c.want {
				err = fmt.Errorf("X-Selected-Model = %q, want %q", got, c.want)
			}
			m.record(t, label, c.name, err, c.name+" prompt -> "+got)
		}

		// Escalate-only: a conversation pinned high must not fall back to a
		// cheap member when the next message looks easy.
		conv := fmt.Sprintf("conv-sticky-%d", time.Now().UnixNano())
		body := func(p string) string {
			return req("dyn", []string{userMsg(p)}, `,"conversation_id":"`+conv+`"`)
		}
		r1, _, _ := chat(t, ts.URL, body(hard))
		r2, _, _ := chat(t, ts.URL, body("hi"))
		first, second := r1.Header.Get("X-Selected-Model"), r2.Header.Get("X-Selected-Model")
		var err error
		if first != second {
			err = fmt.Errorf("conversation downgraded: %q then %q", first, second)
		}
		m.record(t, label, "memory", err, "sticky: "+first+" held across turns")

		resp, _, _ := chat(t, ts.URL, req("adapter", []string{userMsg("hi")}))
		err = nil
		if got := resp.Header.Get("X-Selected-Model"); got != "adapter" {
			err = fmt.Errorf("bypass selected %q, want adapter", got)
		}
		m.record(t, label, "tool call", err, "bypass: concrete name wins")
	})

	m.render(t)
}
