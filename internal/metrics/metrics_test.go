package metrics

import (
	"strings"
	"testing"
)

func TestCollectorAggregates(t *testing.T) {
	c := New()
	c.Record(Event{Model: "a", Provider: "openai", Routed: true, PromptTokens: 10, CompletionTokens: 5, LatencyMs: 100})
	c.Record(Event{Model: "a", Provider: "openai", Routed: true, Escalated: true, PromptTokens: 20, CompletionTokens: 10, LatencyMs: 300})
	c.Record(Event{Model: "b", Provider: "github", PromptTokens: 1, CompletionTokens: 1, LatencyMs: 50, Err: true})

	snap := c.Snapshot()
	if snap.TotalRequests != 3 || snap.RoutedRequests != 2 || snap.BypassRequests != 1 || snap.TotalErrors != 1 {
		t.Fatalf("totals wrong: %+v", snap)
	}
	// Sorted by requests desc: a (2) before b (1).
	if snap.Models[0].Model != "a" {
		t.Fatalf("sort wrong: %v", snap.Models)
	}
	a := snap.Models[0]
	if a.Requests != 2 || a.Escalations != 1 || a.PromptTokens != 30 || a.CompletionTokens != 15 {
		t.Fatalf("model a wrong: %+v", a)
	}
	if a.AvgLatencyMs != 200 || a.MaxLatencyMs != 300 {
		t.Fatalf("latency wrong: avg=%d max=%d", a.AvgLatencyMs, a.MaxLatencyMs)
	}
}

func TestPrometheusFormat(t *testing.T) {
	c := New()
	c.Record(Event{Model: "gpt", Provider: "openai", Routed: true, LatencyMs: 10})
	out := c.Prometheus()
	for _, want := range []string{
		"routsi_requests_total 1",
		"routsi_requests_routed_total 1",
		`routsi_model_requests_total{model="gpt",provider="openai"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("prometheus output missing %q in:\n%s", want, out)
		}
	}
}
