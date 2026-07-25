package backend

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// CLIAgent answers via a local agent CLI in one-shot mode (codex, copilot).
// Unlike Devin there is no session mapping yet: multi-turn context is carried
// by rendering the transcript into the prompt, which works with the standard
// client-resends-history flow and with proxy conversations alike. Streaming
// is faked off Complete — these CLIs print only the final answer.
type CLIAgent struct {
	model   *config.Model
	workdir string
}

func NewCLIAgent(m *config.Model) *CLIAgent {
	return &CLIAgent{model: m, workdir: ensureWorkdir(m)}
}

// ensureWorkdir resolves the agent's working directory: configured, or a
// hidden per-model dir under the OS cache (we serve an LLM API — no
// user-facing folder concept).
func ensureWorkdir(m *config.Model) string {
	if m.Workdir != "" {
		return m.Workdir
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "routsi", string(m.Type), strings.ReplaceAll(m.Name, "/", "_"))
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (a *CLIAgent) bin() string {
	if a.model.DevinBin != "" {
		return a.model.DevinBin
	}
	return string(a.model.Type)
}

func (a *CLIAgent) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	prompt := req.LastUserText()
	if len(req.Messages) > 1 {
		prompt = renderTranscript(req)
	}

	var args []string
	outFile := ""
	switch a.model.Type {
	case config.TypeCodex:
		// --output-last-message gives the clean final answer without the
		// exec banner; --skip-git-repo-check because the workdir is a cache
		// dir, not a repo.
		outFile = filepath.Join(a.workdir, "last-message.txt")
		args = []string{"exec", "--skip-git-repo-check", "--output-last-message", outFile}
		if m := a.model.UpstreamModel; m != "" && m != a.model.Name {
			args = append(args, "-m", m)
		}
		args = append(args, prompt)
	case config.TypeCopilot:
		args = []string{"-p", prompt, "--log-level", "none", "--no-color"}
		if m := a.model.UpstreamModel; m != "" && m != a.model.Name {
			args = append(args, "--model", m)
		}
	case config.TypeClaude:
		args = []string{"-p", prompt}
		if m := a.model.UpstreamModel; m != "" && m != a.model.Name {
			args = append(args, "--model", m)
		}
	default:
		return "", fmt.Errorf("cliagent: unsupported type %q", a.model.Type)
	}

	if len(a.model.Args) > 0 {
		args = append(append([]string{}, a.model.Args...), args...)
	}
	cmd := exec.CommandContext(ctx, a.bin(), args...)
	cmd.Dir = a.workdir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", a.model.Type, err, strings.TrimSpace(errb.String()))
	}
	if outFile != "" {
		if b, err := os.ReadFile(outFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(bytes.TrimSpace(b)), nil
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func (a *CLIAgent) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := a.Complete(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}
