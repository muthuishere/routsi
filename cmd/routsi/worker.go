package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// workerMessage mirrors internal/api.Message (role + raw content) for the
// worker CLI's own JSON decoding, without importing the api package.
type workerMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// workerJob mirrors internal/queue.Job's wire shape.
type workerJob struct {
	ID       string          `json:"id"`
	Messages []workerMessage `json:"messages"`
}

// workerBase normalizes a proxy base URL (drop a trailing slash).
func workerBase(proxy string) string { return strings.TrimRight(proxy, "/") }

// registerQueue POSTs /v1/workers/register and fails loud on a non-2xx
// response. Idempotent on the proxy side — safe to call every time a worker
// (re)starts.
func registerQueue(base, name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := http.Post(base+"/v1/workers/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot reach proxy %s: %w", base, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

// pollJob does one long-poll GET against /v1/workers/{name}/jobs?wait=.
// ok=false, err=nil means the wait window elapsed with no job (204) — the
// caller should just poll again. A non-2xx/204 response or transport error
// returns a non-nil err.
func pollJob(base, name string, wait int) (workerJob, bool, error) {
	url := fmt.Sprintf("%s/v1/workers/%s/jobs?wait=%d", base, name, wait)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return workerJob{}, false, err
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return workerJob{}, false, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNoContent {
		return workerJob{}, false, nil
	}
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r.Body)
		return workerJob{}, false, fmt.Errorf("poll failed (%d): %s", r.StatusCode, strings.TrimSpace(string(b)))
	}
	var job workerJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		return workerJob{}, false, fmt.Errorf("decode job: %w", err)
	}
	return job, true, nil
}

// answerJob POSTs the answer content to /v1/workers/{name}/jobs/{id}.
func answerJob(base, name, id, content string) error {
	body, _ := json.Marshal(map[string]string{"content": content})
	resp, err := http.Post(fmt.Sprintf("%s/v1/workers/%s/jobs/%s", base, name, id), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot reach proxy %s: %w", base, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answer failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

// runWorker is the pull-worker loop (ADR-001/005): register a queue once, then
// long-poll for questions, pipe each to the local agent command on stdin, and
// post its stdout back as the answer. Fails loud and exits non-zero on a
// non-2xx register (e.g. name collision). No worker auth in v1 (--token
// accepted but ignored).
func runWorker() {
	fs := flag.NewFlagSet("worker run", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "routsi proxy base URL")
	name := fs.String("queue", "", "queue name to register/serve (required)")
	agent := fs.String("agent", "", "agent command; receives the question on stdin, prints the answer (required)")
	wait := fs.Int("wait", 25, "long-poll seconds")
	_ = fs.String("token", "", "reserved (worker auth not enabled yet)")
	_ = fs.Parse(os.Args[2:]) // args after "worker run"

	if *name == "" || *agent == "" {
		fmt.Fprintln(os.Stderr, "usage: routsi worker run --proxy URL --queue NAME --agent 'cmd'")
		os.Exit(2)
	}
	base := workerBase(*proxy)

	if err := registerQueue(base, *name); err != nil {
		log.Fatalf("register: %v", err)
	}
	fmt.Printf("registered ✓  queue=%q on %s — serving with: %s\n", *name, base, *agent)

	for {
		job, ok, err := pollJob(base, *name, *wait)
		if err != nil {
			fmt.Fprintf(os.Stderr, "poll error (retrying in 2s): %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if !ok {
			continue // idle heartbeat
		}

		start := time.Now()
		answer, aerr := runAgent(*agent, promptFrom(job.Messages))
		if aerr != nil {
			answer = "agent error: " + aerr.Error()
		}
		if err := answerJob(base, *name, job.ID, answer); err != nil {
			fmt.Fprintf(os.Stderr, "post answer: %v\n", err)
			continue
		}
		fmt.Printf("answered job %s (%s)\n", job.ID, time.Since(start).Round(time.Millisecond))
	}
}

// workerRegisterCmd handles `routsi worker register --proxy URL --queue NAME`
// — a one-shot register, useful for an agent session driving the poll/answer
// loop itself instead of shelling to `worker run`.
func workerRegisterCmd() {
	fs := flag.NewFlagSet("worker register", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "routsi proxy base URL")
	name := fs.String("queue", "", "queue name to register (required)")
	_ = fs.Parse(os.Args[2:])

	if *name == "" {
		fmt.Fprintln(os.Stderr, "usage: routsi worker register --proxy URL --queue NAME")
		os.Exit(2)
	}
	base := workerBase(*proxy)
	if err := registerQueue(base, *name); err != nil {
		fmt.Fprintln(os.Stderr, "register:", err)
		os.Exit(1)
	}
	fmt.Printf("registered ✓  queue=%q on %s\n", *name, base)
}

// workerPollCmd handles `routsi worker poll --proxy URL --queue NAME [--wait 25]`.
// On a job it prints one JSON line {"id","prompt","messages"} to stdout and
// exits 0. On an idle wait window it prints nothing and exits 0, so a caller
// loop (an agent session) just polls again. Network/HTTP errors exit non-zero.
func workerPollCmd() {
	fs := flag.NewFlagSet("worker poll", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "routsi proxy base URL")
	name := fs.String("queue", "", "queue name to poll (required)")
	wait := fs.Int("wait", 25, "long-poll seconds")
	_ = fs.Parse(os.Args[2:])

	if *name == "" {
		fmt.Fprintln(os.Stderr, "usage: routsi worker poll --proxy URL --queue NAME [--wait 25]")
		os.Exit(2)
	}
	base := workerBase(*proxy)
	job, ok, err := pollJob(base, *name, *wait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "poll:", err)
		os.Exit(1)
	}
	if !ok {
		return // idle: nothing printed, exit 0
	}
	out := struct {
		ID       string          `json:"id"`
		Prompt   string          `json:"prompt"`
		Messages []workerMessage `json:"messages"`
	}{ID: job.ID, Prompt: promptFrom(job.Messages), Messages: job.Messages}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

// workerAnswerCmd handles `routsi worker answer --proxy URL --queue NAME --id
// JOBID [--text '...']`. Reads the answer from --text if given, else from
// stdin (the whole of it). Non-zero exit + stderr on failure.
func workerAnswerCmd() {
	fs := flag.NewFlagSet("worker answer", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "routsi proxy base URL")
	name := fs.String("queue", "", "queue name (required)")
	id := fs.String("id", "", "job id to answer (required)")
	text := fs.String("text", "", "answer text (alternative to piping it on stdin)")
	_ = fs.Parse(os.Args[2:])

	if *name == "" || *id == "" {
		fmt.Fprintln(os.Stderr, "usage: routsi worker answer --proxy URL --queue NAME --id JOBID [--text '...']  (or pipe the answer on stdin)")
		os.Exit(2)
	}
	content := *text
	if content == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "answer: reading stdin:", err)
			os.Exit(1)
		}
		content = string(b)
	}
	base := workerBase(*proxy)
	if err := answerJob(base, *name, *id, content); err != nil {
		fmt.Fprintln(os.Stderr, "answer:", err)
		os.Exit(1)
	}
	fmt.Printf("answered job %s\n", *id)
}

// promptFrom renders the last user message text (agents want a prompt, not raw
// JSON). Falls back to concatenating all message text.
func promptFrom(msgs []workerMessage) string {
	text := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				b.WriteString(p.Text)
			}
			return b.String()
		}
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return text(msgs[i].Content)
		}
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(text(m.Content))
		b.WriteString("\n")
	}
	return b.String()
}

// runAgent runs `sh -c <cmd>` with the prompt on stdin, returns stdout.
func runAgent(cmd, prompt string) (string, error) {
	c := exec.CommandContext(context.Background(), "sh", "-c", cmd)
	c.Stdin = strings.NewReader(prompt)
	var out, errb bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// scaffoldWorker prints an editable pure-curl worker loop for those who'd
// rather not use the built-in command.
func scaffoldWorker() {
	io.WriteString(os.Stdout, `#!/bin/sh
# routsi worker (editable). Needs: curl, and an agent command that reads the
# question on stdin and prints the answer. No worker auth in v1.
PROXY="${PROXY:-http://localhost:8080}"
QUEUE="${QUEUE:-my-agent}"
AGENT="${AGENT:-cat}"   # e.g. 'codex exec --skip-git-repo-check -' or 'claude -p'

curl -sS -X POST "$PROXY/v1/workers/register" -H 'content-type: application/json' \
  -d "{\"name\":\"$QUEUE\"}" >/dev/null || { echo "register failed"; exit 1; }
echo "registered $QUEUE on $PROXY"
while :; do
  JOB=$(curl -sS "$PROXY/v1/workers/$QUEUE/jobs?wait=25")
  [ -z "$JOB" ] && continue
  ID=$(printf '%s' "$JOB"    | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  [ -z "$ID" ] && continue
  Q=$(printf '%s' "$JOB" | sed -n 's/.*"content":"\([^"]*\)".*/\1/p')
  A=$(printf '%s' "$Q" | $AGENT)
  curl -sS -X POST "$PROXY/v1/workers/$QUEUE/jobs/$ID" -H 'content-type: application/json' \
    --data "$(printf '{"content":%s}' "$(printf '%s' "$A" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")" >/dev/null
  echo "answered $ID"
done
`)
}
