# routsi

An OpenAI-compatible endpoint that acts as a **dynamic LLM router**: clients call it
like any OpenAI API, and per request it decides — based on the question content and/or
the conversation id — which upstream model/provider to forward to, then returns the
response (streaming included) in OpenAI wire format. Routing intent: cheap models for
cheap questions, strong models for hard ones, sticky routing per conversation.

Language: **Go** (module `github.com/muthuishere/routsi`). Task runner: **Taskfile v3**.

> **How to work with this code → [AGENTS.md](AGENTS.md)** (architecture, build/test/run,
> how to add a backend/agent/router, invariants). This file adds the living project
> state, decisions, and caveats below.

## Workflow (2026-07-23 — full ceremony; the project is going big)

Non-trivial work moves through four gates **in order**. Update this file at the end.

1. **ADR** — `docs/adr/NNN-title.md`, one decision per file: Status
   (Proposed/Accepted/Superseded) · Context · Decision · Alternatives · Consequences ·
   Open questions. Discuss in chat, cite `docs/research/`; get explicit OK before
   Accepted. **No code embodying a Proposed decision is committed.**
2. **Spike** — `docs/spikes/NNN-*.md`: a throwaway proof-of-concept that de-risks the ADR
   (does the API exist? does the timing/protocol work?). Evidence + findings, not
   production code; any code stays in the spike doc or a `_`-prefixed dir, out of the
   build.
3. **OpenSpec** — after the ADR is Accepted: `openspec/changes/<id>/` (proposal + spec
   deltas + tasks) referencing the ADR number. `openspec validate`; walk through before
   coding. `openspec archive` when done.
4. **Implementation (TDD)** — red→green→refactor, table-driven tests, `httptest` mock
   upstreams for wire code. `go vet ./...` + `go test ./...` (`task test`) green. Tick
   OpenSpec tasks as completed.

Trivial changes (typos, docs, dep bumps): skip the gates. When unsure, it isn't trivial.

> **Earlier phases (catalog API, agents, discovery, dynamic groups, auth/mTLS, service,
> metrics, dashboard) were built in a collapsed "speed over ceremony" mode.** That mode
> is retired as of ADR-001; new subsystems follow the four gates above.

## Repo shape

```
cmd/routsi/           # main: subcommands serve|install|uninstall|status|version
internal/api/         # minimal OpenAI wire types + envelope builders + usage
internal/config/      # models.yaml load + startup validation (ConfigDir helper)
internal/router/      # Router interface + v1 rules scorer (low|medium|high)
internal/sticky/      # conversation-id -> model TTL pin store
internal/backend/     # Backend interface, raw forward relay, toolnexus, devin, cliagent
internal/discovery/   # startup model discovery (upstream /models, devin probe, known-models.json)
internal/metrics/     # in-process collector -> /metrics (prom) + /stats (json)
internal/service/     # launchd/systemd install (the watchdog)
internal/server/      # HTTP surface + embedded dashboard.html (/, /stats, /metrics)
docs/research/        # the evidence base behind the design (read before re-deciding)
models.yaml           # sample config
```

## Core design decisions (already made, evidence in docs/research/)

- **Pre-request routing only**; never touch a response mid-stream. Forwards are raw
  byte passthrough with model+auth rewrite; retries only before the first byte.
- **`auto` routes; a concrete model name is a hard bypass.** Chosen model is always
  disclosed via `X-Selected-Model`.
- **Dynamic virtual models** (`type: dynamic`): named groups (e.g. `dynamic-1`) with
  `levels: {low, medium, high}` → member models. The rules router classifies each task
  into a level; missing levels fall back to the nearest declared one; pins are scoped
  per group (`convID#group`) with escalate-only. `auto` is the same mechanism over the
  global `tiers:` map (cheap/strong keys are aliases of low/high). Members are
  validated at server start (after discovery), so discovered variants are legal
  members; nesting groups is rejected.
- **Sticky by conversation id** (header > body field > first-user-message hash),
  escalate-only, never downgrade mid-conversation. In-memory TTL store.
- **Backend interface** (`Complete`/`Stream`) for anything that isn't an OpenAI-style
  forward: custom Go handlers (registered by name), toolnexus-translated
  Anthropic/Gemini upstreams (toolkit created with `Builtins: false` — a translator
  must never expose shell/file tools), and the Devin CLI (`type: devin` — first turn
  runs `devin -p` and captures the session via `devin list --format json`; later turns
  `devin -r <session> -p`, so proxy conversations map to real Devin sessions.
  Live-verified 2026-07-23).

## Conventions

- Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`), trunk-based off `main`,
  short-lived branches, squash-merge.
- Errors: wrap with `fmt.Errorf("...: %w", err)`; no panics in request paths.
- Config via env vars; **never** commit or print secret values — reference `$VAR` only.
  Upstream provider keys are use-only env vars per my global rules.
- OpenAI compatibility is the contract: request/response/streaming (SSE) shapes must
  match the OpenAI API so any OpenAI SDK works unmodified against this proxy.
- Match existing style; no drive-by refactors.

## Phases (owner-set, 2026-07-23)

- **Phase 1 (current): the model-catalog API.** Every model/agent is addressable by
  name over the OpenAI surface — LLM forwards, toolnexus-translated styles, custom Go
  handlers, Devin — all conversation-id capable. Use toolnexus wherever it fits
  (translation, proxy-managed conversation memory). No routing intelligence required
  beyond the existing rules stub.
- **Phase 2 (later): the agentic auto-router** — dynamic choice across the whole
  catalog (including agent-internal variants like `devin --model X`) based on signals
  TBD. Design inputs live in docs/research/; don't build it until the owner says so.

## Conversation contract

- **Explicit `conversation_id`** (X-Conversation-Id header or body field) = the PROXY
  owns memory. Client sends only the new message; forwards go through toolnexus `Ask`
  (its ConversationStore keeps the transcript), Devin resumes its session, custom
  handlers receive the id. No id = raw passthrough, client resends history as usual.
- Fingerprint ids (`fp-` prefix, hashed first user message) are for routing stickiness
  ONLY — they must never trigger proxy-managed memory.

## Pull-workers (ADR-001, shipped 2026-07-25)

First feature under the restored ADR→spike→OpenSpec→TDD workflow. **Additive** — nothing
existing changed. A remote worker registers a named queue and answers requests routed to
it (`internal/queue` broker; `type: queue` backend; `POST /v1/workers/register`, `GET
/v1/workers/{name}/jobs?wait=`, `POST /v1/workers/{name}/jobs/{id}`, `GET /v1/workers`
status). Queue is a routable model; dynamic registration adds it at runtime (server
`dynamic` map, mutex-guarded); fast-503 when no worker polled recently; one worker per
queue; non-streaming; at-most-once; no worker auth in v1 (reserved `workers.auth: {}`).
`routsi worker run|scaffold` CLI + an embedded agent skill installed via `routsi install
--skills` (→ ~/.claude/skills, ~/.codex/skills; skill source at cmd/routsi/skills/).
Live-verified: proxy + `routsi worker` loop answered a real chat request end-to-end;
status/dynamic-register/collision/skills-install all checked. OpenSpec change archived
(2026-07-25-add-pull-worker). ADR-002/003 (unified-registry + control-plane refactor)
**parked** — owner scoped to additive only.

## Current state (2026-07-23)

Phase 1 built and tested: routing (`auto` + rules), bypass, stickiness with
escalate-only, raw SSE passthrough, custom-handler envelope (real + faked streaming),
retry-before-first-byte, proxy-managed conversation memory (toolnexus `Ask`), CLI
agents devin/codex/copilot (all three live-verified), `variants:` expansion
(`codex/gpt-5.6-sol` style catalog entries), `provider:` → owned_by in /v1/models,
estimated `usage` tokens in enveloped responses (final stream chunk carries usage),
dynamic model discovery (`discover_models: true`, once at startup): forwards fetch
upstream GET /models (capped 100; live-verified against api.openai.com — 103 models);
devin is probed with a bogus `--model` whose error prints the account's live model
list ("Available: ..." — 35 models live-verified), falling back to the table;
codex/copilot/claude have no list mechanism (probed: errors carry no list) so their
lists come from `~/.config/routsi/known-models.json` (bootstrapped with
defaults on first run, user-editable, merged over built-ins; env override
ROUTSI_CONFIG_DIR for tests). Claude Code CLI is the 4th agent type
(`type: claude`, one-shot `-p --model`). Config resolution: `-config` flag >
./models.yaml > ~/.config/routsi/models.yaml. Default/tier/dynamic-member
existence is checked at server start (post-discovery), NOT at parse — discovered
names are legal there. Full live sweep 2026-07-23: 149-model catalog, claude/haiku
answered, dynamic-1 routed low/high correctly, sticky escalate-only held, SSE
streamed, proxy-managed memory recalled across single-message turns.

Renamed llm-forward-proxy → **routsi** (module github.com/muthuishere/routsi) and
productized 2026-07-23: subcommand CLI (serve|install|uninstall|status|version);
`routsi install` sets up a keep-alive service (launchd on macOS / systemd --user on
Linux, restart-on-failure + start-at-login, no root — internal/service); in-process
metrics (internal/metrics) at `/metrics` (Prometheus) + `/stats` (JSON); self-contained
live dashboard embedded at `/` (internal/server/dashboard.html, polls /stats, theme-
aware). Claude Code added as 4th agent type. OpenRouter is the go-to cheap upstream for
testing (haiku + gemini-flash live-verified, incl. as dynamic-1 members). Live-verified
this round: /stats, /metrics, dashboard HTML served, CLI version/status, metrics
aggregation (routed vs bypass, per-model tokens/latency/escalations). Distribution:
CLI-only (no Docker per owner) — native binary is the artifact; `task build` stamps
version via ldflags, `task install` → ~/.local/bin.

Caveats: metrics token counts are 0 on raw passthrough (bytes relayed, not parsed) —
only enveloped/proxy-managed paths report (estimated) tokens; `routsi install` not
run in-session (would modify the user's launchd — owner runs it); toolnexus translator
untested against a live Anthropic upstream (OpenRouter covers those models via
passthrough); CLI-agent + translator streaming is faked off Complete; codex/copilot/
claude one-shot (transcript rendered, no session mapping; codex has resume worth
wiring); codex variant names account-dependent.
Added 2026-07-23 (post-publish): **inbound auth** — `auth.tokens_env` bearer tokens
guard /v1/* + /stats + /metrics (constant-time compare; /health open; dashboard via
/?token=...; unit-tested 401/200/query-token) — and **TLS/mTLS** — `tls.cert/key` for
HTTPS, `tls.client_ca` requires+verifies client certs (wired in main.go, not
live-tested). Repo is PUBLIC: github.com/muthuishere/routsi (MIT, AGENTS.md guide,
gh-pages site at muthuishere.github.io/routsi via gh-pages branch — Pages REST API
404s on gh oauth tokens, branch push auto-provisions). Local dir still named
llm-forward-proxy.
Not yet built (see docs/research/): tool-call passthrough on enveloped paths,
escalation memory handoff, compaction, budget caps, learned scorer, Phase-2 router.

Added 2026-07-25: **SSE heartbeat keep-alive** on the streaming envelope path
(`internal/server/server.go`'s `envelope`) — the buffered backend's `Stream` now
runs in a goroutine funneling deltas through a channel; the handler goroutine
serializes all writes via a `select` over deltas/done/ticker/ctx-done, so a
heartbeat can never interleave into a half-written chunk. Wire format: a bare
SSE comment `: ping\n\n` (ignored by OpenAI SDK parsers) written only if no
delta landed since the last tick. New config key `stream_heartbeat` (default
15s; `0` disables), wired through `internal/config`. Forward passthrough
(`internal/backend/forward.go`) left untouched — it already streams live
upstream bytes, lower risk; not rearchitected. Also added
`internal/server/scenarios_test.go` — five named scenario tests (token auth,
auto routing, concrete bypass, streaming+heartbeat, pull-worker) as executable
examples backing a future docs "Scenarios" page. `go vet ./...` + `go test
./...` green.
