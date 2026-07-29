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

## External decider (ADR-007, shipped 2026-07-25)

Built fast, short-ADR-only (ceremony waived by owner). Optional `decider:` config
block (`internal/config`) — `command`/`timeout`/`cwd` — lets a user swap the built-in
`Rules` scorer for a subprocess: `router.External` (new, `internal/router/external.go`)
implements `Router` unchanged, spawns `sh -c "<command>"` per `auto` request, writes a
JSON request (model/conversation_id/messages/levels/tiers) to stdin, reads
`{"level":"low|medium|high"}` from stdout, and falls back to `Rules` on ANY failure
(spawn error, timeout, non-zero exit, empty/malformed/unknown output) — logs a one-line
warning, never the prompt. Wired in `cmd/routsi/main.go` (`Decider.Command != ""` ⇒
`router.NewExternal(...)`, else `nil` ⇒ `server.New` defaults to `Rules` as before — the
no-decider path is byte-for-byte unchanged). `{"model":...}` direct-model extension
considered and skipped (would need `server.resolve` to special-case decider output;
level contract already covers the case via the group's own tiers/levels map).
Examples: `examples/decider.js` + `examples/decider.py` (zero/stdlib deps, mirror the
built-in heuristic, heavily commented), `examples/README.md` documents the stdin/stdout
contract. Table-driven tests in `internal/router/external_test.go` cover: high level,
malformed/empty output, unknown level, non-zero exit, timeout (all fall back to the
`Rules` decision for that input) — `go vet ./...` + `go test ./...` green.

## 2026-07-25 — agent-driven pull-worker helpers

Added `routsi worker register|poll|answer` (in `cmd/routsi/worker.go`, dispatched from
`cmd/routsi/skills.go`'s `worker()`) so a *running* agent session (Claude Code/codex) can
become a pull-worker itself — register once, then poll/answer turn by turn, answering with
its own reasoning instead of shelling to a subprocess. `run`/`scaffold` unchanged. Fixed a
pre-existing off-by-one in `worker()`'s sub-verb dispatch (`os.Args[2]` → `os.Args[1]`,
since `main()` already strips the top-level `worker` arg) — `routsi worker scaffold` was
silently falling through to `run` before this fix. Rewrote
`cmd/routsi/skills/routsi-worker/SKILL.md` to teach the agent-driven loop as the primary
flow (Mode A) with the headless subprocess (`worker run --agent`) as Mode B. Additive only;
`internal/server`, `internal/queue` untouched.

## Tool-calling track (2026-07-29 — ADRs 008–012 Proposed, no code yet)

Survey finding: OpenAI `tools`/`tool_calls` work ONLY on raw forward passthrough;
every enveloped backend drops them — structural, at the string-only `Backend`
contract (`backend.go:20`), `api.Message`/`RespMessage` have no tool fields, and an
explicit `conversation_id` flips even a forward model into the lossy enveloped path
(`server.go:211-219`). Five ADRs (all **Proposed** — awaiting owner OK, no code):
008 structured `Result` contract + tool wire types (foundation) · 009 request
fidelity (sampling params/response_format/images, explicit-never-silent drops via
`X-Dropped-Params`) · 010 toolnexus client-declared tool relay (builtins stay off;
toolnexus v0.10.0 already ships ToAnthropic/ToGemini adapters — open question is
declaration-only/no-execute mode, spike 004) · 011 CLI-agent tool calling — Phase A
schema-constrained emulation, **live-proven in spike 002**: `claude -p --json-schema`
(inline JSON, not a file) emitted a valid `get_weather` tool_call and completed the
tool-result round trip on haiku, one-shot, no process persistence; Phase B MCP
trampoline (park the MCP call across HTTP turns) gated on spike 003 · 012 pull-worker
tools (wire-additive job/answer fields + register-time capabilities, spike 005).
Spikes: docs/spikes/002–005. Index: docs/adr/README.md "Tool-calling track".
Spike 002 round 2 (2026-07-29, live): **all four CLIs** (claude/codex/copilot/devin)
emitted a correct 3-call parallel tool batch with exact args; claude+devin also
completed the tool-result→answer turn (devin via native `-r <sid>` resume — best
continuation path). Gotchas recorded: codex needs OpenAI-strict schemas
(`arguments` as JSON-string = OpenAI's own wire shape) and is minutes-slow;
copilot needs fence-extraction past its terminal footer; devin needs a trusted
cwd. ADR-011 updated: `emulated` default for all four, parallel calls in v1.
Round 3 (same day): 10-tool adversarial catalog (near-duplicate names, enums,
arrays, ISO codes, a search→book dependency) — **zero failures** on every CLI
tested: 6-call bursts argument-exact on all four; dependency trap passed (search
only, no invented flight_id) on claude/copilot/devin; distractor answered without
tools; devin completed the full search→book chain across turns via session
resume. One lesson: resume-by-most-recent-session grabs the wrong conversation —
per-conversation session mapping (already in devin.go) is mandatory.

## Tool calls SHIPPED on the queue path + interactive-worker pattern (2026-07-29)

Owner-directed fast build (ADR-008/012 minimal slices now Accepted): `api.ToolCall`/
`api.Result`, `Message` gains tool_calls/tool_call_id/name, `ChatRequest` gains
tools/tool_choice; new optional `backend.ResultBackend` (only QueueBackend implements)
— server envelope prefers it and emits `tool_calls` + `finish_reason:"tool_calls"` in
both non-stream and stream (indexed deltas via NewToolChunk/FinishChunk); queue Job
carries tools, worker answer POST accepts `{content, tool_calls}` (OpenAI or
simplified `{name,arguments}` shape, server normalizes + synthesizes call ids). All
other backends byte-identical. vet+tests green.
**E2E live-proven (spike 006)**: opencode → routsi `devin-live` queue → interactive
devin TUI (ghostty-sendkeys, `--permission-mode dangerous` + accept-edits) driven by a
file-handoff driver (`job-<id>.md` in, `answer-<id>.json` out, 45s re-nudge for
free-plan throttle) → devin emitted `write` tool_call → **opencode executed it and a
150-line localStorage todo app landed in opencode's cwd** → tool result round-tripped
→ devin's final text rendered. Key findings: `devin -p` is DEAD for big prompts
(~80KB SIGPIPE ceiling, argv and --prompt-file alike; stdin panics) — opencode system
prompts exceed it; single-threaded driver loses concurrent jobs to the 5-min maxWait
(opencode fires title+main together — needs poll-while-busy or per-slot queues);
`pkill -f opencode` kills routsi if the config path contains "opencode". Extended same day: **all three CLI agents run as interactive workers
simultaneously** — devin-live, claude-live, codex-live queues on one routsi, each a
ghostty TUI + parameterized driver (`worker-driver.js <sendkeys> <spool> <queue>
<workdir>`, lives in ~/oce for now, NOT yet in-repo). opencode 5-step task per
worker: devin 10 rounds, claude 7 (skipped style.css and said so), codex 14 (plan-
tracked via todowrite, all 5 steps). Automation traps in spike 006: kill opencode by
walking the ghostty process tree (pattern kills hit routsi / miss the TUI);
opencode's external-dir permission modal ignores injected keys — launch from a real
project root, avoid symlinked cwds; session db at
~/.local/share/opencode/opencode.db (move aside to reset sticky model/sessions,
auth.json separate). Next: promote the driver into `routsi worker` / the
routsi-worker skill (ADR-005 extension).

## 2026-07-30 (later) — `routsi worker join` shipped; ADR-010 blocked upstream

`routsi worker join` (cmd/routsi/join.go, wired in skills.go dispatch) productizes the
interactive-worker pattern: registers the queue, long-polls, writes each job (transcript
+ tool schemas, spike-006 format) to `job-<id>.md` in --workdir, runs a user --notify
command (sh -c; env ROUTSI_JOB_ID/JOB_FILE/ANSWER_FILE; re-run every --nudge, default
45s) until the agent writes `answer-<id>.json` ({"content"} or {"tool_calls"} — anything
else wrapped as content), POSTs it back; --job-timeout 8m default. Live-verified with a
HEADLESS notify (`claude -p --dangerously-skip-permissions "Read $ROUTSI_JOB_FILE…"`) —
tool_calls response end-to-end, no TUI needed. Docs switched to join (example README,
opencode.md, SKILL.md; worker-driver.js kept as prototype). Spike 004 resolved: toolnexus
v0.10.0 `Ask` hard-executes tool_use (client.go:1173, :1763), no declaration-only mode ⇒
**ADR-010 blocked on upstream toolnexus relay mode** — file issue there before accepting.
Still open: ADR-009 (request fidelity), ADR-011 Phase B (MCP trampoline, spike 003),
`tools: off|emulated` knob + X-Tool-Mode header, worker capabilities-at-register.

## 2026-07-30 — ADR-011 Phase A shipped + opencode/decider docs

One-shot CLI agents (devin/codex/claude/copilot) now emit tool_calls: shared
fenced-JSON emulation in `internal/backend/toolemu.go` (`buildToolPrompt` +
`parseToolReply`, parse-failure ⇒ plain text, never a fabricated call);
`CompleteResult` on CLIAgent + Devin (both now `backend.ResultBackend`);
`renderTranscript` renders tool_calls/tool-result turns. Table tests in
toolemu_test.go; live-verified through the proxy (claude-oneshot: tool_call out,
tool-result round trip back to text). Deviation from ADR-011 as written: fenced
JSON for ALL four agents (no per-agent --json-schema plumbing yet; the
schema-constrained variant + `tools: off|emulated` config knob + X-Tool-Mode
header remain open). New docs: docs/opencode.md (opencode with one-shot vs
interactive-worker flavors + agent-routing decider), examples/decider-agents.js
(custom JS decider → dynamic group levels map to agents/queues; tested).

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
