---
name: routsi-worker
description: >
  Become a routsi worker: register a named queue with a routsi proxy and answer the questions it
  routes to you. TWO modes — (A) THIS agent session answers with its own reasoning, driving the
  register/poll/answer loop itself turn by turn (the point of this skill), or (B) a headless
  subprocess loop (`routsi worker run --agent 'cmd'`) shells each question to another command.
  Use when the user says "become a routsi worker", "join a routsi proxy as a worker", "supply
  answers to routsi", "let my claude/codex/opencode answer routsi requests", "register my agent
  with routsi", "run a routsi worker", or "become a routsi provider".
---

# routsi-worker — turn this agent session into a routsi provider

routsi is an OpenAI-compatible proxy. A **worker** lets an agent answer requests that the proxy
routes to a named **queue** — without installing or logging in anything on the proxy host. The
proxy pushes questions to your queue; you answer with your own agent, right where it's already
logged in.

## Two modes — pick one

- **(A) Agent-driven loop (this skill's main flow, below).** *You* — the running Claude
  Code/codex/opencode session — register a queue, poll for jobs yourself, compose each answer
  with your own reasoning, and post it back. No subprocess in the middle; the model answering is
  this very session. This is the point of this skill.
- **(B) Headless subprocess loop.** `routsi worker run --proxy URL --queue NAME --agent 'cmd'`
  runs a standalone loop that shells each question's prompt to `cmd` on stdin and posts back
  whatever `cmd` prints on stdout. Use this when you want a fire-and-forget process (e.g. in a
  systemd unit or tmux pane) rather than an interactive agent session driving it turn by turn.
  See `routsi worker scaffold` for a pure-curl version of the same loop.

Both modes share the same proxy contract: register a queue name once; it becomes a routable model
(`{"model":"QUEUE_NAME"}` reaches you); one worker per queue; non-streaming (a whole answer per
question); no worker auth in v1.

Installed via `routsi install --skills` into `~/.claude/skills` and `~/.codex/skills` — invoke it
in any agent session with a matching request (see the trigger phrases above).

## Mode A — the agent-driven loop (do this yourself, step by step)

You need the **`routsi`** binary on PATH (`routsi version` to confirm) and a proxy URL that's
reachable (outbound HTTPS only — no inbound ports needed on your side).

### 1. Determine the proxy URL and a queue name

Ask the user if either is unknown. Default proxy: `http://localhost:8080` (or whatever the user
has running). Pick a **unique, descriptive queue name** — convention `<who>-<agent>`, e.g.
`alice-claude`, `muthu-codex`. It must not collide with an existing queue or a configured model.

### 2. Register

```sh
routsi worker register --proxy <URL> --queue <NAME>
```

Prints `registered ✓  queue="<NAME>" on <URL>` on success; non-zero exit + a clear stderr message
on failure (e.g. name collision — pick a different name and retry).

### 3. Loop until the user says stop

**a. Poll for a job:**

```sh
routsi worker poll --proxy <URL> --queue <NAME> --wait 25
```

- If a job is waiting, this prints **one JSON line** to stdout and exits 0:
  `{"id":"<jobid>","prompt":"<last user message text>","messages":[...the full messages array...]}`
- If nothing arrives within the wait window, it prints **nothing** and exits 0 — just poll again
  (go back to step a).
- A non-zero exit means a network/HTTP error — report it and retry after a short pause.

**b. On a job: compose the answer yourself.** Read `prompt` (or the full `messages` array for more
context) and **reason about it as you would any question put to you directly** — this is the whole
point of Mode A: the model answering is this session, not a subprocess. Do not shell the prompt to
another agent/CLI; answer it with your own judgment, tools, and context.

**c. Send the answer back:**

```sh
printf '%s' "<your answer>" | routsi worker answer --proxy <URL> --queue <NAME> --id <jobid>
```

`routsi worker answer` reads the answer text from **stdin** (or use `--text '...'` instead of
piping). Prints a short confirmation on success; non-zero exit + stderr on failure (e.g. the job
expired or was already answered — just go back to polling).

**d. Repeat from step a.**

### 3b. Tool calling (when the job carries `tools`)

A job may include the client's OpenAI tool schemas (`"tools": [...]` and optionally
`"tool_choice"`). That means the CLIENT (an SDK, opencode, etc.) can execute functions for
you — you only *decide* which to call; you never run them yourself.

- To call tools, POST the answer as JSON to the raw endpoint (the `--text` form is
  content-only):

  ```sh
  curl -s -X POST "<URL>/v1/workers/<NAME>/jobs/<jobid>" -H 'Content-Type: application/json' \
    -d '{"tool_calls":[{"name":"write","arguments":{"filePath":"a.txt","content":"hi"}}]}'
  ```

  `arguments` may be a JSON object or an already-encoded string; the proxy normalizes to the
  OpenAI wire shape (`finish_reason:"tool_calls"`, string arguments, generated call ids) and
  the client executes the calls. Multiple calls in one array = parallel calls.
- The results come back as the NEXT job on the same conversation: the `messages` array gains
  your assistant `tool_calls` turn plus `role:"tool"` result messages. Continue until you can
  answer with plain `{"content":"..."}`.
- Never invent values that must come from a tool result — make the prerequisite call first.
- Do NOT do the client's task with your own tools (don't create their files locally); the
  whole point is that the client executes on its side.

For a long-lived interactive TUI (devin/claude/codex) serving a queue via file-handoff — no
prompt-size limits, warm context — see `examples/interactive-worker/` in the routsi repo.

### 4. Stopping

There's no special stop command — just stop looping. If you're driving this yourself turn by
turn, stop when the user tells you to. If you scripted the loop (e.g. a shell `while` around the
three commands), Ctrl-C ends it; the proxy fast-fails new requests to that queue once you stop
polling (no cleanup needed on your end).

## Mode B — headless subprocess (for when you want a fire-and-forget process instead)

```sh
routsi worker run --proxy <URL> --queue <NAME> --agent 'claude -p'    # or 'codex exec --skip-git-repo-check -', 'opencode run -', 'cat' (echo test)
```

This is the same register → poll → answer loop as Mode A, but built into one command: it pipes
each question's prompt to `AGENT_CMD` on stdin and posts `AGENT_CMD`'s stdout back as the answer.
Good for a non-interactive process; the model reasoning happens in whatever `AGENT_CMD` is, not in
this skill's session. `routsi worker scaffold` prints an editable pure-curl version of the same
loop for anyone who'd rather not use the built-in command.

## Verify you're live

- **From the proxy dashboard:** open the proxy root (`<URL>/`) — your queue shows up as a
  registered worker / routable model.
- **Over HTTP:** `curl <URL>/v1/workers` (or `GET /v1/models`) — your queue name appears.
- **Send a real request** to the proxy with your queue as the model:
  ```sh
  curl <URL>/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"<NAME>","messages":[{"role":"user","content":"hello"}]}'
  ```
  The proxy routes it to your queue; in Mode A, your poll picks it up next and you answer it
  yourself; the reply comes back in OpenAI wire format.

## Troubleshooting

- **Queue name collision / "one worker per queue".** A queue is served by a single worker in v1.
  If your name is taken (or reserved by a configured model), pick a different, more specific name.
- **Proxy unreachable / connection refused.** Check the `--proxy` URL/port and that you can reach
  it (`curl <URL>/health`). Outbound only — no inbound ports needed on your side.
- **`worker poll` keeps printing nothing.** That's normal idle behavior — just keep polling. It
  only means no request has arrived for your queue yet.
- **`worker answer` fails with a conflict/expired error.** The job likely already timed out or was
  answered — this is not fatal, just go back to polling for the next one.
- **Requests to your queue time out with 503.** No worker is currently polling — make sure you (or
  your Mode B subprocess) are actively looping register → poll → answer.
