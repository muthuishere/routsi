package config

import (
	"testing"
	"time"
)

func TestVariantExpansion(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: up
    type: forward
    base_url: http://x
    provider: openai
    variants: [a, b]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 {
		t.Fatalf("got %d models, want base + 2 variants", len(cfg.Models))
	}
	base := cfg.Model("up")
	if base == nil || base.UpstreamModel != "a" {
		t.Fatalf("base upstream_model = %+v, want first variant", base)
	}
	for _, name := range []string{"up/a", "up/b"} {
		m := cfg.Model(name)
		if m == nil {
			t.Fatalf("variant %q not expanded", name)
		}
		if m.Provider != "openai" {
			t.Fatalf("variant provider = %q", m.Provider)
		}
	}
	if cfg.Model("up/b").UpstreamModel != "b" {
		t.Fatal("variant upstream_model not set")
	}
}

func TestCLIAgentBinAndArgsParsed(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: devin
    type: devin
    bin: echo
    args: ["--org", "acme"]
`))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Model("devin")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.DevinBin != "echo" {
		t.Fatalf("bin = %q, want echo", m.DevinBin)
	}
	if len(m.Args) != 2 || m.Args[0] != "--org" || m.Args[1] != "acme" {
		t.Fatalf("args = %v, want [--org acme]", m.Args)
	}
}

func TestWorkersDefaultsToZeroLeavesBrokerDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: up
    type: forward
    base_url: http://x
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers.Freshness != 0 || cfg.Workers.MaxWait != 0 {
		t.Fatalf("workers = %+v, want zero (broker applies its own defaults)", cfg.Workers)
	}
}

func TestWorkersFreshnessAndMaxWaitParsed(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: up
    type: forward
    base_url: http://x
workers:
  freshness: 10s
  max_wait: 1m
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers.Freshness != 10*time.Second {
		t.Fatalf("freshness = %v", cfg.Workers.Freshness)
	}
	if cfg.Workers.MaxWait != time.Minute {
		t.Fatalf("max_wait = %v", cfg.Workers.MaxWait)
	}
}

func TestProviderDefaultsToType(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: up
    type: forward
    base_url: http://x
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Model("up").Provider; got != "forward" {
		t.Fatalf("provider = %q, want type default", got)
	}
}
