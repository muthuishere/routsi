package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/adapters"
	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// Command is the exec transport of the adapter contract (ADR-013): routsi runs
// a user-supplied shell command per request, writes the job to its stdin, and
// reads the answer off its stdout. The adapter is the user's file — core
// carries no vendor-specific code.
//
// Streaming is buffered (Stream emits the finished answer), as with the CLI
// agents; the SSE heartbeat covers long silences.
type Command struct {
	model *config.Model
	// Shell is the interpreter used to run Command (default "sh"). Exposed so
	// tests and embedders can substitute one; never a package-level default.
	Shell string
}

func NewCommand(m *config.Model) *Command {
	return &Command{model: m, Shell: "sh"}
}

// adapterJob is what the adapter reads on stdin. Field names match
// queue.Job (internal/queue) so exec, socket and queue transports carry one
// schema; prompt/upstream_model/stream are the exec additions.
type adapterJob struct {
	ID             string          `json:"id"`
	Model          string          `json:"model"`
	UpstreamModel  string          `json:"upstream_model,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Stream         bool            `json:"stream"`
	Prompt         string          `json:"prompt"`
	Messages       []api.Message   `json:"messages"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
}

func (c *Command) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	res, err := c.CompleteResult(ctx, req)
	return res.Content, err
}

func (c *Command) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := c.Complete(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}

// CompleteResult runs the adapter and normalizes its answer (ResultBackend).
func (c *Command) CompleteResult(ctx context.Context, req *api.ChatRequest) (api.Result, error) {
	mode := c.model.ToolMode
	if mode == "" {
		mode = config.ToolsNative
	}
	if len(req.Tools) > 0 && mode == config.ToolsOff {
		return api.Result{}, ErrToolsUnsupported
	}

	job := adapterJob{
		ID:             newJobID(),
		Model:          c.model.Name,
		UpstreamModel:  c.model.UpstreamModel,
		ConversationID: req.ConversationID,
		Stream:         req.Stream,
		Prompt:         basePrompt(req),
		Messages:       req.Messages,
		ToolChoice:     req.ToolChoice,
	}

	emulating := len(req.Tools) > 0 && mode == config.ToolsEmulated
	if emulating {
		// The adapter is a plain text-in/text-out process: fold the manifest
		// into the prompt and parse the reply ourselves (ADR-011 Phase A).
		job.Prompt = buildToolPrompt(job.Prompt, req.Tools)
	} else if len(req.Tools) > 0 {
		job.Tools = req.Tools
	}

	out, err := c.run(ctx, job)
	if err != nil {
		return api.Result{}, err
	}
	if emulating {
		return parseToolReply(out), nil
	}
	// A non-JSON stdout is the answer text — that is what makes a one-line
	// adapter (`echo hi`) valid.
	if res, ok := api.ParseAnswer([]byte(out), job.ID); ok {
		return res, nil
	}
	return api.Result{Content: out}, nil
}

func (c *Command) run(ctx context.Context, job adapterJob) (string, error) {
	payload, err := json.Marshal(job)
	if err != nil {
		return "", fmt.Errorf("adapter %s: encode job: %w", c.model.Name, err)
	}

	timeout := c.model.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := c.Shell
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", c.model.Command)
	cmd.Dir = ensureWorkdir(c.model)
	cmd.Stdin = bytes.NewReader(payload)
	// Convenience for adapters that would rather not parse the job at all.
	// ROUTSI_ADAPTERS lets models.yaml reference a shipped template without
	// hardcoding a home path: `command: node $ROUTSI_ADAPTERS/cli.js`.
	cmd.Env = append(os.Environ(),
		"ROUTSI_ADAPTERS="+adapters.Dir(),
		"ROUTSI_MODEL="+job.Model,
		"ROUTSI_UPSTREAM_MODEL="+job.UpstreamModel,
		"ROUTSI_CONVERSATION_ID="+job.ConversationID,
		"ROUTSI_JOB_ID="+job.ID,
		"ROUTSI_STREAM="+strconv.FormatBool(job.Stream),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("adapter %s: timed out after %s", c.model.Name, timeout)
		}
		// stderr is the adapter author's debugging channel; surface it, capped
		// so a chatty adapter can't flood the response.
		return "", fmt.Errorf("adapter %s: %w: %s", c.model.Name, err, truncate(strings.TrimSpace(stderr.String()), 500))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newJobID() string {
	return "cmd_" + api.NewID()
}
