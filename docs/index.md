---
title: routsi
---

# routsi

**One OpenAI-compatible endpoint that routes each request to the right model — or the
right agent.** API models (OpenRouter, OpenAI, DeepSeek, …) and local agent CLIs
(Devin, Codex, Copilot, Claude Code) behind one `/v1/chat/completions`, chosen per
task, sticky per conversation. Single Go binary.

[GitHub →](https://github.com/muthuishere/routsi)

## Why

Gateways (LiteLLM, Portkey, …) balance providers serving the *same* model. routsi
routes between *different answerers*: `devin/claude-opus-4.8` is as callable as
`gpt-4o-mini`, and a `dynamic-1` group sends easy turns to a cheap model and hard
turns to a strong one — escalate-only, never downgrading mid-conversation.

## Install

```sh
go install github.com/muthuishere/routsi/cmd/routsi@latest
routsi serve                 # ./models.yaml or ~/.config/routsi/models.yaml
routsi install               # run as a keep-alive service (launchd/systemd)
```

## Use

```sh
curl localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

- `auto` / `dynamic-1` — routed by task · concrete name — exact model · `devin/opus` —
  an agent as a model
- `X-Conversation-Id` header — sticky routing + proxy-managed memory / real agent
  sessions
- Dashboard at `/`, JSON at `/stats`, Prometheus at `/metrics`
- Bearer-token auth + mTLS via config

## Design

Grounded in the routing literature and production practice (RouteLLM, cascade
analyses, OpenRouter/LiteLLM field reports) — the evidence lives in
[`docs/research/`](https://github.com/muthuishere/routsi/tree/main/docs/research).
Key invariants: route before the first byte, never mid-stream; stickiness is
escalate-only; fingerprint conversation ids never touch agent memory.

MIT licensed.
