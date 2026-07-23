// Package metrics is an in-process, dependency-free collector for routsi.
// It exposes Prometheus text (/metrics) and a JSON snapshot (/stats) that the
// embedded dashboard polls. Everything is bounded — one struct per model —
// so it never grows without limit.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one completed request, recorded by the server.
type Event struct {
	Model            string // the model that actually answered
	Provider         string
	Routed           bool // true if auto/dynamic chose it, false on bypass
	Escalated        bool // true if this turn escalated a conversation
	PromptTokens     int
	CompletionTokens int
	LatencyMs        int64
	Err              bool
}

type modelStat struct {
	Requests   int64
	Errors     int64
	Escalations int64
	PromptTok  int64
	ComplTok   int64
	LatSumMs   int64
	LatMaxMs   int64
}

// Collector is safe for concurrent use.
type Collector struct {
	mu       sync.Mutex
	start    time.Time
	now      func() time.Time
	total    int64
	routed   int64
	bypass   int64
	errors   int64
	perModel map[string]*modelStat
	provider map[string]string // model -> provider
}

func New() *Collector {
	return &Collector{
		start:    time.Now(),
		now:      time.Now,
		perModel: map[string]*modelStat{},
		provider: map[string]string{},
	}
}

func (c *Collector) Record(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
	if ev.Routed {
		c.routed++
	} else {
		c.bypass++
	}
	if ev.Err {
		c.errors++
	}
	s := c.perModel[ev.Model]
	if s == nil {
		s = &modelStat{}
		c.perModel[ev.Model] = s
	}
	c.provider[ev.Model] = ev.Provider
	s.Requests++
	if ev.Err {
		s.Errors++
	}
	if ev.Escalated {
		s.Escalations++
	}
	s.PromptTok += int64(ev.PromptTokens)
	s.ComplTok += int64(ev.CompletionTokens)
	s.LatSumMs += ev.LatencyMs
	if ev.LatencyMs > s.LatMaxMs {
		s.LatMaxMs = ev.LatencyMs
	}
}

// ModelSnapshot is the per-model view for JSON/dashboard.
type ModelSnapshot struct {
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	Escalations      int64  `json:"escalations"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	AvgLatencyMs     int64  `json:"avg_latency_ms"`
	MaxLatencyMs     int64  `json:"max_latency_ms"`
}

type Snapshot struct {
	UptimeSeconds int64           `json:"uptime_seconds"`
	TotalRequests int64           `json:"total_requests"`
	RoutedRequests int64          `json:"routed_requests"`
	BypassRequests int64          `json:"bypass_requests"`
	TotalErrors   int64           `json:"total_errors"`
	Models        []ModelSnapshot `json:"models"`
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{
		UptimeSeconds:  int64(c.now().Sub(c.start).Seconds()),
		TotalRequests:  c.total,
		RoutedRequests: c.routed,
		BypassRequests: c.bypass,
		TotalErrors:    c.errors,
	}
	for name, s := range c.perModel {
		avg := int64(0)
		if s.Requests > 0 {
			avg = s.LatSumMs / s.Requests
		}
		snap.Models = append(snap.Models, ModelSnapshot{
			Model:            name,
			Provider:         c.provider[name],
			Requests:         s.Requests,
			Errors:           s.Errors,
			Escalations:      s.Escalations,
			PromptTokens:     s.PromptTok,
			CompletionTokens: s.ComplTok,
			AvgLatencyMs:     avg,
			MaxLatencyMs:     s.LatMaxMs,
		})
	}
	sort.Slice(snap.Models, func(i, j int) bool {
		return snap.Models[i].Requests > snap.Models[j].Requests
	})
	return snap
}

// Prometheus renders the standard text exposition format.
func (c *Collector) Prometheus() string {
	snap := c.Snapshot()
	var b strings.Builder
	b.WriteString("# HELP routsi_requests_total Total chat requests.\n# TYPE routsi_requests_total counter\n")
	fmt.Fprintf(&b, "routsi_requests_total %d\n", snap.TotalRequests)
	b.WriteString("# HELP routsi_requests_routed_total Requests where routsi chose the model.\n# TYPE routsi_requests_routed_total counter\n")
	fmt.Fprintf(&b, "routsi_requests_routed_total %d\n", snap.RoutedRequests)
	fmt.Fprintf(&b, "routsi_requests_bypass_total %d\n", snap.BypassRequests)
	b.WriteString("# HELP routsi_errors_total Total upstream/backend errors.\n# TYPE routsi_errors_total counter\n")
	fmt.Fprintf(&b, "routsi_errors_total %d\n", snap.TotalErrors)
	fmt.Fprintf(&b, "routsi_uptime_seconds %d\n", snap.UptimeSeconds)
	b.WriteString("# HELP routsi_model_requests_total Per-model request count.\n# TYPE routsi_model_requests_total counter\n")
	for _, m := range snap.Models {
		lbl := fmt.Sprintf("{model=%q,provider=%q}", m.Model, m.Provider)
		fmt.Fprintf(&b, "routsi_model_requests_total%s %d\n", lbl, m.Requests)
		fmt.Fprintf(&b, "routsi_model_errors_total%s %d\n", lbl, m.Errors)
		fmt.Fprintf(&b, "routsi_model_escalations_total%s %d\n", lbl, m.Escalations)
		fmt.Fprintf(&b, "routsi_model_prompt_tokens_total%s %d\n", lbl, m.PromptTokens)
		fmt.Fprintf(&b, "routsi_model_completion_tokens_total%s %d\n", lbl, m.CompletionTokens)
		fmt.Fprintf(&b, "routsi_model_latency_avg_ms%s %d\n", lbl, m.AvgLatencyMs)
		fmt.Fprintf(&b, "routsi_model_latency_max_ms%s %d\n", lbl, m.MaxLatencyMs)
	}
	return b.String()
}
