package config

import "testing"

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
