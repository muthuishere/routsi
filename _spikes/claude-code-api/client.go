package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const messagesURL = "https://api.anthropic.com/v1/messages?beta=true"

type CredentialSource interface {
	Load(context.Context) (OAuthCredential, error)
	Refresh(context.Context) (OAuthCredential, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MessagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type CanaryResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status"`
	Model      string `json:"model,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	TextBytes  int    `json:"text_bytes"`
	Usage      Usage  `json:"usage"`
}

type Client struct {
	HTTP    *http.Client
	Auth    CredentialSource
	Version string
	URL     string
}

func (c *Client) Canary(ctx context.Context, req MessagesRequest, marker string) (CanaryResult, error) {
	cred, err := c.Auth.Load(ctx)
	if err != nil {
		return CanaryResult{}, err
	}
	result, status, err := c.do(ctx, req, marker, cred.AccessToken)
	if status == http.StatusUnauthorized {
		fresh, refreshErr := c.Auth.Refresh(ctx)
		if refreshErr != nil {
			return result, fmt.Errorf("%w: refresh unavailable after HTTP 401", ErrAuthRequired)
		}
		result, _, err = c.do(ctx, req, marker, fresh.AccessToken)
	}
	return result, err
}

func (c *Client) do(ctx context.Context, payload MessagesRequest, marker, accessToken string) (CanaryResult, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return CanaryResult{}, 0, errors.New("encode request")
	}
	endpoint := c.URL
	if endpoint == "" {
		endpoint = messagesURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CanaryResult{}, 0, errors.New("build request")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	req.Header.Set("X-App", "cli")
	if c.Version != "" {
		req.Header.Set("User-Agent", "claude-cli/"+c.Version+" (external, cli)")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return CanaryResult{}, 0, errors.New("claude transport failed")
	}
	defer resp.Body.Close()
	result := CanaryResult{HTTPStatus: resp.StatusCode, RequestID: resp.Header.Get("request-id")}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusUnauthorized {
			return result, resp.StatusCode, ErrAuthRequired
		}
		return result, resp.StatusCode, fmt.Errorf("claude returned HTTP %d", resp.StatusCode)
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])); mediaType != "text/event-stream" {
		return result, resp.StatusCode, errors.New("Claude response was not an SSE stream")
	}
	if err := consumeSSE(resp.Body, marker, &result); err != nil {
		return result, resp.StatusCode, err
	}
	return result, resp.StatusCode, nil
}

type streamFrame struct {
	Type    string `json:"type"`
	Message *struct {
		Model string `json:"model"`
		Usage Usage  `json:"usage"`
	} `json:"message,omitempty"`
	Delta *struct {
		Type       string `json:"type,omitempty"`
		Text       string `json:"text,omitempty"`
		StopReason string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Usage *Usage `json:"usage,omitempty"`
	Error *struct {
		Type string `json:"type"`
	} `json:"error,omitempty"`
}

func consumeSSE(r io.Reader, marker string, out *CanaryResult) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var text strings.Builder
	started, stopped := false, false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var frame streamFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			return errors.New("invalid Claude SSE frame")
		}
		switch frame.Type {
		case "message_start":
			if started || stopped {
				return errors.New("invalid Claude SSE frame order")
			}
			started = true
			if frame.Message != nil {
				out.Model = frame.Message.Model
				out.Usage = frame.Message.Usage
			}
		case "content_block_delta":
			if !started || stopped {
				return errors.New("invalid Claude SSE frame order")
			}
			if frame.Delta != nil && frame.Delta.Type == "text_delta" {
				text.WriteString(frame.Delta.Text)
			}
		case "message_delta":
			if !started || stopped {
				return errors.New("invalid Claude SSE frame order")
			}
			if frame.Delta != nil {
				out.StopReason = frame.Delta.StopReason
			}
			if frame.Usage != nil {
				out.Usage.OutputTokens = frame.Usage.OutputTokens
			}
		case "error":
			kind := "unknown"
			if frame.Error != nil && frame.Error.Type != "" {
				kind = frame.Error.Type
			}
			return fmt.Errorf("Claude stream error: %s", kind)
		case "message_stop":
			if !started || stopped {
				return errors.New("invalid Claude SSE frame order")
			}
			stopped = true
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("Claude stream read failed")
	}
	if !started || !stopped {
		return errors.New("Claude SSE stream ended before message_stop")
	}
	out.TextBytes = text.Len()
	out.OK = marker != "" && strings.Contains(strings.TrimSpace(text.String()), marker)
	return nil
}
