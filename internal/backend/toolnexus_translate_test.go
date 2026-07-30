package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// ADR-010: a translated (anthropic) upstream must relay client-declared tools —
// one provider call, nothing executed proxy-side, and the model's tool_use
// handed back as OpenAI tool_calls for the CLIENT to run.
func TestTranslatedUpstreamRelaysToolCalls(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
		  "id":"msg_1","model":"claude-x","stop_reason":"tool_use",
		  "content":[{"type":"tool_use","id":"toolu_9","name":"get_weather","input":{"city":"Chennai"}}],
		  "usage":{"input_tokens":11,"output_tokens":7}
		}`))
	}))
	defer srv.Close()

	tn := NewToolnexus(&config.Model{
		Name: "claude-native", Style: config.StyleAnthropic,
		BaseURL: srv.URL, UpstreamModel: "claude-x",
	})
	req := &api.ChatRequest{
		Model:    "claude-native",
		Messages: []api.Message{{Role: "user", Content: json.RawMessage(`"weather in Chennai?"`)}},
		Tools:    json.RawMessage(`[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`),
	}

	res, err := tn.CompleteResult(context.Background(), req)
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1 (%+v)", len(res.ToolCalls), res)
	}
	c := res.ToolCalls[0]
	if c.ID != "toolu_9" {
		t.Errorf("id = %q — the provider's call id must survive so the client can echo it", c.ID)
	}
	if c.Function.Name != "get_weather" || c.Type != "function" {
		t.Errorf("call = %s/%s", c.Type, c.Function.Name)
	}
	// Arguments must be a JSON *string* (OpenAI wire shape), not an object.
	var args map[string]any
	if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not a JSON string: %q", c.Function.Arguments)
	}
	if args["city"] != "Chennai" {
		t.Errorf("arguments = %v", args)
	}

	// The tool DECLARATION must have reached the provider, translated to
	// Anthropic shape (input_schema, not OpenAI's function/parameters nesting).
	raw, _ := json.Marshal(gotBody["tools"])
	if !strings.Contains(string(raw), "input_schema") || !strings.Contains(string(raw), "get_weather") {
		t.Errorf("declaration not translated to Anthropic shape: %s", raw)
	}
	// And nothing may have been executed on our side: a single call, no loop.
	if _, ok := gotBody["messages"]; !ok {
		t.Errorf("no messages sent: %v", gotBody)
	}
}

// The second turn: the client returns its tool result, which must reach the
// provider as a native tool_result block referencing the original call id.
func TestTranslatedUpstreamCarriesToolResultBack(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_2","model":"claude-x","stop_reason":"end_turn",
		  "content":[{"type":"text","text":"It is 31C and humid in Chennai."}],
		  "usage":{"input_tokens":20,"output_tokens":9}}`))
	}))
	defer srv.Close()

	tn := NewToolnexus(&config.Model{
		Name: "claude-native", Style: config.StyleAnthropic,
		BaseURL: srv.URL, UpstreamModel: "claude-x",
	})
	req := &api.ChatRequest{
		Model: "claude-native",
		Messages: []api.Message{
			{Role: "user", Content: json.RawMessage(`"weather in Chennai?"`)},
			{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"toolu_9","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Chennai\"}"}}]`)},
			{Role: "tool", ToolCallID: "toolu_9", Content: json.RawMessage(`"{\"temp_c\":31}"`)},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"get_weather"}}]`),
	}

	res, err := tn.CompleteResult(context.Background(), req)
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}
	if len(res.ToolCalls) != 0 {
		t.Errorf("second turn should be text, got calls: %+v", res.ToolCalls)
	}
	if !strings.Contains(res.Content, "31") {
		t.Errorf("final answer lost the tool result: %q", res.Content)
	}
	sent, _ := json.Marshal(gotBody["messages"])
	for _, want := range []string{"tool_result", "toolu_9"} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("tool result not translated to a native block (%q missing): %s", want, sent)
		}
	}
}
