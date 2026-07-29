package backend

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolReply(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantCalls int
		wantName  string
		wantArgs  string
		wantText  string
	}{
		{
			name:      "fenced tool_calls, object arguments",
			in:        "```json\n{\"kind\":\"tool_calls\",\"tool_calls\":[{\"name\":\"write\",\"arguments\":{\"filePath\":\"a.txt\",\"content\":\"hi\"}}]}\n```",
			wantCalls: 1, wantName: "write", wantArgs: `{"filePath":"a.txt","content":"hi"}`,
		},
		{
			name:      "bare JSON, string arguments (codex strict style)",
			in:        `{"kind":"tool_calls","answer":"","tool_calls":[{"name":"bash","arguments":"{\"command\":\"ls\"}"}]}`,
			wantCalls: 1, wantName: "bash", wantArgs: `{"command":"ls"}`,
		},
		{
			name:      "parallel calls survive",
			in:        "```json\n{\"kind\":\"tool_calls\",\"tool_calls\":[{\"name\":\"a\",\"arguments\":{}},{\"name\":\"b\",\"arguments\":{}}]}\n```",
			wantCalls: 2, wantName: "a", wantArgs: "{}",
		},
		{
			name:     "fenced answer",
			in:       "```json\n{\"kind\":\"answer\",\"answer\":\"plain reply\"}\n```",
			wantText: "plain reply",
		},
		{
			name:     "prose fallback — no fabricated call",
			in:       "I could not find a fence so here is text.",
			wantText: "I could not find a fence so here is text.",
		},
		{
			name:     "malformed JSON falls back to raw text",
			in:       "```json\n{\"kind\":\"tool_calls\",\n```",
			wantText: "```json\n{\"kind\":\"tool_calls\",\n```",
		},
		{
			name:     "tool_calls kind with empty list falls back to text",
			in:       `{"kind":"tool_calls","tool_calls":[]}`,
			wantText: `{"kind":"tool_calls","tool_calls":[]}`,
		},
		{
			name:      "fence with trailing terminal noise (copilot footer)",
			in:        "```json\n{\"kind\":\"tool_calls\",\"tool_calls\":[{\"name\":\"write\",\"arguments\":{}}]}\n```\n\nAI Credits 0.59 (25s)",
			wantCalls: 1, wantName: "write", wantArgs: "{}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parseToolReply(tc.in)
			if len(res.ToolCalls) != tc.wantCalls {
				t.Fatalf("calls = %d, want %d (%+v)", len(res.ToolCalls), tc.wantCalls, res)
			}
			if tc.wantCalls > 0 {
				c := res.ToolCalls[0]
				if c.Function.Name != tc.wantName || c.Function.Arguments != tc.wantArgs {
					t.Fatalf("call = %s(%s), want %s(%s)", c.Function.Name, c.Function.Arguments, tc.wantName, tc.wantArgs)
				}
				if c.ID == "" || c.Type != "function" {
					t.Fatalf("call missing id/type: %+v", c)
				}
			} else if res.Content != tc.wantText {
				t.Fatalf("content = %q, want %q", res.Content, tc.wantText)
			}
		})
	}
}

func TestBuildToolPrompt(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","function":{"name":"get_weather"}}]`)
	p := buildToolPrompt("What's the weather?", tools)
	for _, want := range []string{"get_weather", "fenced json block", "What's the weather?", "cannot run them yourself"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}
