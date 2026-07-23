# Working with routsi

Guide for anyone (human or agent) changing this codebase. Vendor-neutral; `CLAUDE.md`
points here and adds the living project state + caveats.

## What it is

One OpenAI-compatible HTTP endpoint that routes each request to the right **model or
agent**. A request names a `model`; routsi resolves it three ways:

- a **concrete name** (`openrouter/anthropic/claude-haiku-4.5`, `devin/opus`) → bypass,
  no routing;
- **`auto`** → classify the task, pick a tier from the global `tiers` map;
- a **`dynamic` group** (`dynamic-1`) → classify, pick from that group's `levels`.

Routed groups are sticky per conversation and escalate-only. The answer always comes
back as OpenAI JSON/SSE with a `usage` block and an `X-Selected-Model` header.

## Build / test / run

```sh
task build           # -> bin/routsi (version-stamped)
task test            # go vet ./... && go test ./...
task dev             # go run ./cmd/routsi serve -config models.yaml
routsi serve         # or run the binary; :8080 by default
```

Tests are standard `testing`, table-driven, `httptest` for wire, fake shell scripts
for CLI agents. **Note:** CLI-agent and discovery tests exec temp scripts; some sandboxes
hang on that — run them with the sandbox disabled if `go test` stalls at 20s multiples.

## Package map (dependency order)

| package | role |
|---|---|
| `internal/api` | OpenAI wire types (`ChatRequest`/`ChatResponse`), envelope + `usage` builders |
| `internal/config` | `models.yaml` load + validation, `variants:` expansion, `ConfigDir()` |
| `internal/router` | `Router` interface + `Rules` scorer → level `low`/`medium`/`high` |
| `internal/sticky` | conversation-id → model TTL pin store (escalate-only lives in server) |
| `internal/backend` | `Backend` interface + impls: `Forward` (raw relay), `Toolnexus` (translate), `Devin`, `CLIAgent` (codex/copilot/claude), custom `Registry` |
| `internal/discovery` | startup model discovery: upstream `GET /models`, devin probe, `known-models.json` |
| `internal/metrics` | in-process collector → Prometheus + JSON snapshot |
| `internal/service` | launchd/systemd install (the watchdog) |
| `internal/server` | HTTP surface, `resolve` (bypass > sticky > router), envelope, dashboard |
| `cmd/routsi` | CLI: `serve` \| `install` \| `uninstall` \| `status` \| `version` |

## The two core interfaces (extend these, not the server)

```go
// internal/router — swap in a smarter scorer without touching the server.
type Router interface { Pick(req *api.ChatRequest) string } // returns low|medium|high

// internal/backend — anything that can answer. The server wraps it in OpenAI JSON/SSE.
type Backend interface {
    Complete(ctx, req) (string, error)
    Stream(ctx, req, emit func(delta string)) error
}
```

## How to add things

- **A new upstream/model** → edit `models.yaml`. No code. Use `discover_models: true` to
  auto-expand.
- **A new agent CLI** (like a Gemini CLI) → add a `ModelType` const in `config`, extend
  the `switch` in `config.validate` (CLI-agent case), add the flag mapping in
  `backend/cliagent.go`'s `Complete`, wire it in `server.New`'s switch, and add its
  default models to `discovery.defaultKnownCLIModels`. ~20 lines + a test with a fake
  script (see `cliagent_test.go`).
- **A smarter router** → implement `router.Router`, pass it to `server.New` (currently
  `nil` → `NewRules`). The rules scorer classifies the **current turn only** on purpose
  (cumulative length creep — see `docs/research/conversation-routing.md`).
- **A custom in-process backend** → `reg.Register("name", backend.Completer(fn))` before
  `server.New`; reference it as `type: custom, handler: name` in config.

## Conventions

- Errors wrap with `%w`; no panics in request paths. Config validates at startup — fail
  loud, never silently misroute.
- Secrets are **env-var names** in config (`api_key_env`), never values. Never log or
  commit a key.
- Match local style; touch only what the task needs. Keep tests alongside changes;
  `go vet ./...` + `go test ./...` must pass.

## Design invariants (don't break without reading the research)

- **Pre-request routing only** — never re-route mid-stream. Forwards are raw byte
  passthrough; routing decides on the request head.
- **Escalate-only stickiness** — a conversation's pin only moves to a higher tier, never
  down mid-conversation.
- **Fingerprint ids (`fp-`) are for stickiness only** — they must never key backend
  memory (cross-user leak). Explicit ids own memory.

Evidence for all of the above is in `docs/research/` — read it before re-deciding.
```
