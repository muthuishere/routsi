// Package backend holds everything that can answer a chat request: raw
// OpenAI-style HTTP forwards (byte passthrough), custom Go handlers, and
// toolnexus-translated upstreams (Anthropic/Gemini styles behind the OpenAI
// face).
package backend

import (
	"context"
	"fmt"

	"github.com/muthuishere/routsi/internal/api"
)

// Backend produces answer content; the server wraps it in the OpenAI envelope
// (chat.completion, or chat.completion.chunk SSE frames when streaming).
// Implementations that cannot truly stream may just Complete — the server
// fakes streaming by chunking the final answer.
type Backend interface {
	// Complete returns the full answer text.
	Complete(ctx context.Context, req *api.ChatRequest) (string, error)
	// Stream pushes answer deltas through emit as they are produced.
	Stream(ctx context.Context, req *api.ChatRequest, emit func(delta string)) error
}

// Registry maps handler names (models.yaml `handler:`) to custom Backends.
type Registry struct {
	handlers map[string]Backend
}

func NewRegistry() *Registry { return &Registry{handlers: map[string]Backend{}} }

func (r *Registry) Register(name string, b Backend) {
	r.handlers[name] = b
}

func (r *Registry) Get(name string) (Backend, error) {
	b, ok := r.handlers[name]
	if !ok {
		return nil, fmt.Errorf("no custom handler registered as %q", name)
	}
	return b, nil
}

// Completer adapts a plain function into a non-streaming Backend.
type Completer func(ctx context.Context, req *api.ChatRequest) (string, error)

func (f Completer) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	return f(ctx, req)
}

func (f Completer) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := f(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}
