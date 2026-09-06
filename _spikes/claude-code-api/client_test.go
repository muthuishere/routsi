package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeCredentialSource struct{ credential OAuthCredential }

func (f fakeCredentialSource) Load(context.Context) (OAuthCredential, error) {
	return f.credential, nil
}
func (f fakeCredentialSource) Refresh(context.Context) (OAuthCredential, error) {
	return f.credential, nil
}

func TestCanaryUsesBearerWithoutExposingIt(t *testing.T) {
	const fakeToken = "fake-access-token-for-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			t.Error("missing fake bearer token")
		}
		if r.Header.Get("Anthropic-Beta") == "" || r.Header.Get("X-App") != "cli" {
			t.Error("missing subscription compatibility headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"MARKER\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), Auth: fakeCredentialSource{OAuthCredential{AccessToken: fakeToken}}, URL: server.URL}
	got, err := c.Canary(context.Background(), MessagesRequest{Model: "claude-test", MaxTokens: 1, Stream: true, Messages: []Message{{Role: "user", Content: "test"}}}, "MARKER")
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Model != "claude-test" {
		t.Fatalf("unexpected sanitized result: %+v", got)
	}
}

func TestConsumeSSERejectsTruncation(t *testing.T) {
	stream := `data: {"type":"message_start","message":{"model":"claude-test"}}` + "\n"
	var got CanaryResult
	if err := consumeSSE(strings.NewReader(stream), "marker", &got); err == nil {
		t.Fatal("expected truncated stream rejection")
	}
}

func TestConsumeSSESanitizesAndSummarizes(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-test","usage":{"input_tokens":7,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ROUTSI_"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"CLAUDE_CANARY_OK"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	var got CanaryResult
	if err := consumeSSE(strings.NewReader(stream), "ROUTSI_CLAUDE_CANARY_OK", &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Model != "claude-test" || got.StopReason != "end_turn" || got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 5 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.TextBytes != len("ROUTSI_CLAUDE_CANARY_OK") {
		t.Fatalf("unexpected text byte count: %d", got.TextBytes)
	}
}

func TestConsumeSSEDoesNotReturnProviderBodyOnMalformedFrame(t *testing.T) {
	err := consumeSSE(strings.NewReader("data: not-json-with-secret\n"), "marker", &CanaryResult{})
	if err == nil || strings.Contains(err.Error(), "not-json-with-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
