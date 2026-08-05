# ADR-013 — The adapter contract: one schema, three transports

Status: **Accepted** (owner-directed, 2026-08-05 — ceremony compressed, ADR + implementation
in one pass, as with ADR-007/008)

## Context

routsi hardcodes one Go backend per agent CLI: `devin`, `codex`, `copilot`, `claude`
(`config.ModelType`, `internal/backend/cliagent.go`, `devin.go`). Every new agent costs a
Go type, a `case` in `config.validate`, a `case` in `server.New`, flag plumbing in
`runPrompt`, and a routsi release. That does not scale: OpenClaw, opencode,
claw-orchestrator, workflow runners, and in-house scripts are all "something that can
answer a chat turn", and none will ever be a routsi `ModelType`.

It is also the wrong place to compete. Competitive research (2026-08-05, benchmarked on
this machine) found ccproxy-api already exposes Claude/Codex/Copilot with native tool
calling, MCP servers and real permissions via `claude-code-sdk` — strictly better than our
`claude -p` one-shot with fenced-JSON tool emulation. Meanwhile the one thing no
competitor has is the *inbound* direction: a backend with no URL — a live agent session, a
TUI, a human — answering as a model (spike 006). Vendor-specific CLI code in core is
maintenance spent on ground we lose, crowding out the ground we own.

Separately: an adapter must be able to spawn a process and hold a session. That rules out
the obvious "make plugins fast" answer — see Alternatives.

## Decision

Define **one adapter contract** — a single JSON job/answer schema — carried over **three
transports**. Core keeps only the HTTP surface, routing, the OpenAI envelope + tool wire,
metrics and auth. Everything vendor-specific moves out of the binary.

| transport | type | adapter is | spawn cost | fits |
|---|---|---|---|---|
| **exec** | `command` | a script routsi runs per request | ~1–5ms | one-shot CLIs, JS files, workflows |
| **socket** | `adapter` | a long-lived sidecar on a unix socket | zero | anything stateful — sessions, a TUI, a browser |
| **queue** | `queue` | a worker that dials *in* (ADR-001) | zero | remote / interactive agents — **already shipped** |

socket and queue are the same idea with the polarity flipped: one routsi dials out, the
other the worker dials in. The queue half already works, so this ADR implements **exec**
first and specifies socket for later.

### The job (routsi → adapter)

Field-compatible with `queue.Job` (`internal/queue/queue.go:30`) so all three transports
carry one schema, plus three fields an exec adapter needs:

```json
{ "id": "job-1", "model": "openclaw", "upstream_model": "deep",
  "conversation_id": "c-1", "stream": false,
  "prompt": "<rendered transcript>",
  "messages": [...], "tools": [...], "tool_choice": null }
```

`prompt` is the same rendering the CLI-agent backends feed `-p` today, so an adapter that
ignores `messages` entirely is still correct — that is what makes a ten-line wrapper
viable. `messages` remains the full-fidelity path.

### The answer (adapter → routsi)

Either a JSON object, or anything else (taken verbatim as the answer text):

```json
{"content": "...", "tool_calls": [{"name": "get_weather", "arguments": {"city": "Paris"}}]}
```

Both the OpenAI wire shape (`{"function":{"name","arguments":"<json string>"}}`) and the
simplified shape are accepted — the same normalization the pull-worker answer endpoint
already performs, now shared so all three transports agree.

### exec specifics

```yaml
- name: openclaw
  type: command
  command: node ./adapters/openclaw.js   # sh -c, cwd = workdir
  workdir: ./adapters                    # optional (default: managed cache dir)
  timeout: 5m                            # optional
  tools: native                          # native | emulated | off
```

Job on stdin, answer on stdout. Env for the child: `ROUTSI_MODEL`,
`ROUTSI_UPSTREAM_MODEL`, `ROUTSI_CONVERSATION_ID`, `ROUTSI_JOB_ID`, `ROUTSI_STREAM` — so a
trivial adapter need not parse JSON at all.

**Tool modes.** `native`: tools pass through, adapter returns `tool_calls`. `emulated`:
routsi renders the ADR-011 fenced-JSON manifest into `prompt` and parses the reply
(`toolemu.go`), giving a dumb CLI tool calling for free. `off`: a request carrying `tools`
fails with `ErrToolsUnsupported` → 400, per ADR-008 — never a silent drop.

**Streaming** is buffered: `Stream` emits the completed answer, as today's CLI agents do.
The SSE heartbeat already covers long silences.

Per the standing rule (safe defaults, nothing static): every knob above defaults to a sane
value and is overridable; no package-level state.

## Alternatives considered

- **WASM adapters (wasmtime/wazero).** Rejected, and worth recording why: **WASI cannot
  spawn a process.** The entire job of a `claude`/`devin`/`openclaw` adapter is running a
  local binary and holding its session — precisely what the sandbox forbids. WASM would
  buy µs-scale dispatch for a workload whose real cost is seconds of agent latency, in
  exchange for not being able to do the job. WASM *does* fit the **decider** (pure
  function, no I/O, every request, currently a subprocess spawn); recorded as a future
  option there, out of scope here.
- **Keep adding Go types.** Rejected: every agent becomes a routsi release, and the four
  existing ones already cost ~590 lines of flag-shaped `switch`.
- **Argument templating (`args: ["-p", "{{prompt}}"]`) as the primary contract.** Rejected:
  spike 006 measured a ~80KB argv ceiling (`devin -p` SIGPIPE) and real system prompts
  exceed it. stdin has no such limit. The bare-CLI case is served by a shipped wrapper
  instead of a second mode in Go.
- **Socket transport first.** Deferred, not rejected: exec covers one-shot adapters with
  no daemon to supervise, and the stateful case already has a working answer in the queue.

## Consequences

- The catalog is open: any binary, script, workflow runner or daemon client is one YAML
  block from being an OpenAI-addressable model, inheriting tools, stickiness,
  dynamic-group membership, `/v1/models`, metrics and auth unchanged.
- Trust boundary: `command` runs arbitrary local processes with routsi's environment and
  privileges — same trust level as `decider.command` and today's CLI agents, and
  models.yaml is already operator-owned. It must be said plainly in the README:
  **do not point `command` at anything you would not run yourself.**
- The child inherits routsi's environment, which may hold provider keys. Shipped adapters
  read only what they need and never echo them.
- Per-request process spawn is right at agent latencies (seconds) and wrong for high-QPS
  models. Documented, not enforced; the socket transport is the answer when it matters.

## Open questions

- NDJSON streaming (`{"delta":"..."}` per line) once an adapter can genuinely stream.
- A `conversation_id` → session-handle map so an adapter can resume the way `devin.go`
  does, instead of re-rendering the transcript each turn.
- Socket transport framing: newline-delimited JSON over `unix://`, or plain HTTP.
