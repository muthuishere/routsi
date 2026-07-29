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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/audit"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
	"github.com/muthuishere/routsi/internal/metrics"
	"github.com/muthuishere/routsi/internal/queue"
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
	audit    *audit.Ring
	tokens   []string // bearer tokens; empty = auth off
	broker   *queue.Broker

	dmu     sync.RWMutex       // guards dynamic (runtime-registered) queue targets
	dynamic map[string]*target // queue name -> target, added on worker register
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
		audit:    audit.New(),
		tokens:   cfg.Auth.AuthTokens(),
		broker:   queue.NewWithConfig(cfg.Workers.Freshness, cfg.Workers.MaxWait),
		dynamic:  map[string]*target{},
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
		case m.Type == config.TypeQueue:
			// Config-declared queue reserves the name; a worker supplies
			// answers at runtime via the broker.
			s.broker.Register(m.Name)
			t.backend = backend.NewQueue(s.broker, m.Name)
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
	// Guarded: the API and observability data (they spend keys / expose usage).
	mux.HandleFunc("POST /v1/chat/completions", s.guard(s.chat))
	mux.HandleFunc("GET /v1/models", s.guard(s.models))
	mux.HandleFunc("GET /metrics", s.guard(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, s.metrics.Prometheus())
	}))
	mux.HandleFunc("GET /stats", s.guard(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.metrics.Snapshot())
	}))
	mux.HandleFunc("GET /audit", s.guard(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 200 {
			limit = 200
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"decisions": s.audit.Recent(limit)})
	}))
	mux.HandleFunc("GET /config", s.guard(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.configSnapshot())
	}))
	// Worker (pull-worker) endpoints — no worker auth in v1 (ADR-001); the
	// status list rides the same guard as /stats.
	mux.HandleFunc("POST /v1/workers/register", s.workerRegister)
	mux.HandleFunc("GET /v1/workers/{name}/jobs", s.workerPoll)
	mux.HandleFunc("POST /v1/workers/{name}/jobs/{id}", s.workerAnswer)
	mux.HandleFunc("GET /v1/workers", s.guard(s.workers))
	// Open: liveness, and the dashboard shell (holds no data; it fetches /stats
	// with the token the operator passes as /?token=...).
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
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
	source := s.decisionSource(dec)

	start := time.Now()
	ev := metrics.Event{Model: t.model.Name, Provider: t.model.Provider, Routed: dec.routed, Escalated: dec.escalated}
	// Raw passthrough when the client owns the history. An explicit
	// conversation id flips a forward into proxy-managed memory: the client
	// sends only the new message; toolnexus's store carries the transcript.
	if t.forward != nil && !explicitConversation(convID) {
		status, err := t.forward.Relay(w, r, &req)
		latency := time.Since(start).Milliseconds()
		ev.LatencyMs, ev.Err = latency, err != nil
		s.metrics.Record(ev) // token counts unknown on raw passthrough
		s.recordAudit(req.Model, t.model.Name, dec.level, source, status, latency, nil, convID)
		logReq(req.Model, t.model.Name, convID, status, start, err)
		return
	}
	usage, err := s.envelope(w, r, t, &req)
	latency := time.Since(start).Milliseconds()
	ev.LatencyMs = latency
	if usage != nil {
		ev.PromptTokens, ev.CompletionTokens = usage.PromptTokens, usage.CompletionTokens
	}
	ev.Err = err != nil
	s.metrics.Record(ev)
	status := http.StatusOK
	if err != nil {
		status = 0
	}
	s.recordAudit(req.Model, t.model.Name, dec.level, source, status, latency, usage, convID)
	logReq(req.Model, t.model.Name, convID, status, start, err)
}

// recordAudit appends one decision to the audit ring. Never blocks or fails
// the request — the ring is an in-memory, mutex-guarded, fixed-size buffer.
// Only metadata and token counts are stored; message/prompt content and
// secrets never reach this path.
func (s *Server) recordAudit(requested, selected, level, source string, status int, latencyMs int64, usage *api.Usage, convID string) {
	d := audit.Decision{
		Time:           time.Now().UTC().Format(time.RFC3339),
		RequestedModel: requested,
		SelectedModel:  selected,
		Level:          level,
		Source:         source,
		Status:         status,
		LatencyMs:      latencyMs,
		ConversationID: convID,
	}
	if usage != nil {
		d.Tokens = audit.Tokens{Prompt: usage.PromptTokens, Completion: usage.CompletionTokens, Total: usage.TotalTokens}
	}
	s.audit.Record(d)
}

// resolve picks the target. Concrete model name = bypass, no routing. "auto"
// routes over the global tiers map; a dynamic model routes over its own
// levels map. Both share the group semantics: classify the task, pin per
// conversation, escalate-only (never downgrade mid-conversation).
// decision records how a target was chosen, for metrics.
type decision struct {
	routed    bool   // auto/dynamic chose it (vs a bypass to a concrete name)
	escalated bool   // this turn escalated a pinned conversation upward
	group     string // "auto" or the dynamic model's name; empty on bypass
	pinReused bool   // an existing sticky pin was reused unchanged this turn
	level     string // the level the resolved model corresponds to; "" on bypass
}

func (s *Server) resolve(req *api.ChatRequest, convID string) (*target, decision) {
	if req.Model == RouterModel {
		return s.resolveGroup("auto", s.cfg.Tiers, req, convID)
	}
	if m := s.cfg.Model(req.Model); m != nil && m.Type == config.TypeDynamic {
		return s.resolveGroup(m.Name, m.Levels, req, convID)
	}
	if t := s.targets[req.Model]; t != nil {
		return t, decision{}
	}
	// A worker that registered at runtime is addressable by its queue name.
	s.dmu.RLock()
	t := s.dynamic[req.Model]
	s.dmu.RUnlock()
	return t, decision{}
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
			return s.targets[picked], decision{routed: true, escalated: true, group: group, level: levelOf(levels, picked)}
		}
		return s.targets[pinned], decision{routed: true, group: group, pinReused: true, level: levelOf(levels, pinned)}
	}
	s.sticky.Pin(key, picked)
	return s.targets[picked], decision{routed: true, group: group, level: levelOf(levels, picked)}
}

// decisionSource classifies HOW a target was chosen, for the audit trail.
// Best-effort: it derives from what the handler already knows (routed vs
// bypass, a reused pin, and whether an external decider is configured), not
// from any deeper plumbing into router.External.
func (s *Server) decisionSource(dec decision) string {
	if !dec.routed {
		return "bypass"
	}
	if dec.pinReused {
		return "sticky-pin"
	}
	if dec.group == RouterModel {
		if _, ok := s.router.(*router.External); ok {
			return "auto-external"
		}
		return "auto-rules"
	}
	return "dynamic"
}

// backendStatus maps a backend error to an HTTP status. A queue with no live
// worker fails fast as 503 (ADR-001) so callers don't hang.
func backendStatus(err error) int {
	if errors.Is(err, queue.ErrNoWorker) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

// --- worker (pull-worker) endpoints ---

func (s *Server) workerRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Name == "" {
		httpError(w, http.StatusBadRequest, "register: JSON body {\"name\":\"...\"} required")
		return
	}
	name := body.Name
	// A worker may not shadow a configured model or the router aliases.
	if name == RouterModel || s.cfg.Model(name) != nil || s.cfg.Tiers[name] != "" {
		if m := s.cfg.Model(name); m == nil || m.Type != config.TypeQueue {
			httpError(w, http.StatusConflict, "name %q is reserved by a configured model", name)
			return
		}
	}
	s.broker.Register(name)
	s.dmu.Lock()
	if s.dynamic[name] == nil && s.cfg.Model(name) == nil {
		s.dynamic[name] = &target{
			model:   &config.Model{Name: name, Type: config.TypeQueue, Provider: "worker"},
			backend: backend.NewQueue(s.broker, name),
		}
	}
	s.dmu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "queue": name})
}

func (s *Server) workerPoll(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	wait := 25 * time.Second
	if q := r.URL.Query().Get("wait"); q != "" {
		if d, err := time.ParseDuration(q + "s"); err == nil && d > 0 && d <= 60*time.Second {
			wait = d
		}
	}
	job, ok := s.broker.Poll(r.Context(), name, wait)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (s *Server) workerAnswer(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	var body struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function *struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
			// simplified worker shape: {"name":..., "arguments":{...}}
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<20)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "answer: JSON body {\"content\":\"...\"} required")
		return
	}
	res := api.Result{Content: body.Content}
	for i, tc := range body.ToolCalls {
		call := api.ToolCall{ID: tc.ID, Type: tc.Type}
		if call.Type == "" {
			call.Type = "function"
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%s_%d", id, i)
		}
		if tc.Function != nil {
			call.Function = api.ToolFunction{Name: tc.Function.Name, Arguments: tc.Function.Arguments}
		} else {
			args := string(tc.Arguments)
			var asStr string
			if json.Unmarshal(tc.Arguments, &asStr) == nil {
				args = asStr // worker sent arguments as a JSON-encoded string already
			}
			if args == "" {
				args = "{}"
			}
			call.Function = api.ToolFunction{Name: tc.Name, Arguments: args}
		}
		res.ToolCalls = append(res.ToolCalls, call)
	}
	if err := s.broker.Answer(name, id, res); err != nil {
		httpError(w, http.StatusConflict, "answer: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) workers(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"workers": s.broker.States()})
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
	rb, structured := t.backend.(backend.ResultBackend)

	if !req.Stream {
		var resp api.ChatResponse
		var text string
		if structured {
			res, err := rb.CompleteResult(r.Context(), req)
			if err != nil {
				httpError(w, backendStatus(err), "backend %s: %v", t.model.Name, err)
				return nil, err
			}
			resp, text = api.NewResultCompletion(t.model.Name, res), res.Content
		} else {
			var err error
			text, err = t.backend.Complete(r.Context(), req)
			if err != nil {
				httpError(w, backendStatus(err), "backend %s: %v", t.model.Name, err)
				return nil, err
			}
			resp = api.NewCompletion(t.model.Name, text)
		}
		w.Header().Set("Content-Type", "application/json")
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

	// Buffered backends (agents, pull-workers, custom, translated) block
	// inside Stream for many seconds without writing anything; run it in a
	// goroutine and funnel its deltas through a channel so the handler
	// goroutine can interleave SSE heartbeat comments while it waits. All
	// writes to w happen on this (the handler) goroutine only, so a delta and
	// a heartbeat can never interleave into a half-written frame.
	deltaCh := make(chan string)
	doneCh := make(chan error, 1)
	var structRes api.Result // written before doneCh send; read after doneCh recv
	go func() {
		defer close(deltaCh)
		if structured {
			res, err := rb.CompleteResult(r.Context(), req)
			if err == nil {
				structRes = res
				if res.Content != "" {
					deltaCh <- res.Content
				}
			}
			doneCh <- err
			return
		}
		doneCh <- t.backend.Stream(r.Context(), req, func(delta string) {
			if delta != "" {
				deltaCh <- delta
			}
		})
	}()

	var ticker *time.Ticker
	var tickCh <-chan time.Time
	if s.cfg.StreamHeartbeat > 0 {
		ticker = time.NewTicker(s.cfg.StreamHeartbeat)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	var streamed strings.Builder
	var streamErr error
	activity := false // any delta written since the last tick?
loop:
	for {
		select {
		case delta, ok := <-deltaCh:
			if !ok {
				deltaCh = nil // avoid a tight busy-loop once closed; doneCh still pending
				continue
			}
			activity = true
			streamed.WriteString(delta)
			writeChunk(api.NewChunk(id, t.model.Name, delta, false))
		case streamErr = <-doneCh:
			break loop
		case <-tickCh:
			if !activity {
				fmt.Fprint(w, ": ping\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
			activity = false
		case <-r.Context().Done():
			streamErr = r.Context().Err()
			break loop
		}
	}

	if streamErr != nil {
		// Headers are gone; surface the error inside the stream.
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q}}\n\n", streamErr.Error())
		if flusher != nil {
			flusher.Flush()
		}
		return nil, streamErr
	}
	var final api.ChatResponse
	if len(structRes.ToolCalls) > 0 {
		writeChunk(api.NewToolChunk(id, t.model.Name, structRes.ToolCalls))
		final = api.FinishChunk(id, t.model.Name, "tool_calls")
	} else {
		final = api.NewChunk(id, t.model.Name, "", true)
	}
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
	// Runtime-registered worker queues are routable models too.
	s.dmu.RLock()
	for name := range s.dynamic {
		list = append(list, m{ID: name, Object: "model", OwnedBy: "worker"})
	}
	s.dmu.RUnlock()
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
