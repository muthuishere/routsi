package backend

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/muthuishere/routsi/internal/api"
)

// Tool-call emulation for one-shot CLI agents (ADR-011 Phase A, proven in
// spike 002 across claude/codex/copilot/devin): the client's tool schemas are
// rendered into the prompt with a fenced-JSON answer protocol; the agent's
// reply is parsed back into text or tool calls. On any parse failure the raw
// text is returned as a plain answer — never a fabricated call.

// buildToolPrompt wraps a base prompt with the tool manifest + protocol.
func buildToolPrompt(base string, tools json.RawMessage) string {
	var b strings.Builder
	b.WriteString("You are answering an API request relayed by a proxy. The CALLER can execute these functions for you — you cannot run them yourself, and you must NOT use any tools of your own to do the task:\n\n")
	b.Write(tools)
	b.WriteString("\n\nReply with ONLY a fenced json block matching " +
		`{"kind":"answer"|"tool_calls","answer":string,"tool_calls":[{"name":string,"arguments":object}]}` +
		" — no prose outside the fence. Emit kind=tool_calls with ALL independently runnable calls in one array when the request needs actions; NEVER guess values that must come from another call's result (make the prerequisite call first). Otherwise kind=answer.\n\n")
	b.WriteString(base)
	return b.String()
}

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// parseToolReply maps an agent's reply onto a structured Result.
func parseToolReply(text string) api.Result {
	candidate := ""
	if m := fencedJSON.FindStringSubmatch(text); m != nil {
		candidate = m[1]
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		candidate = t
	}
	if candidate == "" {
		return api.Result{Content: strings.TrimSpace(text)}
	}
	var reply struct {
		Kind      string `json:"kind"`
		Answer    string `json:"answer"`
		ToolCalls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(candidate), &reply); err != nil {
		return api.Result{Content: strings.TrimSpace(text)}
	}
	if reply.Kind == "tool_calls" && len(reply.ToolCalls) > 0 {
		res := api.Result{}
		for i, c := range reply.ToolCalls {
			if c.Name == "" {
				continue
			}
			args := string(c.Arguments)
			var asStr string
			if json.Unmarshal(c.Arguments, &asStr) == nil {
				args = asStr // agent already sent a JSON-encoded string
			}
			if args == "" {
				args = "{}"
			}
			res.ToolCalls = append(res.ToolCalls, api.ToolCall{
				ID:       fmt.Sprintf("call_%s_%d", randHex(), i),
				Type:     "function",
				Function: api.ToolFunction{Name: c.Name, Arguments: args},
			})
		}
		if len(res.ToolCalls) > 0 {
			return res
		}
	}
	if reply.Kind == "answer" {
		return api.Result{Content: reply.Answer}
	}
	return api.Result{Content: strings.TrimSpace(text)}
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
