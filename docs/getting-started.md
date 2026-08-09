---
title: Get started
---

# Get started — one endpoint, a cheap rung and a strong rung

Five minutes, one API key, and everything you own that speaks OpenAI (toolnexus,
opencode, Cursor, the SDKs, your own scripts) gets task-aware routing, sticky
conversations, tool calling, metrics and a dashboard — without changing a line of
client code.

The recipe below is the one on my own machine. It is deliberately small: two
rungs on OpenRouter — **Ling 2.6 Flash** for the easy turns and **Qwen 3.7
Flash** for the rest — both with native tool calling and a very large context.
Together they cost a rounding error, so you can leave routing on all day.

---

## 1. Install

```sh
go install github.com/muthuishere/routsi/cmd/routsi@latest
# or: npm install -g @muthuishere/routsi   (prebuilt binary, no Go toolchain)
```

## 2. Configure

Save as `~/.config/routsi/models.yaml` (routsi finds it automatically; a
`./models.yaml` in the working directory wins, and `-config` beats both). It
also ships as
[`examples/models.qwen.yaml`](https://github.com/muthuishere/routsi/blob/main/examples/models.qwen.yaml):

```yaml
listen: ":11080"
default: small
sticky_ttl: 10m

tiers:            # what plain `model: "auto"` resolves to
  cheap: small
  strong: max

models:
  - name: small                                   # $0.010 / $0.030 per Mtok · 262k ctx
    type: forward
    provider: openrouter
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
    upstream_model: inclusionai/ling-2.6-flash

  - name: big                                     # $0.030 / $0.130 per Mtok · 1M ctx
    type: forward
    provider: openrouter
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
    upstream_model: qwen/qwen3.7-flash

  - name: max                                     # a NAME, not yet a bigger model
    type: forward
    provider: openrouter
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
    upstream_model: qwen/qwen3.7-flash

  - name: dyn
    type: dynamic
    levels:
      low: small
      medium: big
      high: max
```

`max` deliberately points at the same upstream as `big`. A rung is a **name**,
not a model — declaring the top of the ladder up front means you can repoint it
later (`qwen/qwen3.7-plus`, `qwen/qwen3.7-max`, a local agent, a pull-worker)
without touching a single client. Everything keeps asking for `dyn`.

`api_key_env` names an **environment variable**, never a key. routsi reads it at
startup; the value never enters the config file, the logs, or the dashboard.

## 3. Run

```sh
echo 'OPENROUTER_API_KEY=sk-or-...' >> ~/.config/routsi/.env   # or just export it
routsi serve
# routsi v0.3.0 on :11080 — 3 models, default small — dashboard http://localhost:11080/
```

Then point anything at **`http://localhost:11080/v1`** with any non-empty API key.

```sh
curl localhost:11080/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"dyn","messages":[{"role":"user","content":"say hi in 3 words"}]}'
```

Make it permanent — `routsi install` registers a keep-alive service (launchd on
macOS, `systemd --user` on Linux; restart-on-failure, start-at-login, no root).

---

## What you just bought

### One name, the right model

Ask for `dyn` and routsi scores the task and dispatches. The choice is always
disclosed in the `X-Selected-Model` response header, so it is never a black box:

| request | `X-Selected-Model` | upstream |
|---|---|---|
| "say hi in 3 words" | `small` | `inclusionai/ling-2.6-flash` |
| "summarize the tradeoffs of optimistic vs pessimistic locking in a distributed database" | `big` | `qwen/qwen3.7-flash` |
| "Design and critique a lock-free MPMC ring buffer in Go, analyzing the memory ordering guarantees on ARM64 vs x86-64, and prove the absence of ABA" | `max` | `qwen/qwen3.7-flash` |

Ask for `small`, `big` or `max` by name and routing is bypassed entirely — a
concrete model name is always a hard bypass.

> **Declare all three levels.** An omitted level falls back to the nearest
> *declared* one, and `medium` prefers `low` first
> ([`router.Fallback`](https://github.com/muthuishere/routsi/blob/main/internal/router/router.go)).
> Leaving `medium:` out quietly sends medium-scored work to the cheap rung.

### Tool calling, on every rung

Both rungs emit native OpenAI `tool_calls`, including parallel ones in a single
turn, and accept `role: "tool"` results back:

```
finish: tool_calls
  call_9f59a57e6481492  get_weather  {"city": "Chennai"}
  call_c85bb134fcb643a  get_weather  {"city": "Tokyo"}
```

This matters if you drive routsi from toolnexus, opencode, or any agent loop:
the whole point is that the tool contract survives the routing. Every routsi
transport is covered by the capability matrix — run `task matrix` to print it.

### Sticky conversations

Send an `X-Conversation-Id` header (or a `conversation_id` body field) and the
conversation pins to whichever rung it landed on. Pins **escalate only** — a
conversation that reached `max` never silently drops back to `small` mid-thread.
Verified: an easy opener pinned to `small`, then a hard follow-up in the same
conversation came back `X-Selected-Model: big`.

An explicit conversation id also flips on **proxy-managed memory**: routsi keeps
the transcript, so the client sends only the new message each turn.

### Visibility

- `http://localhost:11080/` — live dashboard: the endpoint to copy, ready-made
  client snippets, per-model traffic, and the routing decisions as they happen.
- `/stats` — the same data as JSON. `/metrics` — Prometheus.

---

## Growing the config

Everything below is additive — drop it into the same `models:` list.

**Headroom you only reach on purpose.** Leave a model out of every group and it
can only be reached by name, so it can never surprise the bill:

```yaml
  - name: opus                                    # $5.00 / $25.00
    type: forward
    provider: openrouter
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
    upstream_model: anthropic/claude-opus-5
```

Point `dyn.high` at it the day the ladder needs real headroom — that is a
one-line change, and every client keeps asking for `dyn`.

**Your own Claude Code session as a model.** A pull-worker queue is a routable
model like any other — no inbound URL, no port to open:

```yaml
  - name: claude-session
    type: queue

  - name: dyn-agent
    type: dynamic
    levels: { low: small, medium: big, high: claude-session }
```

```sh
routsi worker join --queue claude-session --workdir ~/routsi-jobs \
  --notify 'claude -p --dangerously-skip-permissions \
    "Read $ROUTSI_JOB_FILE and write the answer as JSON to $ROUTSI_ANSWER_FILE"'
```

Now `dyn-agent` sends easy turns to a hosted model and hard ones to a real
Claude Code session on your laptop.

**A local script as a model** — see [the adapter contract](adr/013-adapter-contract.md).
Anything that reads a job on stdin and writes an answer on stdout is a model:

```yaml
  - name: mine
    type: command
    command: node $ROUTSI_ADAPTERS/cli.js
```

**Your own routing brain** — a `decider:` block swaps the built-in scorer for any
executable. See [`examples/decider.js`](https://github.com/muthuishere/routsi/blob/main/examples/decider.js).

---

## Gotchas worth knowing up front

**Reasoning models eat `max_tokens` before they write a word.** Qwen 3.7 Flash
(and DeepSeek V4 Pro, gpt-5-nano, Claude with extended thinking) spend the
completion budget on reasoning tokens first. Ask for three words with
`max_tokens: 300` and you get `content: null`, `finish_reason: "length"`, and
`reasoning_tokens: 300`. Two fixes, both verified through routsi:

```jsonc
{"max_tokens": 2000}                  // give it room — 795 reasoning tokens, then the answer
{"reasoning": {"enabled": false}}     // or turn thinking off — 0 reasoning tokens, instant answer
```

Because `type: forward` is raw passthrough, provider-specific knobs like
`reasoning` reach the upstream untouched — routsi rewrites only the model name
and the auth header.

**`~`-prefixed OpenRouter ids.** Some OpenRouter entries look like
`~qwen/qwen3.7-flash-latest`. Prefer the unprefixed stable id.

**Any API key works.** routsi is open by default — the client's key is discarded
and replaced with the upstream key named by `api_key_env`. To require a token,
add an `auth.tokens_env` block; `/health` stays open, everything else needs a
bearer.

**Check the price before you pin a model.** OpenRouter's catalog moves:

```sh
curl -s https://openrouter.ai/api/v1/models | python3 -c "
import json,sys
for m in json.load(sys.stdin)['data']:
    if 'qwen' in m['id']:
        p=m['pricing']; sp=m.get('supported_parameters',[])
        print(f\"{m['id']:42} in={float(p['prompt'])*1e6:7.3f} out={float(p['completion'])*1e6:7.3f} tools={'tools' in sp}\")"
```

---

## Where to go next

- [Using routsi with opencode](opencode.md)
- [Adapter contract (ADR-013)](adr/013-adapter-contract.md) — any executable as a model
- [Pull workers (ADR-001)](adr/001-pull-worker-queue.md) — a model behind a firewall
- [All ADRs](adr/README.md) — every design decision, with its alternatives
