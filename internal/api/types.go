// Package api holds the minimal OpenAI chat-completions wire types the proxy
// needs. The proxy never re-models the full protocol: forwards relay raw
// bytes, and only these fields are parsed for routing and enveloping.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Message is one chat message. Content stays raw because OpenAI allows both a
// string and a content-part array.
type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// ToolCall is one function call in an assistant reply (OpenAI wire shape:
// Arguments is a JSON-encoded string, not an object).
type ToolCall struct {
	Index    *int         `json:"index,omitempty"` // set on streaming deltas only
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Result is a structured backend answer: plain text, tool calls, or both.
type Result struct {
	Content   string
	ToolCalls []ToolCall
}

// Text returns the message content as plain text, best-effort: a string as-is,
// a content-part array concatenated over its text parts.
func (m Message) Text() string {
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		out := ""
		for _, p := range parts {
			out += p.Text
		}
		return out
	}
	return ""
}

// ChatRequest is the routed view of a /v1/chat/completions body. Raw keeps the
// untouched original so forwards preserve every field we did not model.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	ConversationID string          `json:"conversation_id"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`

	Raw []byte `json:"-"`
}

// LastUserText returns the text of the last user message, or "".
func (r *ChatRequest) LastUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].Text()
		}
	}
	return ""
}

// ChatResponse is a minimal non-streaming chat.completion envelope.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Usage mirrors OpenAI's token accounting. Backends that don't surface real
// counts get estimates (~4 chars/token) — flagged nowhere in the schema, as
// OpenAI clients have no slot for "estimated", but close enough for cost
// dashboards.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// EstimateUsage builds a Usage from request text + answer text.
func EstimateUsage(req *ChatRequest, answer string) *Usage {
	in := 0
	for _, m := range req.Messages {
		in += len(m.Text())
	}
	u := &Usage{PromptTokens: (in + 3) / 4, CompletionTokens: (len(answer) + 3) / 4}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

type Choice struct {
	Index        int             `json:"index"`
	Message      *RespMessage    `json:"message,omitempty"`
	Delta        *RespMessage    `json:"delta,omitempty"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type RespMessage struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// NewCompletion wraps text as a chat.completion answered by model.
func NewCompletion(model, text string) ChatResponse {
	stop := "stop"
	return ChatResponse{
		ID:      "chatcmpl-" + randID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []Choice{{Message: &RespMessage{Role: "assistant", Content: text}, FinishReason: &stop}},
	}
}

// NewResultCompletion wraps a structured Result as a chat.completion; tool
// calls flip finish_reason to "tool_calls" per the OpenAI contract.
func NewResultCompletion(model string, res Result) ChatResponse {
	c := NewCompletion(model, res.Content)
	if len(res.ToolCalls) > 0 {
		fr := "tool_calls"
		c.Choices[0].Message.ToolCalls = res.ToolCalls
		c.Choices[0].FinishReason = &fr
	}
	return c
}

// NewToolChunk builds a chat.completion.chunk whose delta carries tool calls
// (indexed, as OpenAI streams them), followed by a finish chunk from the
// caller.
func NewToolChunk(id, model string, calls []ToolCall) ChatResponse {
	c := ChatResponse{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model}
	indexed := make([]ToolCall, len(calls))
	for i := range calls {
		idx := i
		indexed[i] = calls[i]
		indexed[i].Index = &idx
	}
	c.Choices = []Choice{{Delta: &RespMessage{ToolCalls: indexed}}}
	return c
}

// FinishChunk builds the final chunk with an explicit finish_reason.
func FinishChunk(id, model, reason string) ChatResponse {
	c := ChatResponse{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model}
	c.Choices = []Choice{{Delta: &RespMessage{}, FinishReason: &reason}}
	return c
}

// NewChunk builds one chat.completion.chunk carrying a content delta; pass
// done=true for the final finish_reason chunk (empty delta).
func NewChunk(id, model, delta string, done bool) ChatResponse {
	c := ChatResponse{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model}
	if done {
		stop := "stop"
		c.Choices = []Choice{{Delta: &RespMessage{}, FinishReason: &stop}}
	} else {
		c.Choices = []Choice{{Delta: &RespMessage{Content: delta}}}
	}
	return c
}

// ChunkID mints an id shared by all chunks of one streamed completion.
func ChunkID() string { return "chatcmpl-" + randID() }

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
