package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// fakeAgent mimics codex ("exec" with --output-last-message) and copilot
// ("-p"), logging args.
func fakeAgent(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
dir="$(dirname "$0")"
echo "$@" >> "$dir/calls.log"
out=""
prompt=""
last=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output-last-message) out="$2"; shift 2 ;;
    -p) prompt="$2"; shift 2 ;;
    -m|--model) shift 2 ;;
    exec|--skip-git-repo-check|--log-level|none|--no-color) shift ;;
    *) last="$1"; shift ;;
  esac
done
[ -z "$prompt" ] && prompt="$last"
if [ -n "$out" ]; then echo "codex-answer: $prompt" > "$out"; echo "banner noise"; else echo "copilot-answer: $prompt"; fi
`
	bin = filepath.Join(dir, "agent")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func agentReq(text string) *api.ChatRequest {
	c, _ := json.Marshal(text)
	return &api.ChatRequest{Messages: []api.Message{{Role: "user", Content: c}}}
}

func TestCodexUsesOutputLastMessage(t *testing.T) {
	bin, dir := fakeAgent(t)
	a := NewCLIAgent(&config.Model{Name: "codex", Type: config.TypeCodex, DevinBin: bin, Workdir: dir, UpstreamModel: "gpt-5-codex"})
	out, err := a.Complete(context.Background(), agentReq("write a haiku"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "codex-answer: write a haiku" {
		t.Fatalf("out = %q (banner must not leak)", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	for _, want := range []string{"exec", "--skip-git-repo-check", "-m gpt-5-codex"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("missing %q in call: %s", want, calls)
		}
	}
}

func TestCopilotPrintMode(t *testing.T) {
	bin, dir := fakeAgent(t)
	a := NewCLIAgent(&config.Model{Name: "copilot", Type: config.TypeCopilot, DevinBin: bin, Workdir: dir, UpstreamModel: "claude-sonnet-5"})
	out, err := a.Complete(context.Background(), agentReq("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "copilot-answer: hello" {
		t.Fatalf("out = %q", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "--model claude-sonnet-5") {
		t.Fatalf("model flag missing: %s", calls)
	}
}

func TestCLIAgentRendersHistory(t *testing.T) {
	bin, dir := fakeAgent(t)
	a := NewCLIAgent(&config.Model{Name: "copilot", Type: config.TypeCopilot, DevinBin: bin, Workdir: dir})
	u1, _ := json.Marshal("q1")
	a1, _ := json.Marshal("ans1")
	u2, _ := json.Marshal("q2")
	req := &api.ChatRequest{Messages: []api.Message{
		{Role: "user", Content: u1}, {Role: "assistant", Content: a1}, {Role: "user", Content: u2},
	}}
	out, err := a.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"q1", "ans1", "q2", "Prior conversation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing %q: %s", want, out)
		}
	}
}

func TestClaudePrintMode(t *testing.T) {
	bin, dir := fakeAgent(t)
	a := NewCLIAgent(&config.Model{Name: "claude", Type: config.TypeClaude, DevinBin: bin, Workdir: dir, UpstreamModel: "opus"})
	out, err := a.Complete(context.Background(), agentReq("hello claude"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "copilot-answer: hello claude" { // fake script answers via -p path
		t.Fatalf("out = %q", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "--model opus") {
		t.Fatalf("model flag missing: %s", calls)
	}
}
