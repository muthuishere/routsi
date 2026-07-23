# routsi

One OpenAI-compatible endpoint that routes each request to the right **model or
agent** — API models (OpenRouter, OpenAI, DeepSeek, …) *and* local agent CLIs
(Devin, Codex, Copilot, Claude Code) — with per-task routing, per-conversation
stickiness, a live dashboard, and metrics. A single Go binary; point any OpenAI SDK
at it.

## Quick start

```sh
task build                       # -> bin/routsi
cp models.yaml ~/.config/routsi/ # or keep ./models.yaml
routsi serve                     # listens on :8080 by default
```

Point any OpenAI client at `http://localhost:8080/v1`. Then:

```sh
curl localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

- **`model: "auto"`** — routsi classifies the task and picks a tier.
- **`model: "dynamic-1"`** — a named group you define (low/medium/high members).
- **`model: "openrouter/anthropic/claude-haiku-4.5"`** — a concrete model, no routing.
- **`model: "devin/opus"`** — an agent as a model.

### Forwarding to an agent model (devin/…)

Agent CLIs expose their inner models as `<agent>/<model>` catalog entries — from
`variants:` or discovery (`GET /v1/models` lists them all). Just name one:

```sh
curl localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -H 'X-Conversation-Id: my-thread-1' \
  -d '{"model":"devin/claude-opus-4.8","messages":[{"role":"user","content":"refactor plan?"}]}'
```

`devin/claude-opus-4.8` runs `devin --model claude-opus-4.8`; the conversation id maps
to a real Devin session (`-r` resume on later turns). Bare `devin` uses the default
model. Same pattern for `codex/…`, `copilot/…`, `claude/…` (one-shot). A dynamic
group level can point at an agent too: `high: devin/claude-opus-4.8`.

## Auth (token or mTLS)

Generate tokens with the built-in CLI:

```sh
routsi token -n 2      # prints rtk_... tokens + setup instructions
```

```yaml
auth:
  tokens_env: ROUTSI_TOKENS   # export ROUTSI_TOKENS="tok1,tok2" — enables bearer auth
tls:
  cert: server.crt            # cert+key = HTTPS
  key: server.key
  client_ca: clients-ca.crt   # + client_ca = mTLS (client certs required)
```

With tokens on: `/v1/*`, `/stats`, `/metrics` need `Authorization: Bearer <tok>`
(OpenAI SDKs send this automatically as the api_key); dashboard: `/?token=tok`.
`/health` stays open. Token values live in env vars, never in the config file.

The chosen model is returned in the `X-Selected-Model` header. Every response
carries a `usage` block.

## Run it as a service (watchdog)

Keeps routsi up across crashes and logins — launchd on macOS, systemd-user on Linux:

```sh
routsi install     # install + start (restart-on-failure, start-at-login)
routsi status      # is it running?
routsi uninstall   # stop + remove
```

No root; everything lives under your home dir. macOS logs: `~/Library/Logs/routsi.log`.

## Dashboard & metrics

- **`http://localhost:8080/`** — live dashboard (requests, tokens, latency, routing
  split, escalations), auto-refreshing, self-contained.
- **`/stats`** — JSON snapshot.
- **`/metrics`** — Prometheus text (scrape it).

## Config (`models.yaml`)

See the commented [`models.yaml`](models.yaml). Highlights:

- `type: forward` — any OpenAI-compatible upstream (raw byte passthrough).
- `type: devin|codex|copilot|claude` — local agent CLIs (must be installed + logged in).
- `type: dynamic` — a virtual model with `levels: {low, medium, high}`.
- `variants:` / `discover_models: true` — expand one entry into many models. Forwards
  fetch upstream `GET /models`; Devin is probed live; codex/copilot/claude read
  `~/.config/routsi/known-models.json` (editable).

## How it differs from LiteLLM / OpenRouter / other gateways

Gateways route between *providers serving the same model* (load-balance, fallback).
routsi routes between *fundamentally different answerers* — API models **and full
agents** — behind one OpenAI face, choosing by task, sticky per conversation. It's a
single dependency-light binary you run yourself, not a platform.

## Design notes

Routing/conversation decisions are grounded in `docs/research/`. Not yet built:
inbound auth, tool-call passthrough on enveloped paths, escalation memory handoff,
compaction. See `CLAUDE.md` for current state.
