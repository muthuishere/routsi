// Package server wires the OpenAI-compatible surface: parse the request head,
// resolve the target model (bypass > sticky > router), dispatch to a raw
// forward or an enveloped backend, and always disclose the chosen model via
// the X-Selected-Model header (visible attribution + explicit bypass are the
// two escape hatches practitioners demand — practitioner-signals.md).
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
	"github.com/muthuishere/routsi/internal/metrics"
	"github.com/muthuishere/routsi/internal/router"
	"github.com/muthuishere/routsi/internal/sticky"
)

// RouterModel is the virtual model name that activates routing; any other
// name is a hard bypass to exactly that configured model.
const RouterModel = "auto"

type target struct {
	model   *config.Model
	forward *backend.Forward // set when model is an openai-style forward
	backend backend.Backend  // set otherwise
}

type Server struct {
	cfg      *config.Config
	registry *backend.Registry
	router   router.Router
	sticky   *sticky.Store
	targets  map[string]*target
	metrics  *metrics.Collector
}

func New(cfg *config.Config, reg *backend.Registry, rt router.Router) (*Server, error) {
	if reg == nil {
		reg = backend.NewRegistry()
	}
	if rt == nil {
		rt = router.NewRules()
	}
	s := &Server{
		cfg:      cfg,
		registry: reg,
		router:   rt,
		sticky:   sticky.New(cfg.StickyTTL),
		targets:  map[string]*target{},
		metrics:  metrics.New(),
	}
	for i := range cfg.Models {
		m := &cfg.Models[i]
		if m.Type == config.TypeDynamic {
			continue // virtual: resolved per request to a member target
		}
		t := &target{model: m}
		switch {
		case m.Type == config.TypeForward && m.Style == config.StyleOpenAI:
			t.forward = backend.NewForward(m)
			// Companion for proxy-managed conversations (explicit id).
			t.backend = backend.NewToolnexus(m)
		case m.Type == config.TypeForward:
			t.backend = backend.NewToolnexus(m)
		case m.Type == config.TypeDevin:
			t.backend = backend.NewDevin(m)
		case m.Type == config.TypeCodex || m.Type == config.TypeCopilot || m.Type == config.TypeClaude:
			t.backend = backend.NewCLIAgent(m)
		default:
			b, err := reg.Get(m.Handler)
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", m.Name, err)
			}
			t.backend = b
		}
		s.targets[m.Name] = t
	}
	// Reference checks live here, after variants/discovery exist.
	if s.targets[cfg.Default] == nil {
		return nil, fmt.Errorf("default model %q is not a routable model", cfg.Default)
	}
	for tier, name := range cfg.Tiers {
		if s.targets[name] == nil {
			return nil, fmt.Errorf("tier %q points at unknown model %q", tier, name)
		}
	}
	// Dynamic membership is checked here, after variants/discovery exist.
	for i := range cfg.Models {
		m := &cfg.Models[i]
		if m.Type != config.TypeDynamic {
			continue
		}
		for level, member := range m.Levels {
			if s.targets[member] == nil {
				return nil, fmt.Errorf("dynamic model %q: level %s points at unknown or nested model %q", m.Name, level, member)
			}
		}
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, s.metrics.Prometheus())
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.metrics.Snapshot())
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(dashboardHTML)
	})
	return mux
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 20<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: %v", err)
		return
	}
	var req api.ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return
	}
	req.Raw = raw

	convID := conversationID(r, &req)
	req.ConversationID = convID // backends see the resolved id, however it arrived
	t, dec := s.resolve(&req, convID)
	if t == nil {
		httpError(w, http.StatusNotFound, "unknown model %q (declared models or %q)", req.Model, RouterModel)
		return
	}
	w.Header().Set("X-Selected-Model", t.model.Name)

	start := time.Now()
	ev := metrics.Event{Model: t.model.Name, Provider: t.model.Provider, Routed: dec.routed, Escalated: dec.escalated}
	// Raw passthrough when the client owns the history. An explicit
	// conversation id flips a forward into proxy-managed memory: the client
	// sends only the new message; toolnexus's store carries the transcript.
	if t.forward != nil && !explicitConversation(convID) {
		status, err := t.forward.Relay(w, r, &req)
		ev.LatencyMs, ev.Err = time.Since(start).Milliseconds(), err != nil
		s.metrics.Record(ev) // token counts unknown on raw passthrough
		logReq(req.Model, t.model.Name, convID, status, start, err)
		return
	}
	usage, err := s.envelope(w, r, t, &req)
	ev.LatencyMs = time.Since(start).Milliseconds()
	if usage != nil {
		ev.PromptTokens, ev.CompletionTokens = usage.PromptTokens, usage.CompletionTokens
	}
	ev.Err = err != nil
	s.metrics.Record(ev)
	status := http.StatusOK
	if err != nil {
		status = 0
	}
	logReq(req.Model, t.model.Name, convID, status, start, err)
}

// resolve picks the target. Concrete model name = bypass, no routing. "auto"
// routes over the global tiers map; a dynamic model routes over its own
// levels map. Both share the group semantics: classify the task, pin per
// conversation, escalate-only (never downgrade mid-conversation).
// decision records how a target was chosen, for metrics.
type decision struct {
	routed    bool // auto/dynamic chose it (vs a bypass to a concrete name)
	escalated bool // this turn escalated a pinned conversation upward
}

func (s *Server) resolve(req *api.ChatRequest, convID string) (*target, decision) {
	if req.Model == RouterModel {
		return s.resolveGroup("auto", s.cfg.Tiers, req, convID)
	}
	if m := s.cfg.Model(req.Model); m != nil && m.Type == config.TypeDynamic {
		return s.resolveGroup(m.Name, m.Levels, req, convID)
	}
	return s.targets[req.Model], decision{}
}

func (s *Server) resolveGroup(group string, levels map[string]string, req *api.ChatRequest, convID string) (*target, decision) {
	level := s.router.Pick(req)
	picked := ""
	for _, l := range router.Fallback(level) {
		if name := levels[l]; name != "" && s.targets[name] != nil {
			picked = name
			break
		}
	}
	if picked == "" {
		picked = s.cfg.Default
	}
	// Pins are scoped per group so `auto` and each dynamic model keep
	// independent conversations.
	key := ""
	if convID != "" {
		key = convID + "#" + group
	}
	pinned, ok := s.sticky.Get(key)
	if ok && s.targets[pinned] != nil {
		if router.Rank(level) > router.Rank(levelOf(levels, pinned)) {
			s.sticky.Pin(key, picked) // escalate and re-pin
			return s.targets[picked], decision{routed: true, escalated: true}
		}
		return s.targets[pinned], decision{routed: true}
	}
	s.sticky.Pin(key, picked)
	return s.targets[picked], decision{routed: true}
}

// levelOf finds the highest level a member is mapped to within a group.
func levelOf(levels map[string]string, member string) string {
	best, bestRank := "", -1
	for l, name := range levels {
		if name == member && router.Rank(l) > bestRank {
			best, bestRank = l, router.Rank(l)
		}
	}
	return best
}

// envelope dispatches to a Backend and wraps the answer as OpenAI JSON/SSE.
// It returns the usage it reported so the caller can record metrics.
func (s *Server) envelope(w http.ResponseWriter, r *http.Request, t *target, req *api.ChatRequest) (*api.Usage, error) {
	if !req.Stream {
		text, err := t.backend.Complete(r.Context(), req)
		if err != nil {
			httpError(w, http.StatusBadGateway, "backend %s: %v", t.model.Name, err)
			return nil, err
		}
		w.Header().Set("Content-Type", "application/json")
		resp := api.NewCompletion(t.model.Name, text)
		resp.Usage = api.EstimateUsage(req, text)
		return resp.Usage, json.NewEncoder(w).Encode(resp)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	id := api.ChunkID()
	writeChunk := func(c api.ChatResponse) {
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Role-opening chunk, as OpenAI sends.
	open := api.NewChunk(id, t.model.Name, "", false)
	open.Choices[0].Delta.Role = "assistant"
	writeChunk(open)

	var streamed strings.Builder
	err := t.backend.Stream(r.Context(), req, func(delta string) {
		if delta != "" {
			streamed.WriteString(delta)
			writeChunk(api.NewChunk(id, t.model.Name, delta, false))
		}
	})
	if err != nil {
		// Headers are gone; surface the error inside the stream.
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q}}\n\n", err.Error())
		if flusher != nil {
			flusher.Flush()
		}
		return nil, err
	}
	final := api.NewChunk(id, t.model.Name, "", true)
	final.Usage = api.EstimateUsage(req, streamed.String())
	writeChunk(final)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return final.Usage, nil
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	type m struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	list := []m{{ID: RouterModel, Object: "model", OwnedBy: "routsi"}}
	for _, mm := range s.cfg.Models {
		list = append(list, m{ID: mm.Name, Object: "model", OwnedBy: mm.Provider})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
}

// explicitConversation reports whether convID was supplied by the caller
// (header/body) rather than derived by fingerprinting. Only explicit ids opt
// into proxy-managed memory; fingerprints ("fp-") exist for stickiness only.
func explicitConversation(convID string) bool {
	return convID != "" && !strings.HasPrefix(convID, "fp-")
}

// conversationID: explicit header, then body field, then a hash of the first
// user message (OpenRouter's prefix-fingerprint recipe) so stickiness works
// with unmodified SDKs.
func conversationID(r *http.Request, req *api.ChatRequest) string {
	if id := r.Header.Get("X-Conversation-Id"); id != "" {
		return id
	}
	if req.ConversationID != "" {
		return req.ConversationID
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			sum := sha256.Sum256([]byte(m.Text()))
			return "fp-" + hex.EncodeToString(sum[:8])
		}
	}
	return ""
}

func httpError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	msg := fmt.Sprintf(format, args...)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "proxy_error"}})
}

func logReq(asked, chose, convID string, status int, start time.Time, err error) {
	if err != nil {
		log.Printf("chat model=%s -> %s conv=%s status=%d dur=%s err=%v", asked, chose, convID, status, time.Since(start).Round(time.Millisecond), err)
		return
	}
	log.Printf("chat model=%s -> %s conv=%s status=%d dur=%s", asked, chose, convID, status, time.Since(start).Round(time.Millisecond))
}
