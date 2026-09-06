package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
)

func TestLiveClaudeBedrockThroughRoutsi(t *testing.T) {
	ts := liveClaudeBedrockServer(t)
	raw := callClaudeBedrockE2E(t, ts, `{"model":"claude-aws","messages":[{"role":"user","content":"Reply with exactly ROUTSI_CLAUDE_BEDROCK_OK and nothing else."}]}`)
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &completion) != nil || len(completion.Choices) != 1 || strings.TrimSpace(completion.Choices[0].Message.Content) != "ROUTSI_CLAUDE_BEDROCK_OK" {
		t.Fatal("unexpected Claude Bedrock completion")
	}
}

func TestLiveClaudeBedrockNativeToolCall(t *testing.T) {
	ts := liveClaudeBedrockServer(t)
	raw := callClaudeBedrockE2E(t, ts, `{"model":"claude-aws","messages":[{"role":"user","content":"Call get_weather exactly once with city Chennai. Do not answer directly."}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"required"}`)
	var completion struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []api.ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &completion) != nil || len(completion.Choices) != 1 || completion.Choices[0].FinishReason != "tool_calls" || len(completion.Choices[0].Message.ToolCalls) != 1 {
		t.Fatal("expected one native Claude Bedrock tool call")
	}
	if completion.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatal("unexpected Claude Bedrock tool name")
	}
}

func liveClaudeBedrockServer(t *testing.T) *httptest.Server {
	t.Helper()
	if testing.Short() {
		t.Skip("live Claude Bedrock E2E is disabled by -short")
	}
	model := os.Getenv("ROUTSI_BEDROCK_TEST_MODEL")
	if model == "" {
		t.Skip("set ROUTSI_BEDROCK_TEST_MODEL to an enabled Claude Bedrock model or inference-profile ID")
	}
	region, profile := os.Getenv("ROUTSI_BEDROCK_TEST_REGION"), os.Getenv("ROUTSI_BEDROCK_TEST_PROFILE")
	var options []func(*awsconfig.LoadOptions) error
	if region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	if profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
	if err != nil || awsCfg.Region == "" {
		t.Skip("AWS configuration unavailable; configure a region and run aws login (or aws sso login for an SSO profile)")
	}
	if _, err := awsCfg.Credentials.Retrieve(context.Background()); err != nil {
		t.Skip("AWS credentials unavailable or expired; run aws login (or aws sso login for an SSO profile)")
	}
	cfg := &config.Config{Default: "claude-aws", Models: []config.Model{{Name: "claude-aws", Type: config.TypeSubscription, Provider: "claude-bedrock", UpstreamModel: model, AWSRegion: awsCfg.Region, AWSProfile: profile}}}
	s, err := New(cfg, backend.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func callClaudeBedrockE2E(t *testing.T, ts *httptest.Server, body string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
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
		t.Fatalf("Claude Bedrock returned HTTP %d: %s", resp.StatusCode, safeE2EError(raw))
	}
	return raw
}
