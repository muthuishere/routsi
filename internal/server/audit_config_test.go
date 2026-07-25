package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/audit"
	"github.com/muthuishere/routsi/internal/config"
)

// TestAuditRecordsDecisions: after driving a bypass and an auto-routed
// request, GET /audit reports metadata + tokens for each, newest first, and
// never leaks message content.
func TestAuditRecordsDecisions(t *testing.T) {
	var cheapModel, strongModel string
	up1 := mockUpstream(t, "cheap", &cheapModel)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &strongModel)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	const secret = "the-super-secret-prompt-text-xyz"
	// Bypass to a concrete model.
	resp := post(t, ts.URL, fmt.Sprintf(`{"model":"gpt-strong","messages":[{"role":"user","content":%q}]}`, secret), nil)
	resp.Body.Close()

	// auto-routed, easy question -> gpt-cheap.
	resp = post(t, ts.URL, `{"model":"auto","messages":[{"role":"user","content":"hi there"}]}`, nil)
	resp.Body.Close()

	r, err := http.Get(ts.URL + "/audit")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", r.StatusCode, body)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("audit leaked message content: %s", body)
	}

	var out struct {
		Decisions []audit.Decision `json:"decisions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Decisions) != 2 {
		t.Fatalf("got %d decisions, want 2: %+v", len(out.Decisions), out.Decisions)
	}
	// Newest first: the auto-routed request.
	autoDec := out.Decisions[0]
	if autoDec.RequestedModel != "auto" || autoDec.SelectedModel != "gpt-cheap" {
		t.Fatalf("auto decision = %+v", autoDec)
	}
	if autoDec.Level != "low" {
		t.Fatalf("auto level = %q, want low", autoDec.Level)
	}
	if autoDec.Source != "auto-rules" {
		t.Fatalf("auto source = %q, want auto-rules", autoDec.Source)
	}
	if autoDec.Status != http.StatusOK {
		t.Fatalf("auto status = %d", autoDec.Status)
	}

	bypassDec := out.Decisions[1]
	if bypassDec.RequestedModel != "gpt-strong" || bypassDec.SelectedModel != "gpt-strong" {
		t.Fatalf("bypass decision = %+v", bypassDec)
	}
	if bypassDec.Level != "" {
		t.Fatalf("bypass level = %q, want empty", bypassDec.Level)
	}
	if bypassDec.Source != "bypass" {
		t.Fatalf("bypass source = %q, want bypass", bypassDec.Source)
	}
	// gpt-strong here is a raw forward with no explicit conversation id, so
	// it's byte passthrough — tokens are unknown and expected to be 0.
	if bypassDec.Tokens.Total != 0 {
		t.Fatalf("raw passthrough should report 0 tokens, got %+v", bypassDec.Tokens)
	}
}

// TestAuditNoContentField: the JSON body must never contain a raw message
// or content key that could carry prompt text.
func TestAuditNoContentField(t *testing.T) {
	var a, b string
	up1 := mockUpstream(t, "cheap", &a)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &b)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	resp := post(t, ts.URL, `{"model":"my-agent","messages":[{"role":"user","content":"ping"}]}`, nil)
	resp.Body.Close()

	r, err := http.Get(ts.URL + "/audit")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "\"messages\"") || strings.Contains(string(body), "\"content\"") {
		t.Fatalf("audit body must never carry message content, got: %s", body)
	}
}

// TestConfigSnapshot: GET /config returns the sanitized shape, including the
// model catalog and dynamic groups, and never leaks a token value.
func TestConfigSnapshot(t *testing.T) {
	var a, b string
	up1 := mockUpstream(t, "cheap", &a)
	defer up1.Close()
	up2 := mockUpstream(t, "strong", &b)
	defer up2.Close()
	ts := testServer(t, up1.URL, up2.URL, echoRegistry())

	r, err := http.Get(ts.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", r.StatusCode, body)
	}
	var cv configView
	if err := json.Unmarshal(body, &cv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cv.Default != "gpt-cheap" {
		t.Fatalf("default = %q", cv.Default)
	}
	if cv.Auth {
		t.Fatal("auth should be off for this test server")
	}
	if cv.TLS != "off" {
		t.Fatalf("tls = %q, want off", cv.TLS)
	}
	if cv.Decider != "rules" {
		t.Fatalf("decider = %q, want rules", cv.Decider)
	}
	// testServer's "cheap"/"strong" tier keys normalize to router levels
	// low/high (config.validate).
	if cv.Tiers["low"] != "gpt-cheap" || cv.Tiers["high"] != "gpt-strong" {
		t.Fatalf("tiers = %+v", cv.Tiers)
	}
	if len(cv.Models) != 3 {
		t.Fatalf("models = %+v", cv.Models)
	}
}

// TestAuditAndConfigGuardedByAuth: with tokens configured, /audit and
// /config 401 without a token and 200 with one; /config never leaks the
// token value in its body.
func TestAuditAndConfigGuardedByAuth(t *testing.T) {
	var x string
	up := mockUpstream(t, "cheap", &x)
	defer up.Close()
	t.Setenv("ROUTSI_TEST_TOKENS_AC", "tok-secret-abc")

	yaml := fmt.Sprintf(`
default: gpt-cheap
auth:
  tokens_env: ROUTSI_TEST_TOKENS_AC
models:
  - name: gpt-cheap
    type: forward
    base_url: %s
`, up.URL)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, path := range []string{"/audit", "/config"} {
		r, _ := http.Get(ts.URL + path)
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without token: status %d, want 401", path, r.StatusCode)
		}
		r.Body.Close()

		r2, _ := http.Get(ts.URL + path + "?token=tok-secret-abc")
		body, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		if r2.StatusCode != http.StatusOK {
			t.Fatalf("%s with token: status %d: %s", path, r2.StatusCode, body)
		}
		if strings.Contains(string(body), "tok-secret-abc") {
			t.Fatalf("%s leaked the token value: %s", path, body)
		}
	}
}
