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
	base := strings.TrimRight(*proxy, "/")

	// Register (fail loud).
	body, _ := json.Marshal(map[string]string{"name": *name})
	resp, err := http.Post(base+"/v1/workers/register", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("register: cannot reach proxy %s: %v", base, err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("register failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	fmt.Printf("registered ✓  queue=%q on %s — serving with: %s\n", *name, base, *agent)

	jobsURL := fmt.Sprintf("%s/v1/workers/%s/jobs?wait=%d", base, *name, *wait)
	for {
		req, _ := http.NewRequest(http.MethodGet, jobsURL, nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "poll error (retrying in 2s): %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if r.StatusCode == http.StatusNoContent {
			r.Body.Close()
			continue // idle heartbeat
		}
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			log.Fatalf("poll failed (%d): %s", r.StatusCode, strings.TrimSpace(string(b)))
		}
		var job struct {
			ID       string `json:"id"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()

		start := time.Now()
		answer, aerr := runAgent(*agent, promptFrom(job.Messages))
		if aerr != nil {
			answer = "agent error: " + aerr.Error()
		}
		ab, _ := json.Marshal(map[string]string{"content": answer})
		pr, perr := http.Post(fmt.Sprintf("%s/v1/workers/%s/jobs/%s", base, *name, job.ID), "application/json", bytes.NewReader(ab))
		if perr != nil {
			fmt.Fprintf(os.Stderr, "post answer: %v\n", perr)
			continue
		}
		pr.Body.Close()
		fmt.Printf("answered job %s (%s)\n", job.ID, time.Since(start).Round(time.Millisecond))
	}
}

// promptFrom renders the last user message text (agents want a prompt, not raw
// JSON). Falls back to concatenating all message text.
func promptFrom(msgs []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}) string {
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
