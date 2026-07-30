package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/api"
)

// A tool-calling conversation must survive the trip into toolnexus history:
// the assistant turn keeps its tool_calls and the tool result keeps its
// tool_call_id, otherwise the provider cannot match a result to its call.
func TestHistoryEntryPreservesToolFields(t *testing.T) {
	msg := func(role, content, toolCalls, toolCallID string) api.Message {
		m := api.Message{Role: role, ToolCallID: toolCallID}
		if content != "" {
			m.Content = json.RawMessage(`"` + content + `"`)
		}
		if toolCalls != "" {
			m.ToolCalls = json.RawMessage(toolCalls)
		}
		return m
	}
	calls := `[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Chennai\"}"}}]`

	t.Run("assistant tool_calls survive", func(t *testing.T) {
		e := historyEntry(msg("assistant", "", calls, ""))
		got, ok := e["tool_calls"].([]any)
		if !ok || len(got) != 1 {
			t.Fatalf("tool_calls lost: %#v", e)
		}
		fn := got[0].(map[string]any)["function"].(map[string]any)
		if fn["name"] != "get_weather" {
			t.Fatalf("call name = %v", fn["name"])
		}
		if fn["arguments"] != `{"city":"Chennai"}` {
			t.Fatalf("arguments not byte-preserved: %v", fn["arguments"])
		}
	})

	t.Run("tool result keeps tool_call_id", func(t *testing.T) {
		e := historyEntry(msg("tool", `{\"temp_c\":31}`, "", "call_1"))
		if e["tool_call_id"] != "call_1" {
			t.Fatalf("tool_call_id lost: %#v", e)
		}
		if e["role"] != "tool" {
			t.Fatalf("role = %v", e["role"])
		}
	})

	t.Run("plain message renders exactly as before", func(t *testing.T) {
		e := historyEntry(msg("user", "hello", "", ""))
		if len(e) != 2 || e["role"] != "user" || e["content"] != "hello" {
			t.Fatalf("plain message changed shape: %#v", e)
		}
	})
}

// split must carry the tool fields through for every history message, and
// still return the last user message as the prompt.
func TestSplitCarriesToolTurns(t *testing.T) {
	req := &api.ChatRequest{Messages: []api.Message{
		{Role: "user", Content: json.RawMessage(`"weather in Chennai?"`)},
		{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}]`)},
		{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"31C"`)},
		{Role: "user", Content: json.RawMessage(`"and Tokyo?"`)},
	}}
	prompt, history := split(req)
	if prompt != "and Tokyo?" {
		t.Fatalf("prompt = %q", prompt)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	asst := history[1].(map[string]any)
	if _, ok := asst["tool_calls"]; !ok {
		t.Fatalf("assistant tool_calls dropped from history: %#v", asst)
	}
	if got := history[2].(map[string]any)["tool_call_id"]; got != "call_1" {
		t.Fatalf("tool_call_id dropped from history: %v", got)
	}
}

// ErrToolsUnsupported stays part of the backend contract for any backend that
// genuinely cannot honour tools — the translator no longer needs it (ADR-010
// relays through single-turn Translate), but silently dropping tools must never
// become acceptable again, so the sentinel and its 400 mapping remain.
func TestToolsUnsupportedSentinelIsWrappable(t *testing.T) {
	err := fmt.Errorf("%w: model %q is text-only", ErrToolsUnsupported, "some-model")
	if !errors.Is(err, ErrToolsUnsupported) {
		t.Fatal("sentinel must survive %w wrapping so the server can map it to 400")
	}
	if !strings.Contains(err.Error(), "some-model") {
		t.Fatal("wrapped message should keep the caller context")
	}
}
