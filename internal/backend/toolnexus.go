package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	toolnexus "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// Toolnexus answers via a non-OpenAI-style upstream (Anthropic/Gemini) using
// the toolnexus unified client for wire-format translation. Tools are
// disabled — this is a translator, not an agent; a proxy must never expose
// shell/file builtins. The OpenAI client resends full history each request,
// so we pass req.Messages as history verbatim instead of toolnexus's own
// conversation store.
type Toolnexus struct {
	model *config.Model

	once sync.Once
	c    *toolnexus.Client
	tk   *toolnexus.Toolkit
	err  error
}

func NewToolnexus(m *config.Model) *Toolnexus { return &Toolnexus{model: m} }

func (t *Toolnexus) init(ctx context.Context) error {
	t.once.Do(func() {
		t.tk, t.err = toolnexus.CreateToolkit(ctx, toolnexus.Options{Builtins: false})
		if t.err != nil {
			return
		}
		key := os.Getenv(t.model.APIKeyEnv)
		if key == "" {
			key = "unused" // keyless upstream; toolnexus requires a value
		}
		t.c = toolnexus.CreateClient(toolnexus.ClientOptions{
			BaseURL: t.model.BaseURL,
			Style:   toolnexus.ClientStyle(t.model.Style),
			Model:   t.model.UpstreamModel,
			APIKey:  key,
		})
	})
	return t.err
}

// CompleteResult implements backend.ResultBackend. When the request carries
// `tools` it relays them through toolnexus's single-turn translation (ADR-010):
// exactly ONE provider call, no tool execution, no agent loop, and the model's
// tool calls come back in OpenAI shape for the CLIENT to execute. That is the
// whole pass-through contract — each HTTP turn is self-contained because the
// client resends its history (including prior tool results) every time, so this
// path stays stateless.
func (t *Toolnexus) CompleteResult(ctx context.Context, req *api.ChatRequest) (api.Result, error) {
	if len(req.Tools) == 0 {
		text, err := t.Complete(ctx, req)
		return api.Result{Content: text}, err
	}
	if err := t.init(ctx); err != nil {
		return api.Result{}, fmt.Errorf("toolnexus %s: %w", t.model.Name, err)
	}
	var tools []any
	if err := json.Unmarshal(req.Tools, &tools); err != nil {
		return api.Result{}, fmt.Errorf("toolnexus %s: tools: %w", t.model.Name, err)
	}
	messages := make([]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, historyEntry(m))
	}
	tr := toolnexus.TranslateRequest{Messages: messages, Tools: tools}
	if len(req.ToolChoice) > 0 {
		var tc any
		if json.Unmarshal(req.ToolChoice, &tc) == nil {
			tr.ToolChoice = tc
		}
	}
	res, err := t.c.Translate(ctx, tr)
	if err != nil {
		return api.Result{}, fmt.Errorf("toolnexus %s: %w", t.model.Name, err)
	}
	out := api.Result{Content: res.Text}
	for _, c := range res.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, api.ToolCall{
			ID:       c.ID,
			Type:     "function",
			Function: api.ToolFunction{Name: c.Name, Arguments: c.Arguments},
		})
	}
	return out, nil
}

func (t *Toolnexus) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	if len(req.Tools) > 0 {
		// Streaming/plain-Backend path: a tool-calling turn has no text to
		// stream, so resolve it through the structured path and hand back the
		// text (the server prefers CompleteResult and gets the calls).
		res, err := t.CompleteResult(ctx, req)
		return res.Content, err
	}
	if err := t.init(ctx); err != nil {
		return "", fmt.Errorf("toolnexus %s: %w", t.model.Name, err)
	}
	// An explicit conversation id means the PROXY owns the memory: the client
	// sends only the new message and toolnexus's conversation store carries
	// the transcript (Ask semantics). Fingerprint ids ("fp-") exist only for
	// routing stickiness — the client is resending full history there.
	if id := req.ConversationID; id != "" && !strings.HasPrefix(id, "fp-") {
		res, err := t.c.Ask(ctx, req.LastUserText(), t.tk, id)
		if err != nil {
			return "", fmt.Errorf("toolnexus %s: %w", t.model.Name, err)
		}
		return res.Text, nil
	}
	prompt, history := split(req)
	res, err := t.c.RunWithHistory(ctx, prompt, t.tk, history)
	if err != nil {
		return "", fmt.Errorf("toolnexus %s: %w", t.model.Name, err)
	}
	return res.Text, nil
}

// Stream fakes streaming off Complete for v1: toolnexus's true streaming path
// (StreamWithID) has no history variant yet, and history fidelity beats
// early first-token here.
func (t *Toolnexus) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := t.Complete(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}

// split turns OpenAI messages into (last user prompt, prior history) in the
// role/content map shape RunWithHistory appends to verbatim.
// historyEntry renders one message for toolnexus's history. It preserves the
// tool-calling fields the OpenAI wire carries: without them a `tool` result
// loses its tool_call_id and an assistant turn loses its tool_calls entirely,
// so a provider can never match a result to its call and multi-turn tool use
// cannot work (ADR-010). Strictly additive — a message with no tool fields
// renders exactly as before.
//
// This carries the fields through in OpenAI shape, which is correct for
// `style: openai` upstreams. Translating them into provider-native tool_use /
// tool_result blocks for Anthropic/Gemini is a gap in the toolnexus library
// (its adapters are declaration-only); see ADR-010.
func historyEntry(m api.Message) map[string]any {
	e := map[string]any{"role": m.Role, "content": m.Text()}
	if len(m.ToolCalls) > 0 {
		var calls any
		if json.Unmarshal(m.ToolCalls, &calls) == nil {
			e["tool_calls"] = calls
		}
	}
	if m.ToolCallID != "" {
		e["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		e["name"] = m.Name
	}
	return e
}

func split(req *api.ChatRequest) (string, []any) {
	last := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			last = i
			break
		}
	}
	var history []any
	for i, m := range req.Messages {
		if i == last {
			continue
		}
		history = append(history, historyEntry(m))
	}
	if last < 0 {
		return "", history
	}
	return req.Messages[last].Text(), history
}
