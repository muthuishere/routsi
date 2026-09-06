package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDiscoverModelsUsesConnectWithoutLeakingCredential(t *testing.T) {
	const key = "SECRET_DISCOVERY_CANARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != discoveryMethod {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/proto" {
			t.Error("missing protobuf content type")
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Error("missing Connect version")
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("missing runtime credential")
		}
		w.Header().Set("Content-Type", "application/proto")
		w.Write([]byte{0x0a, 0x01, 'x'})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	m, err := discoverModels(context.Background(), server.Client(), credentials{apiKey: secret{value: key}, server: u})
	if err != nil {
		t.Fatal(err)
	}
	if m.HTTPStatus != 200 || m.TopLevelFields["1:2"] != 1 {
		t.Fatalf("metadata = %#v", m)
	}
}
