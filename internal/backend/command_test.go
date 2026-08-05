package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

func cmdModel(t *testing.T, command string, mode config.ToolMode) *config.Model {
	t.Helper()
	return &config.Model{
		Name:     "adapter-1",
		Type:     config.TypeCommand,
		Command:  command,
		Workdir:  t.TempDir(),
		Timeout:  10 * time.Second,
		ToolMode: mode,
	}
}

func chatReq(t *testing.T, text string, tools string) *api.ChatRequest {
	t.Helper()
	content, _ := json.Marshal(text)
	req := &api.ChatRequest{
		Model:    "adapter-1",
		Messages: []api.Message{{Role: "user", Content: content}},
	}
	if tools != "" {
		req.Tools = json.RawMessage(tools)
	}
	return req
}

const weatherTools = `[{"type":"function","function":{"name":"get_weather",` +
	`"description":"current weather","parameters":{"type":"object",` +
	`"properties":{"city":{"type":"string"}},"required":["city"]}}}]`

func TestCommandPlainTextAnswer(t *testing.T) {
	// The simplest possible adapter: ignore the job, print text. Non-JSON
	// stdout must be taken verbatim as the answer.
	c := NewCommand(cmdModel(t, `echo "hello from adapter"`, ""))
	res, err := c.CompleteResult(context.Background(), chatReq(t, "hi", ""))
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}
	if res.Content != "hello from adapter" {
		t.Errorf("content = %q, want %q", res.Content, "hello from adapter")
	}
	if len(res.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", res.ToolCalls)
	}
}

func TestCommandReceivesJobOnStdin(t *testing.T) {
	// The adapter echoes back fields it read from the job, proving the wire
	// shape: prompt, model, conversation_id and the tools passthrough.
	script := `cat > job.json; ` +
		`printf '{"content":"%s|%s|%s"}' ` +
		`"$(grep -o '"prompt":"[^"]*"' job.json | cut -d: -f2 | tr -d '"')" ` +
		`"$ROUTSI_MODEL" "$ROUTSI_CONVERSATION_ID"`
	c := NewCommand(cmdModel(t, script, ""))
	req := chatReq(t, "what is the weather", "")
	req.ConversationID = "conv-42"

	res, err := c.CompleteResult(context.Background(), req)
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}
	parts := strings.Split(res.Content, "|")
	if len(parts) != 3 {
		t.Fatalf("content = %q, want 3 |-separated fields", res.Content)
	}
	if parts[0] != "what is the weather" {
		t.Errorf("prompt = %q, want %q", parts[0], "what is the weather")
	}
	if parts[1] != "adapter-1" {
		t.Errorf("ROUTSI_MODEL = %q, want adapter-1", parts[1])
	}
	if parts[2] != "conv-42" {
		t.Errorf("ROUTSI_CONVERSATION_ID = %q, want conv-42", parts[2])
	}
}

func TestCommandToolCallShapes(t *testing.T) {
	// Both answer shapes must normalize to the same OpenAI wire result.
	tests := []struct {
		name    string
		stdout  string
		wantArg string
	}{
		{
			name:    "simplified shape",
			stdout:  `{"tool_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`,
			wantArg: `{"city":"Paris"}`,
		},
		{
			name:    "openai shape",
			stdout:  `{"tool_calls":[{"id":"call_x","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}`,
			wantArg: `{"city":"Paris"}`,
		},
		{
			name:    "arguments already a json string",
			stdout:  `{"tool_calls":[{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]}`,
			wantArg: `{"city":"Paris"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCommand(cmdModel(t, "cat >/dev/null; printf '%s' '"+tc.stdout+"'", ""))
			res, err := c.CompleteResult(context.Background(), chatReq(t, "weather?", weatherTools))
			if err != nil {
				t.Fatalf("CompleteResult: %v", err)
			}
			if len(res.ToolCalls) != 1 {
				t.Fatalf("got %d tool calls, want 1", len(res.ToolCalls))
			}
			call := res.ToolCalls[0]
			if call.Function.Name != "get_weather" {
				t.Errorf("name = %q, want get_weather", call.Function.Name)
			}
			if call.Function.Arguments != tc.wantArg {
				t.Errorf("arguments = %q, want %q", call.Function.Arguments, tc.wantArg)
			}
			if call.Type != "function" {
				t.Errorf("type = %q, want function", call.Type)
			}
			if call.ID == "" {
				t.Error("tool call id must be synthesized when the adapter omits it")
			}
		})
	}
}

func TestCommandToolsOffRefuses(t *testing.T) {
	// Never silently drop tools (ADR-008): the request must fail loudly.
	c := NewCommand(cmdModel(t, `echo hi`, config.ToolsOff))
	_, err := c.CompleteResult(context.Background(), chatReq(t, "weather?", weatherTools))
	if err != ErrToolsUnsupported {
		t.Fatalf("err = %v, want ErrToolsUnsupported", err)
	}
	// Without tools the same model answers normally.
	res, err := c.CompleteResult(context.Background(), chatReq(t, "hi", ""))
	if err != nil || res.Content != "hi" {
		t.Fatalf("no-tools path: res=%+v err=%v", res, err)
	}
}

func TestCommandToolsEmulatedFoldsManifestIntoPrompt(t *testing.T) {
	// In emulated mode the adapter is text-in/text-out: it must see the tool
	// manifest inside the prompt, and its fenced reply is parsed by routsi.
	script := `cat > job.json; ` +
		`if grep -q get_weather job.json; then ` +
		`printf '` + "```json\\n" + `{"kind":"tool_calls","tool_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}\n` + "```" + `'; ` +
		`else echo "no manifest"; fi`
	c := NewCommand(cmdModel(t, script, config.ToolsEmulated))
	res, err := c.CompleteResult(context.Background(), chatReq(t, "weather in Paris?", weatherTools))
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("emulated tool call not parsed: %+v (content=%q)", res.ToolCalls, res.Content)
	}
}

func TestCommandFailureSurfacesStderr(t *testing.T) {
	c := NewCommand(cmdModel(t, `echo "boom detail" >&2; exit 3`, ""))
	_, err := c.CompleteResult(context.Background(), chatReq(t, "hi", ""))
	if err == nil {
		t.Fatal("want an error from a non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom detail") {
		t.Errorf("error %q should carry the adapter's stderr", err)
	}
	if !strings.Contains(err.Error(), "adapter-1") {
		t.Errorf("error %q should name the model", err)
	}
}

func TestCommandTimeout(t *testing.T) {
	m := cmdModel(t, `sleep 5`, "")
	m.Timeout = 150 * time.Millisecond
	c := NewCommand(m)
	start := time.Now()
	_, err := c.CompleteResult(context.Background(), chatReq(t, "hi", ""))
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took %v — the deadline is not being enforced", elapsed)
	}
}

func TestCommandStreamEmitsWholeAnswer(t *testing.T) {
	c := NewCommand(cmdModel(t, `echo streamed`, ""))
	var got []string
	err := c.Stream(context.Background(), chatReq(t, "hi", ""), func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(got) != 1 || got[0] != "streamed" {
		t.Errorf("deltas = %#v, want [streamed]", got)
	}
}
