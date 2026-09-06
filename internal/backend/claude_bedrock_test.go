package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

type fakeBedrockConverse struct {
	input *bedrockruntime.ConverseInput
	out   *bedrockruntime.ConverseOutput
}

func (f *fakeBedrockConverse) Converse(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.input = in
	return f.out, nil
}

func TestClaudeBedrockMapsNativeToolsAndCachePoints(t *testing.T) {
	fake := &fakeBedrockConverse{out: &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{
		Role: types.ConversationRoleAssistant,
		Content: []types.ContentBlock{&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
			ToolUseId: aws.String("call-1"), Name: aws.String("weather"), Input: document.NewLazyDocument(map[string]any{"city": "Chennai"}),
		}}},
	}}}}
	b := newClaudeBedrockWithClient(&config.Model{Name: "claude", UpstreamModel: "anthropic.claude-test"}, fake)
	req := &api.ChatRequest{
		Messages: []api.Message{
			{Role: "system", Content: json.RawMessage(`"Be concise"`)},
			{Role: "user", Content: json.RawMessage(`"Weather?"`)},
		},
		Tools:      json.RawMessage(`[{"type":"function","function":{"name":"weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`),
		ToolChoice: json.RawMessage(`"required"`),
	}
	result, err := b.CompleteResult(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "weather" || result.ToolCalls[0].Function.Arguments != `{"city":"Chennai"}` {
		t.Fatalf("unexpected tool result: %#v", result.ToolCalls)
	}
	if len(fake.input.System) != 2 {
		t.Fatalf("system blocks = %d, want text + cache point", len(fake.input.System))
	}
	if _, ok := fake.input.System[1].(*types.SystemContentBlockMemberCachePoint); !ok {
		t.Fatalf("last system block is %T, want cache point", fake.input.System[1])
	}
	if fake.input.ToolConfig == nil || len(fake.input.ToolConfig.Tools) != 2 {
		t.Fatalf("tool config = %#v, want tool + cache point", fake.input.ToolConfig)
	}
	if _, ok := fake.input.ToolConfig.Tools[1].(*types.ToolMemberCachePoint); !ok {
		t.Fatalf("last tool block is %T, want cache point", fake.input.ToolConfig.Tools[1])
	}
	if _, ok := fake.input.ToolConfig.ToolChoice.(*types.ToolChoiceMemberAny); !ok {
		t.Fatalf("tool choice is %T, want required/any", fake.input.ToolConfig.ToolChoice)
	}
}

func TestClaudeBedrockMapsToolResultConversation(t *testing.T) {
	calls := json.RawMessage(`[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Chennai\"}"}}]`)
	_, messages, err := bedrockMessages([]api.Message{
		{Role: "assistant", Content: json.RawMessage(`""`), ToolCalls: calls},
		{Role: "tool", ToolCallID: "call-1", Content: json.RawMessage(`"31 C"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != types.ConversationRoleAssistant || messages[1].Role != types.ConversationRoleUser {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	if _, ok := messages[1].Content[0].(*types.ContentBlockMemberToolResult); !ok {
		t.Fatalf("tool response block is %T", messages[1].Content[0])
	}
}

func TestBuiltInSubscriptionAdaptersSelfRegister(t *testing.T) {
	devin, err := NewSubscription(context.Background(), &config.Model{Provider: "devin"}, http.DefaultClient)
	if err != nil || devin == nil {
		t.Fatalf("devin registration: backend=%T err=%v", devin, err)
	}
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	claude, err := NewSubscription(context.Background(), &config.Model{Name: "claude", Provider: "claude-bedrock", UpstreamModel: "anthropic.claude-test"}, http.DefaultClient)
	if err != nil || claude == nil {
		t.Fatalf("claude registration: backend=%T err=%v", claude, err)
	}
}
