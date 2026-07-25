package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/api"
)

// externalRequest is the JSON routsi writes to the decider's stdin. Messages
// are passed as-is (current turn's worth, same as Rules sees via
// LastUserText) — never the full accumulated history unless the caller
// already has it in req.Messages.
type externalRequest struct {
	Model          string            `json:"model"`
	ConversationID string            `json:"conversation_id"`
	Messages       []api.Message     `json:"messages"`
	Levels         []string          `json:"levels"`
	Tiers          map[string]string `json:"tiers,omitempty"`
}

// externalResponse is the JSON the decider must print to stdout. Level is the
// primary contract — it keeps a clean swap-in for Router.Pick. A future
// extension could also honor {"model": "..."} for naming a concrete model
// directly, but that would require server.resolve to special-case the
// decider's output rather than just consuming a level; skipped for v1 to
// keep this a drop-in Router (see docs/adr/007).
type externalResponse struct {
	Level string `json:"level"`
}

// External shells out to a user-supplied command for each `auto` request,
// writing an externalRequest to its stdin and reading an externalResponse
// from stdout. Any failure (spawn error, timeout, non-zero exit, malformed
// output, unknown level) falls back to Fallback's decision — the proxy must
// never fail a request because a decider misbehaves.
type External struct {
	Command  string
	Timeout  time.Duration
	Cwd      string
	Tiers    map[string]string // resolved auto tier names, echoed to the decider for context
	Fallback Router
}

// NewExternal builds an External decider. timeout <= 0 defaults to 3s.
func NewExternal(command string, timeout time.Duration, cwd string, tiers map[string]string, fallback Router) *External {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if fallback == nil {
		fallback = NewRules()
	}
	return &External{Command: command, Timeout: timeout, Cwd: cwd, Tiers: tiers, Fallback: fallback}
}

func (e *External) Pick(req *api.ChatRequest) string {
	level, err := e.pick(req)
	if err != nil {
		log.Printf("router: external decider failed, falling back to rules: %v", err)
		return e.Fallback.Pick(req)
	}
	return level
}

func (e *External) pick(req *api.ChatRequest) (string, error) {
	in := externalRequest{
		Model:          req.Model,
		ConversationID: req.ConversationID,
		Messages:       req.Messages,
		Levels:         []string{LevelLow, LevelMedium, LevelHigh},
		Tiers:          e.Tiers,
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", e.Command)
	cmd.Dir = e.Cwd
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", errTimeout
		}
		return "", err
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", errEmptyOutput
	}
	var resp externalResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	switch resp.Level {
	case LevelLow, LevelMedium, LevelHigh:
		return resp.Level, nil
	default:
		return "", errUnknownLevel
	}
}

var (
	errTimeout      = errString("decider timed out")
	errEmptyOutput  = errString("decider produced empty output")
	errUnknownLevel = errString("decider returned an unknown level")
)

type errString string

func (e errString) Error() string { return string(e) }
