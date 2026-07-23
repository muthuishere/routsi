package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/muthuishere/routsi/internal/config"
)

func TestForwardDiscoveryFromUpstreamModels(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"m-small"},{"id":"m-large"},{"id":""}]}`)
	}))
	defer up.Close()

	t.Setenv("DISC_KEY", "sekret")
	cfg, err := config.Parse([]byte(fmt.Sprintf(`
models:
  - name: up
    type: forward
    base_url: %s
    api_key_env: DISC_KEY
    discover_models: true
`, up.URL)))
	if err != nil {
		t.Fatal(err)
	}
	if err := Populate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	for _, want := range []string{"up/m-small", "up/m-large"} {
		if cfg.Model(want) == nil {
			t.Fatalf("%q not discovered; have %d models", want, len(cfg.Models))
		}
	}
	if cfg.Model("up/") != nil {
		t.Fatal("empty id must be skipped")
	}
	// Base entry now proxies the first discovered model instead of itself.
	if got := cfg.Model("up").UpstreamModel; got != "m-small" {
		t.Fatalf("base upstream_model = %q", got)
	}
}

func TestDiscoveryErrorIsNonFatalPerModel(t *testing.T) {
	cfg, err := config.Parse([]byte(`
models:
  - name: dead
    type: forward
    base_url: http://127.0.0.1:1
    discover_models: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Populate(context.Background(), cfg); err == nil {
		t.Fatal("want a joined discovery error")
	}
	if cfg.Model("dead") == nil {
		t.Fatal("base model must survive discovery failure")
	}
}

// fakeDevinProbe mimics the devin CLI's unknown-model rejection.
func fakeDevinProbe(t *testing.T, output string) string {
	t.Helper()
	bin := t.TempDir() + "/devin"
	script := "#!/bin/sh\nprintf '%s\\n' \"Error: Unknown model: 'x'\" \"" + output + "\"\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestDevinProbeDiscoversLiveModels(t *testing.T) {
	bin := fakeDevinProbe(t, "Available: alpha, beta-2, gamma")
	cfg := &config.Config{Models: []config.Model{
		{Name: "devin", Type: config.TypeDevin, DiscoverModels: true, DevinBin: bin, UpstreamModel: "devin"},
	}}
	if err := Populate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"devin/alpha", "devin/beta-2", "devin/gamma"} {
		if cfg.Model(want) == nil {
			t.Fatalf("%q not discovered from probe: %+v", want, cfg.Models)
		}
	}
}

func TestDevinProbeFallsBackToTable(t *testing.T) {
	bin := fakeDevinProbe(t, "no list here")
	cfg := &config.Config{Models: []config.Model{
		{Name: "devin", Type: config.TypeDevin, DiscoverModels: true, DevinBin: bin, UpstreamModel: "devin"},
	}}
	if err := Populate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Model("devin/sonnet") == nil || cfg.Model("devin/opus") == nil {
		t.Fatalf("fallback table not applied: %+v", cfg.Models)
	}
}

func TestExplicitVariantsNotDuplicated(t *testing.T) {
	cfg, err := config.Parse([]byte(`
models:
  - name: devin
    type: devin
    bin: /bin/echo
    variants: [sonnet]
    discover_models: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Populate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range cfg.Models {
		if m.Name == "devin/sonnet" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("devin/sonnet appears %d times", count)
	}
	if cfg.Model("devin/opus") == nil {
		t.Fatal("discovery should still add models beyond explicit variants")
	}
}

func TestKnownModelsFileOverridesAndBootstraps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROUTSI_CONFIG_DIR", dir)

	// First call bootstraps the file with defaults.
	got := knownCLIModels()
	if _, err := os.Stat(dir + "/known-models.json"); err != nil {
		t.Fatalf("known-models.json not bootstrapped: %v", err)
	}
	if len(got[config.TypeClaude]) == 0 {
		t.Fatal("claude defaults missing")
	}

	// User edits win.
	if err := os.WriteFile(dir+"/known-models.json", []byte(`{"codex":["my-own-model"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got = knownCLIModels()
	if len(got[config.TypeCodex]) != 1 || got[config.TypeCodex][0] != "my-own-model" {
		t.Fatalf("codex override not applied: %v", got[config.TypeCodex])
	}
	// Unmentioned agents keep defaults.
	if len(got[config.TypeCopilot]) == 0 {
		t.Fatal("copilot defaults lost on partial override")
	}
}
