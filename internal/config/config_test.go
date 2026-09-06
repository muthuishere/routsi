package config

import (
	"strings"
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

func TestDevinSubscriptionDoesNotRequireCLI(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: devin
    type: subscription
    provider: devin
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Model("devin").Type; got != TypeSubscription {
		t.Fatalf("type = %q", got)
	}
}

func TestSubscriptionProviderValidationIsDeferredToBackendRegistry(t *testing.T) {
	_, err := Parse([]byte(`
models:
  - name: unknown
    type: subscription
    provider: unknown
`))
	if err != nil {
		t.Fatalf("provider registry should validate at server startup: %v", err)
	}
}

func TestEnvironmentExpansionAppliesAcrossYAMLValues(t *testing.T) {
	t.Setenv("ROUTSI_TEST_LISTEN", ":19191")
	t.Setenv("ROUTSI_TEST_MODEL", "worker-from-env")
	t.Setenv("ROUTSI_TEST_BUCKET", "usage-bucket")
	t.Setenv("ROUTSI_TEST_PATH_STYLE", "true")
	t.Setenv("ROUTSI_TEST_BATCH", "17")
	t.Setenv("ROUTSI_TEST_ACCESS", "fake-access")
	t.Setenv("ROUTSI_TEST_SECRET", "fake-secret")
	cfg, err := Parse([]byte(`
listen: ${ROUTSI_TEST_LISTEN}
analytics:
  bucket: ${ROUTSI_TEST_BUCKET}
  use_path_style: ${ROUTSI_TEST_PATH_STYLE}
  batch_size: ${ROUTSI_TEST_BATCH}
  access_key: ${ROUTSI_TEST_ACCESS}
  secret_key: ${ROUTSI_TEST_SECRET}
models:
  - name: ${ROUTSI_TEST_MODEL}
    type: queue
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":19191" || cfg.Models[0].Name != "worker-from-env" || cfg.Analytics.Bucket != "usage-bucket" || !cfg.Analytics.UsePathStyle || cfg.Analytics.BatchSize != 17 {
		t.Fatalf("environment expansion did not populate all YAML value types")
	}
	if cfg.Analytics.AccessKey == "" || cfg.Analytics.SecretKey == "" {
		t.Fatal("credential references were not expanded")
	}
}

func TestEnvironmentExpansionIgnoresComments(t *testing.T) {
	_, err := Parse([]byte("models:\n  # - name: ${ROUTSI_MISSING_COMMENT_ONLY}\n  - name: worker\n    type: queue\n"))
	if err != nil {
		t.Fatalf("comment reference must stay inert: %v", err)
	}
}

func TestEnvironmentNameFieldsRemainNames(t *testing.T) {
	cfg, err := Parse([]byte("auth:\n  tokens_env: ROUTSI_TOKENS\nmodels:\n  - name: worker\n    type: queue\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.TokensEnv != "ROUTSI_TOKENS" {
		t.Fatalf("tokens_env changed to %q", cfg.Auth.TokensEnv)
	}
}

func TestEnvironmentExpansionRejectsMissingVariable(t *testing.T) {
	_, err := Parse([]byte("models:\n  - name: ${ROUTSI_DEFINITELY_MISSING}\n    type: queue\n"))
	if err == nil || !strings.Contains(err.Error(), "ROUTSI_DEFINITELY_MISSING") {
		t.Fatalf("expected named missing-variable error, got %v", err)
	}
}

func TestLiteralAnalyticsCredentialRejectedBeforeDecode(t *testing.T) {
	_, err := Parse([]byte("analytics:\n  bucket: usage\n  access_key: literal\n  secret_key: literal\nmodels:\n  - name: worker\n    type: queue\n"))
	if err == nil || !strings.Contains(err.Error(), "must use ${ENV_VAR}") {
		t.Fatalf("expected literal credential rejection, got %v", err)
	}
}
