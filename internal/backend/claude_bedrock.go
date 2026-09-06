package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/muthuishere/routsi/internal/api"
	appconfig "github.com/muthuishere/routsi/internal/config"
)

// bedrockConverseAPI is deliberately narrow so the native protocol mapping can
// be tested without AWS credentials or network access.
type bedrockConverseAPI interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// ClaudeBedrock exposes Anthropic Claude on Amazon Bedrock behind Routsi's
// OpenAI-compatible endpoint. Authentication is SigV4 via the standard AWS SDK
// chain; no Claude CLI or API key extraction is involved.
type ClaudeBedrock struct {
	model  *appconfig.Model
	client bedrockConverseAPI
}

func init() {
	RegisterSubscription("claude-bedrock", func(ctx context.Context, m *appconfig.Model, _ *http.Client) (Backend, error) {
		return NewClaudeBedrock(ctx, m)
	})
}

func NewClaudeBedrock(ctx context.Context, m *appconfig.Model) (*ClaudeBedrock, error) {
	if m.UpstreamModel == "" {
		return nil, fmt.Errorf("claude-bedrock needs upstream_model")
	}
	var options []func(*awsconfig.LoadOptions) error
	if m.AWSRegion != "" {
		options = append(options, awsconfig.WithRegion(m.AWSRegion))
	}
	if m.AWSProfile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(m.AWSProfile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration for model %q: %w", m.Name, err)
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("model %q: AWS region is unavailable; set aws_region, AWS_REGION, or a profile region", m.Name)
	}
	return &ClaudeBedrock{model: m, client: bedrockruntime.NewFromConfig(cfg)}, nil
}

func newClaudeBedrockWithClient(m *appconfig.Model, client bedrockConverseAPI) *ClaudeBedrock {
	return &ClaudeBedrock{model: m, client: client}
}

func (b *ClaudeBedrock) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	res, err := b.CompleteResult(ctx, req)
	return res.Content, err
}

func (b *ClaudeBedrock) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	res, err := b.CompleteResult(ctx, req)
	if err != nil {
		return err
	}
	emit(res.Content)
	return nil
}

func (b *ClaudeBedrock) CompleteResult(ctx context.Context, req *api.ChatRequest) (api.Result, error) {
	in, err := b.converseInput(req)
	if err != nil {
		return api.Result{}, err
	}
	out, err := b.client.Converse(ctx, in)
	if err != nil {
		return api.Result{}, fmt.Errorf("Amazon Bedrock Converse failed for model %q; verify AWS login, Bedrock model access, region, and IAM permissions: %w", b.model.Name, err)
	}
	return bedrockResult(out)
}

func (b *ClaudeBedrock) converseInput(req *api.ChatRequest) (*bedrockruntime.ConverseInput, error) {
	system, messages, err := bedrockMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := bedrockTools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseInput{
		ModelId:         aws.String(b.model.UpstreamModel),
		Messages:        messages,
		System:          system,
		ToolConfig:      tools,
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(16000)},
	}, nil
}

func bedrockMessages(input []api.Message) ([]types.SystemContentBlock, []types.Message, error) {
	var system []types.SystemContentBlock
	var messages []types.Message
	for _, m := range input {
		switch m.Role {
		case "system", "developer":
			if text := m.Text(); text != "" {
				system = append(system, &types.SystemContentBlockMemberText{Value: text})
			}
		case "user":
			messages = append(messages, types.Message{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Text()}}})
		case "assistant":
			blocks := []types.ContentBlock{}
			if text := m.Text(); text != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: text})
			}
			if len(m.ToolCalls) > 0 {
				var calls []api.ToolCall
				if err := json.Unmarshal(m.ToolCalls, &calls); err != nil {
					return nil, nil, fmt.Errorf("decode assistant tool_calls: %w", err)
				}
				for _, call := range calls {
					var args any
					if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
						return nil, nil, fmt.Errorf("decode arguments for tool %q: %w", call.Function.Name, err)
					}
					blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String(call.ID), Name: aws.String(call.Function.Name), Input: document.NewLazyDocument(args)}})
				}
			}
			if len(blocks) > 0 {
				messages = append(messages, types.Message{Role: types.ConversationRoleAssistant, Content: blocks})
			}
		case "tool":
			block := &types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
				ToolUseId: aws.String(m.ToolCallID),
				Status:    types.ToolResultStatusSuccess,
				Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: m.Text()}},
			}}
			messages = append(messages, types.Message{Role: types.ConversationRoleUser, Content: []types.ContentBlock{block}})
		default:
			return nil, nil, fmt.Errorf("unsupported message role %q for claude-bedrock", m.Role)
		}
	}
	if len(system) > 0 {
		system = append(system, &types.SystemContentBlockMemberCachePoint{Value: types.CachePointBlock{Type: types.CachePointTypeDefault}})
	}
	return system, messages, nil
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func bedrockTools(raw, choice json.RawMessage) (*types.ToolConfiguration, error) {
	if len(raw) == 0 || string(choice) == `"none"` {
		return nil, nil
	}
	var definitions []openAITool
	if err := json.Unmarshal(raw, &definitions); err != nil {
		return nil, fmt.Errorf("decode tools: %w", err)
	}
	sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].Function.Name < definitions[j].Function.Name })
	toolConfig := &types.ToolConfiguration{}
	for _, definition := range definitions {
		if definition.Type != "function" || definition.Function.Name == "" {
			return nil, fmt.Errorf("claude-bedrock supports named function tools only")
		}
		var schema any
		if err := json.Unmarshal(definition.Function.Parameters, &schema); err != nil {
			return nil, fmt.Errorf("decode schema for tool %q: %w", definition.Function.Name, err)
		}
		toolConfig.Tools = append(toolConfig.Tools, &types.ToolMemberToolSpec{Value: types.ToolSpecification{
			Name: aws.String(definition.Function.Name), Description: aws.String(definition.Function.Description),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(schema)},
		}})
	}
	if len(toolConfig.Tools) == 0 {
		return nil, nil
	}
	toolConfig.Tools = append(toolConfig.Tools, &types.ToolMemberCachePoint{Value: types.CachePointBlock{Type: types.CachePointTypeDefault}})
	toolConfig.ToolChoice = bedrockToolChoice(choice)
	return toolConfig, nil
}

func bedrockToolChoice(raw json.RawMessage) types.ToolChoice {
	if len(raw) == 0 || string(raw) == `"auto"` {
		return &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
	}
	if string(raw) == `"required"` {
		return &types.ToolChoiceMemberAny{Value: types.AnyToolChoice{}}
	}
	var named struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &named) == nil && named.Function.Name != "" {
		return &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: aws.String(named.Function.Name)}}
	}
	return &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
}

func bedrockResult(out *bedrockruntime.ConverseOutput) (api.Result, error) {
	message, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return api.Result{}, fmt.Errorf("Amazon Bedrock returned no message")
	}
	var result api.Result
	for _, block := range message.Value.Content {
		switch value := block.(type) {
		case *types.ContentBlockMemberText:
			result.Content += value.Value
		case *types.ContentBlockMemberToolUse:
			encoded, err := value.Value.Input.MarshalSmithyDocument()
			if err != nil {
				return api.Result{}, fmt.Errorf("decode Bedrock tool input: %w", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, encoded); err != nil {
				return api.Result{}, fmt.Errorf("encode Bedrock tool input: %w", err)
			}
			result.ToolCalls = append(result.ToolCalls, api.ToolCall{ID: aws.ToString(value.Value.ToolUseId), Type: "function", Function: api.ToolFunction{Name: aws.ToString(value.Value.Name), Arguments: compact.String()}})
		}
	}
	return result, nil
}
