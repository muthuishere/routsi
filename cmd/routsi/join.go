package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// `routsi worker join` — serve a queue with a LONG-LIVED INTERACTIVE AGENT
// (devin/claude/codex TUI or anything else) via file handoff, the pattern
// proven in docs/spikes/006: each job (conversation + client tool schemas) is
// written to a job file in --workdir, a user-supplied --notify command tells
// the agent to go read it, and the agent delivers by writing the answer file.
// No prompt-size ceiling (nothing rides argv into the agent), warm sessions,
// and tool calls both ways. The notify command receives the paths in env:
// ROUTSI_JOB_ID, ROUTSI_JOB_FILE, ROUTSI_ANSWER_FILE. It is re-run every
// --nudge while the answer file hasn't appeared (idempotent retry — free-plan
// agents throttle), until --job-timeout.

type joinMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type joinJob struct {
	ID       string          `json:"id"`
	Messages []joinMessage   `json:"messages"`
	Tools    json.RawMessage `json:"tools,omitempty"`
}

func workerJoinCmd() {
	fs := flag.NewFlagSet("worker join", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "routsi proxy base URL")
	name := fs.String("queue", "", "queue name to register/serve (required)")
	workdir := fs.String("workdir", ".", "directory for job-<id>.md / answer-<id>.json handoff files")
	notify := fs.String("notify", "", "command run per job (sh -c) to tell the agent about the job file (required); gets ROUTSI_JOB_ID/ROUTSI_JOB_FILE/ROUTSI_ANSWER_FILE in env")
	wait := fs.Int("wait", 25, "long-poll seconds")
	nudge := fs.Duration("nudge", 45*time.Second, "re-run notify while the answer file is missing")
	jobTimeout := fs.Duration("job-timeout", 8*time.Minute, "give up on a job after this long")
	_ = fs.Parse(os.Args[2:])

	if *name == "" || *notify == "" {
		fmt.Fprintln(os.Stderr, "usage: routsi worker join --proxy URL --queue NAME --workdir DIR --notify 'cmd'")
		fmt.Fprintln(os.Stderr, "example --notify (tmux pane running an agent TUI):")
		fmt.Fprintln(os.Stderr, `  --notify 'tmux send-keys -t devin "Read $ROUTSI_JOB_FILE and follow its HOW TO ANSWER section." Enter'`)
		os.Exit(2)
	}
	base := workerBase(*proxy)
	dir, err := filepath.Abs(*workdir)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		log.Fatalf("workdir: %v", err)
	}
	if err := registerQueue(base, *name); err != nil {
		log.Fatalf("%v", err)
	}
	log.Printf("joined ✓ queue=%q workdir=%s — waiting for jobs", *name, dir)

	for {
		raw, ok, err := joinPoll(base, *name, *wait)
		if err != nil {
			log.Printf("poll: %v (retrying in 5s)", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if !ok {
			continue
		}
		var job joinJob
		if err := json.Unmarshal(raw, &job); err != nil || job.ID == "" {
			log.Printf("bad job payload: %v", err)
			continue
		}
		serveJoinJob(base, *name, dir, *notify, job, *nudge, *jobTimeout)
	}
}

// joinPoll long-polls and returns the raw job JSON (so unmodeled fields
// survive). ok=false means the wait window elapsed idle.
func joinPoll(base, name string, wait int) ([]byte, bool, error) {
	url := fmt.Sprintf("%s/v1/workers/%s/jobs?wait=%d", base, name, wait)
	resp, err := http.Get(url)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return body, true, nil
	case http.StatusNoContent:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("poll failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func serveJoinJob(base, name, dir, notify string, job joinJob, nudge, timeout time.Duration) {
	jobFile := filepath.Join(dir, "job-"+job.ID+".md")
	answerFile := filepath.Join(dir, "answer-"+job.ID+".json")
	if err := os.WriteFile(jobFile, []byte(renderJobFile(job, answerFile)), 0o644); err != nil {
		log.Printf("job %s: write job file: %v", job.ID, err)
		return
	}
	log.Printf("job %s: %d message(s)%s -> %s", job.ID, len(job.Messages),
		map[bool]string{true: " + tools", false: ""}[len(job.Tools) > 0], filepath.Base(jobFile))

	runNotify := func() {
		cmd := exec.Command("sh", "-c", notify)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"ROUTSI_JOB_ID="+job.ID, "ROUTSI_JOB_FILE="+jobFile, "ROUTSI_ANSWER_FILE="+answerFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("job %s: notify: %v: %s", job.ID, err, strings.TrimSpace(string(out)))
		}
	}
	runNotify()

	deadline := time.Now().Add(timeout)
	lastNudge := time.Now()
	for {
		if _, err := os.Stat(answerFile); err == nil {
			time.Sleep(2 * time.Second) // let the agent finish writing
			break
		}
		if time.Now().After(deadline) {
			log.Printf("job %s: no answer file after %s — giving up", job.ID, timeout)
			return
		}
		if time.Since(lastNudge) >= nudge {
			runNotify()
			lastNudge = time.Now()
		}
		time.Sleep(2 * time.Second)
	}

	raw, err := os.ReadFile(answerFile)
	if err != nil {
		log.Printf("job %s: read answer: %v", job.ID, err)
		return
	}
	body := normalizeJoinAnswer(raw)
	resp, err := http.Post(fmt.Sprintf("%s/v1/workers/%s/jobs/%s", base, name, job.ID),
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("job %s: answer post: %v", job.ID, err)
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("job %s: answer rejected (%d): %s", job.ID, resp.StatusCode, strings.TrimSpace(string(rb)))
		return
	}
	log.Printf("job %s: answered (%d bytes)", job.ID, len(body))
}

// normalizeJoinAnswer passes a valid {"content"|"tool_calls":...} object
// through untouched; anything else is wrapped as plain content.
func normalizeJoinAnswer(raw []byte) []byte {
	var probe struct {
		Content   *string         `json:"content"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	trimmed := strings.TrimSpace(string(raw))
	if json.Unmarshal([]byte(trimmed), &probe) == nil &&
		(probe.Content != nil || len(probe.ToolCalls) > 0) {
		return []byte(trimmed)
	}
	wrapped, _ := json.Marshal(map[string]string{"content": trimmed})
	return wrapped
}

// renderJobFile writes the job in the format proven in spike 006: role of the
// worker, the client's tool schemas, the conversation, and the delivery
// contract (write ONE answer file, JSON, content or tool_calls).
func renderJobFile(job joinJob, answerFile string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Job %s\n\n", job.ID)
	b.WriteString("You are acting as the LLM brain for ANOTHER agent/client. The client executes tools on its side; you only DECIDE. Do NOT do the task with your own tools; do NOT create the client's files yourself.\n\n")
	if len(job.Tools) > 0 {
		b.WriteString("## AVAILABLE TOOLS (the client executes these, you may call them)\n\n```json\n")
		b.Write(job.Tools)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("## CONVERSATION\n\n")
	for _, m := range job.Messages {
		content := ""
		var asStr string
		if json.Unmarshal(m.Content, &asStr) == nil {
			content = asStr
		} else if len(m.Content) > 0 {
			content = string(m.Content)
		}
		label := m.Role
		if m.ToolCallID != "" {
			label += " (result of " + m.ToolCallID + ")"
		}
		fmt.Fprintf(&b, "### %s\n%s\n", label, content)
		if len(m.ToolCalls) > 0 {
			fmt.Fprintf(&b, "\n[assistant tool_calls]: %s\n", string(m.ToolCalls))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## HOW TO ANSWER\nWrite EXACTLY ONE file: %s\nIt must contain ONLY valid JSON, one of:\n"+
		"- {\"content\": \"final text reply to the user\"}\n"+
		"- {\"tool_calls\": [{\"name\": \"<tool from AVAILABLE TOOLS>\", \"arguments\": { ... }}]}\n"+
		"Use tool_calls whenever the request needs actions — the client runs them and the results arrive as your next job. NEVER guess values that must come from a call's result. Creating %s is your ONLY deliverable.\n",
		answerFile, answerFile)
	return b.String()
}
