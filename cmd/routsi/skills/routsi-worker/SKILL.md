---
name: routsi-worker
description: >
  Turn any AI coding agent (opencode, codex, Claude Code, or a custom script) into a routsi
  worker: register a named queue with a routsi proxy, long-poll for questions, answer each one
  with your own locally logged-in agent, and post the answer back. The proxy becomes a broker and
  routes matching requests to your queue; your credentials never leave your machine (outbound-only,
  NAT-friendly). Use when the user says "join a routsi proxy as a worker", "supply answers to
  routsi", "register my agent with routsi", "run a routsi worker", "let my codex/claude/opencode
  serve routsi requests", or "become a routsi provider".
---

# routsi-worker — turn your agent into a routsi provider

routsi is an OpenAI-compatible proxy. A **worker** lets *your* logged-in agent answer requests
that the proxy routes to a named **queue** — without installing or logging in anything on the
proxy host. You run one loop on your own machine; the proxy pushes questions to it, you answer
with your local agent.

This skill wraps the built-in CLI command **`routsi worker run`**. That's the whole thing:

```sh
routsi worker run --proxy https://PROXY:8080 --queue QUEUE_NAME --agent 'AGENT_CMD'
```

It registers the queue once, long-polls the proxy for questions, pipes each question to
`AGENT_CMD` on **stdin**, and posts `AGENT_CMD`'s **stdout** back as the answer. The moment the
queue registers it becomes a routable model named after the queue (visible in `/v1/models`), so
anyone hitting the proxy with `model: "QUEUE_NAME"` reaches you.

## How it works (the contract)

1. **Register** the queue with the proxy (idempotent).
2. **Long-poll** for the next question.
3. On a question, render it to a prompt and pipe it to `AGENT_CMD` on **stdin**.
4. Capture `AGENT_CMD`'s **stdout** and **post it back** as the answer.
5. Repeat.

The only requirement on your agent: **it must read the question from stdin and print the answer
to stdout.** Any tool that does that works.

> v1 notes: **no worker auth** (a `--token` flag is accepted but ignored — reserved for later).
> Non-streaming (a whole answer is returned per question). **One worker per queue** — pick a
> unique queue name.

## Prereqs

- The **`routsi`** binary on your PATH (`routsi version` to confirm).
- Your **agent CLI installed and logged in** (e.g. `codex`, `claude`, `opencode`) — the worker
  reuses whatever session that CLI already has. routsi does not log in for you.
- Network reach to the proxy (outbound HTTPS only — no inbound ports on your side).

## Start it (the one-liner)

Pick a **unique, descriptive queue name** — it's how the proxy addresses you, and it must not
collide with an existing queue or a configured model. Convention: `<who>-<agent>`, e.g.
`alice-codex`, `muthu-claude`, `team-opencode`.

### codex

```sh
routsi worker run \
  --proxy https://PROXY:8080 \
  --queue alice-codex \
  --agent 'codex exec --skip-git-repo-check -'
```

### Claude Code

```sh
routsi worker run \
  --proxy https://PROXY:8080 \
  --queue alice-claude \
  --agent 'claude -p'
```

### opencode

```sh
routsi worker run \
  --proxy https://PROXY:8080 \
  --queue alice-opencode \
  --agent 'opencode run -'
```

### Trivial echo test (no agent needed — proves the loop end-to-end)

Any command that reads stdin and prints something works. `cat` just echoes the question back:

```sh
routsi worker run \
  --proxy https://PROXY:8080 \
  --queue test-echo \
  --agent 'cat'
```

Then send a request to the proxy with `model: "test-echo"` and you'll get the question back as
the answer. Good first smoke test before wiring a real agent.

### Custom script

Anything on PATH that reads stdin and writes stdout:

```sh
routsi worker run --proxy https://PROXY:8080 --queue alice-bot --agent './answer.sh'
```

where `answer.sh` reads the prompt on stdin and prints the reply.

## Verify you're live

- **From the proxy dashboard:** open the proxy root (`https://PROXY:8080/`) — your queue shows
  up as a registered worker / routable model.
- **Over HTTP:** `curl https://PROXY:8080/v1/workers` (or `GET /v1/models`) — your queue name
  appears in the list.
- **Send a real request** to the proxy with your queue as the model:
  ```sh
  curl https://PROXY:8080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"alice-codex","messages":[{"role":"user","content":"hello"}]}'
  ```
  The proxy routes it to your queue, your agent answers, and the reply comes back in OpenAI
  wire format. The worker also prints human status per job (`registered ✓`, `answered job … (1.2s)`).

## Troubleshooting

- **Queue name collision / "one worker per queue".** In v1 a queue is served by a single
  worker. If your name is already taken (or reserved by a configured model), pick a different,
  more specific name (`alice-codex-2`, `alice-laptop-codex`).
- **Proxy unreachable / connection refused.** Check the `--proxy` URL and port, that the proxy
  is running, and that you can reach it (`curl https://PROXY:8080/health`). The worker connects
  outbound only — no inbound ports needed on your side, but egress to the proxy must be open.
- **Empty or garbage answers → your agent command is wrong.** `AGENT_CMD` **must read the
  question from stdin and print only the answer to stdout.** Test it standalone first:
  `echo 'what is 2+2?' | codex exec --skip-git-repo-check -` should print an answer. If your CLI
  needs a flag to read stdin (a trailing `-`, `--stdin`, etc.), add it. Diagnostics printed to
  stderr are fine; anything on stdout becomes the answer.
- **Agent not logged in.** The worker reuses your CLI's existing session — if the agent CLI
  isn't authenticated, log it in first (run it once interactively), then start the worker.
- **Requests time out with no answer.** If no worker is polling a queue, the proxy fast-fails
  that queue. Make sure the worker is running and registered (see *Verify you're live*).
