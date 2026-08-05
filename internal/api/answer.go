package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rawAnswer is the adapter/worker answer wire shape (ADR-013). Tool calls are
// accepted in either the OpenAI form ({"function":{"name","arguments":"<json
// string>"}}) or the simplified form ({"name":..., "arguments":{...}}) — an
// adapter author should not have to know that OpenAI encodes arguments as a
// string inside JSON.
type rawAnswer struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_calls"`
}

// ParseAnswer normalizes an adapter or worker answer into a Result. idPrefix
// seeds synthesized tool-call ids for adapters that don't supply their own.
//
// Returns ok=false when data is not a JSON object — the caller decides what
// that means: an exec adapter treats it as plain answer text (so `echo hi` is a
// valid adapter), while an HTTP worker endpoint rejects it as a bad body.
func ParseAnswer(data []byte, idPrefix string) (Result, bool) {
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		return Result{}, false
	}
	var raw rawAnswer
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return Result{}, false
	}
	return raw.result(idPrefix), true
}

// DecodeAnswer is the strict form used by the worker HTTP endpoint, where a
// non-object body is a client error rather than plain text.
func DecodeAnswer(data []byte, idPrefix string) (Result, error) {
	var raw rawAnswer
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, err
	}
	return raw.result(idPrefix), nil
}

func (raw rawAnswer) result(idPrefix string) Result {
	res := Result{Content: raw.Content}
	for i, tc := range raw.ToolCalls {
		call := ToolCall{ID: tc.ID, Type: tc.Type}
		if call.Type == "" {
			call.Type = "function"
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%s_%d", idPrefix, i)
		}
		if tc.Function != nil {
			call.Function = ToolFunction{Name: tc.Function.Name, Arguments: tc.Function.Arguments}
		} else {
			args := string(tc.Arguments)
			var asStr string
			if json.Unmarshal(tc.Arguments, &asStr) == nil {
				args = asStr // already a JSON-encoded string
			}
			if args == "" {
				args = "{}"
			}
			call.Function = ToolFunction{Name: tc.Name, Arguments: args}
		}
		res.ToolCalls = append(res.ToolCalls, call)
	}
	return res
}
