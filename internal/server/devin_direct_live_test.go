package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
)

func TestLiveDevinDirectThroughRoutsi(t *testing.T) {
	ts := liveDevinServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	body := `{"model":"devin-direct","messages":[{"role":"user","content":"Reply with exactly ROUTSI_DEVIN_DIRECT_OK and nothing else."}]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct Devin request returned HTTP %d: %s", resp.StatusCode, safeE2EError(raw))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 || strings.TrimSpace(completion.Choices[0].Message.Content) != "ROUTSI_DEVIN_DIRECT_OK" {
		t.Fatalf("unexpected direct Devin completion")
	}
}

func TestLiveDevinDirectNativeToolCall(t *testing.T) {
	ts := liveDevinServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	body := `{"model":"devin-direct","messages":[{"role":"user","content":"Call get_weather exactly once with city Chennai. Do not answer directly."}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get the current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false},"strict":true}}],"tool_choice":"required"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct Devin tool request returned HTTP %d: %s", resp.StatusCode, safeE2EError(raw))
	}
	var completion struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []api.ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].FinishReason != "tool_calls" || len(completion.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected one native tool call")
	}
	call := completion.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "get_weather" {
		t.Fatalf("tool name = %q", call.Function.Name)
	}
	var args struct {
		City string `json:"city"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.City != "Chennai" {
		t.Fatalf("unexpected tool arguments")
	}
}

func liveDevinServer(t *testing.T) *httptest.Server {
	t.Helper()
	if testing.Short() {
		t.Skip("live Devin E2E is disabled by -short")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot locate Devin authentication state")
	}
	info, err := os.Lstat(filepath.Join(home, ".local", "share", "devin", "credentials.toml"))
	if err != nil {
		t.Skip("Devin authentication state is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Skip("Devin authentication state is not safely reusable")
	}
	cfg, err := config.Parse([]byte("default: devin-direct\nmodels:\n  - name: devin-direct\n    type: subscription\n    provider: devin\n"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, backend.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func safeE2EError(raw []byte) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &v) == nil && v.Error.Message != "" {
		return v.Error.Message
	}
	return "unparseable error response"
}
