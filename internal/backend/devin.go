package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// Devin answers by shelling out to the Devin CLI (`devin -p`). Conversation
// continuity is real: the first turn of a conversation creates a Devin
// session (found via `devin list --format json`, newest in our workdir) and
// later turns resume it with `devin -r <session> -p`. Streaming is faked off
// Complete — the CLI prints only the final answer.
type Devin struct {
	model   *config.Model
	workdir string

	mu       sync.Mutex
	sessions map[string]string // proxy conversation id -> devin session id
}

func NewDevin(m *config.Model) *Devin {
	return &Devin{model: m, workdir: ensureWorkdir(m), sessions: map[string]string{}}
}

func (d *Devin) bin() string {
	if d.model.DevinBin != "" {
		return d.model.DevinBin
	}
	return "devin"
}

func (d *Devin) baseArgs() []string {
	args := append([]string{}, d.model.Args...)
	if d.model.UpstreamModel != "" && d.model.UpstreamModel != d.model.Name {
		args = append(args, "--model", d.model.UpstreamModel)
	}
	if d.model.PermissionMode != "" {
		args = append(args, "--permission-mode", d.model.PermissionMode)
	}
	return args
}

func (d *Devin) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Dir = d.workdir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("devin %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// newestSession returns the most recently active session id in our workdir.
func (d *Devin) newestSession(ctx context.Context) (string, error) {
	out, err := d.run(ctx, "list", "--format", "json")
	if err != nil {
		return "", err
	}
	var sessions []struct {
		ID           string `json:"id"`
		LastActivity int64  `json:"last_activity_at"`
	}
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		return "", fmt.Errorf("devin list: %w", err)
	}
	best := ""
	var bestAt int64 = -1
	for _, s := range sessions {
		if s.LastActivity > bestAt {
			best, bestAt = s.ID, s.LastActivity
		}
	}
	if best == "" {
		return "", fmt.Errorf("devin list: no sessions found")
	}
	return best, nil
}

func (d *Devin) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	return d.complete(ctx, req, func(p string) string { return p })
}

// CompleteResult adds tool-call emulation (ADR-011 Phase A) — same protocol
// as CLIAgent; devin's session mapping is reused unchanged.
func (d *Devin) CompleteResult(ctx context.Context, req *api.ChatRequest) (api.Result, error) {
	if len(req.Tools) == 0 {
		text, err := d.Complete(ctx, req)
		return api.Result{Content: text}, err
	}
	text, err := d.complete(ctx, req, func(p string) string { return buildToolPrompt(p, req.Tools) })
	if err != nil {
		return api.Result{}, err
	}
	return parseToolReply(text), nil
}

func (d *Devin) complete(ctx context.Context, req *api.ChatRequest, wrap func(string) string) (string, error) {
	convID := req.ConversationID
	// Fingerprint ids exist for routing stickiness only. Two users opening
	// with the same first message share a fingerprint — mapping it to a
	// Devin session would resume one user's session for the other.
	if strings.HasPrefix(convID, "fp-") {
		convID = ""
	}
	d.mu.Lock()
	sid := d.sessions[convID]
	d.mu.Unlock()

	prompt := req.LastUserText()
	if sid != "" {
		args := append(d.baseArgs(), "-r", sid, "-p", wrap(prompt))
		return d.run(ctx, args...)
	}

	// New conversation. If the client already carries history (proxy restart,
	// fingerprint conversations), render it into the prompt so nothing is lost.
	if len(req.Messages) > 1 {
		prompt = renderTranscript(req)
	}
	args := append(d.baseArgs(), "-p", wrap(prompt))

	// Create-then-list must not interleave with another new conversation.
	d.mu.Lock()
	defer d.mu.Unlock()
	answer, err := d.run(ctx, args...)
	if err != nil {
		return "", err
	}
	if convID != "" {
		if newSid, lerr := d.newestSession(ctx); lerr == nil {
			d.sessions[convID] = newSid
		}
	}
	return answer, nil
}

func (d *Devin) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := d.Complete(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}

func renderTranscript(req *api.ChatRequest) string {
	var b strings.Builder
	b.WriteString("Prior conversation:\n")
	for i, m := range req.Messages {
		if i == len(req.Messages)-1 && m.Role == "user" {
			break
		}
		switch {
		case m.Role == "tool":
			fmt.Fprintf(&b, "tool result (%s): %s\n", m.ToolCallID, m.Text())
		case len(m.ToolCalls) > 0:
			fmt.Fprintf(&b, "%s tool_calls: %s\n", m.Role, string(m.ToolCalls))
		default:
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Text())
		}
	}
	fmt.Fprintf(&b, "\nAnswer the latest user message: %s", req.LastUserText())
	return b.String()
}
