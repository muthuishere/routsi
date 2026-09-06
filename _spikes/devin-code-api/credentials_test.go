package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCredentialsDoesNotExposeKey(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "credentials.toml")
	const canary = "SECRET_CANARY_MUST_NOT_ESCAPE"
	err := os.WriteFile(p, []byte("windsurf_api_key = \""+canary+"\"\napi_server_url = \"https://server.codeium.com\"\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	c, m, err := loadCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Loaded || !m.KeyPresent || m.ServerHost != "server.codeium.com" {
		t.Fatalf("bad metadata: %#v", m)
	}
	if c.apiKey.value != canary {
		t.Fatal("key not loaded")
	}
}

func TestScanWire(t *testing.T) {
	got, err := scanWire([]byte{0x0a, 0x01, 'x', 0x10, 0x07, 0x0a, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if got["1:2"] != 2 || got["2:0"] != 1 {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestRejectPermissions(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "credentials.toml")
	if err := os.WriteFile(p, []byte("windsurf_api_key=\"fake\"\napi_server_url=\"https://server.codeium.com\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCredentials(p); err == nil {
		t.Fatal("expected broad permissions to be rejected")
	}
}

func TestRejectUntrustedHost(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "credentials.toml")
	if err := os.WriteFile(p, []byte("windsurf_api_key=\"fake\"\napi_server_url=\"https://attacker.example\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCredentials(p); err == nil {
		t.Fatal("expected untrusted host to be rejected")
	}
}

func TestInvalidURLIsSanitized(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "credentials.toml")
	os.WriteFile(p, []byte("windsurf_api_key=\"secret-value\"\napi_server_url=\"http://bad\"\n"), 0600)
	_, _, err := loadCredentials(p)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe error: %v", err)
	}
}
