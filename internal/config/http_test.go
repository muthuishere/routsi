package config

import (
	"testing"
	"time"
)

// An omitted http: block must still produce a pooled, non-default transport —
// that is the whole point of "safe defaults": the operator gets the good
// behaviour without knowing the block exists.
func TestHTTPDefaultsApplied(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  - name: m
    type: forward
    base_url: http://example.invalid/v1
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.HTTP
	if got.MaxIdleConnsPerHost != 256 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 256", got.MaxIdleConnsPerHost)
	}
	if got.MaxIdleConns != 512 {
		t.Errorf("MaxIdleConns = %d, want 512", got.MaxIdleConns)
	}
	if got.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", got.Timeout)
	}
	if got.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", got.IdleConnTimeout)
	}
	if got.RetryCount() != 2 {
		t.Errorf("RetryCount = %d, want 2", got.RetryCount())
	}
}

func TestHTTPOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
http:
  timeout: 30s
  max_idle_conns: 8
  max_idle_conns_per_host: 4
  idle_conn_timeout: 5s
  disable_http2: true
  retries: 0
models:
  - name: m
    type: forward
    base_url: http://example.invalid/v1
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.HTTP
	if got.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", got.Timeout)
	}
	if got.MaxIdleConnsPerHost != 4 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 4", got.MaxIdleConnsPerHost)
	}
	if got.MaxIdleConns != 8 {
		t.Errorf("MaxIdleConns = %d, want 8", got.MaxIdleConns)
	}
	if !got.DisableHTTP2 {
		t.Error("DisableHTTP2 = false, want true")
	}
	// retries: 0 is a meaningful value, not "unset" — it must survive defaulting.
	if got.RetryCount() != 0 {
		t.Errorf("RetryCount = %d, want 0 (explicit zero must not be defaulted)", got.RetryCount())
	}
}
