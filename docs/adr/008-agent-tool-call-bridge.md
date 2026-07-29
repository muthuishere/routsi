# ADR-008: Tool-call passthrough for enveloped backends via a full agent bridge

- **Status:** Proposed
- **Date:** 2026-07-29
- **Deciders:** owner + routsi
- **Relates to:** the enveloped path (`internal/server` `envelope`), the
  `Backend` interface (`internal/backend`), the never-expose-shell-tools
  invariant (`internal/backend/toolnexus.go`).

## Context

routsi is OpenAI-wire-compatible, which is what lets coding agents like
**opencode** point at it as a custom provider. Coding agents live on
**function/tool calling**: they send `tools:[...]` and expect the model to
emit `tool_calls` with `finish_reason:"tool_calls"`, then they execute and
send back `role:"tool"` results.

Two forwarding paths exist today:

- **Raw forward (concrete model = hard bypass).** `internal/backend/forward.go`
  relays the request bytes untouched, so `tools`/`tool_calls`/`tool_choice`
  survive — the *upstream* does the tool calling. opencode already works here.
- **Enveloped path (`auto`, dynamic groups, and all agent/translated
  backends).** The `Backend` interface is **text-only**:

  ```go
  Complete(ctx, req) (string, error)
  Stream(ctx, req, emit func(delta string)) error
  ```

  The envelope (`internal/server/server.go` `envelope`) wraps that string into
  a `chat.completion` whose only response field is `content`
  (`api.RespMessage` = `role`+`content`). **There is no channel for
  `tool_calls` anywhere on this path.** So the moment opencode routes to an
  agent-backed model, tool calling silently vanishes.

The owner's chosen direction (2026-07-29) is the **full agent bridge**:
surface an agent's *internal* tool activity (devin/codex/claude/copilot
running shell/file/etc. inside their own sandbox) to the wire client as
OpenAI-shaped tool events, rather than only the final text.

This ADR exists because two things must be resolved before any code, and both
are easy to get wrong:

1. **The invariant.** CLAUDE.md and `toolnexus.go` are emphatic: *a translator
   must never expose shell/file tools* (`Builtins: false`). A naive "bridge"
   looks like it violates this.
2. **The protocol.** OpenAI's `finish_reason:"tool_calls"` is a **control
   handoff**: it means *"client, your turn — execute these and reply."* An
   autonomous agent has **already executed** its calls. If routsi replays them
   as actionable `tool_calls`, opencode would **re-run** them — re-running a
   `bash rm`, a file write, or a git push the agent already performed. That is
   a correctness and safety bug, not a cosmetic one.

## Decision

### 1. The bridge is *narration*, not *delegation* — and that resolves the invariant

The never-expose-shell-tools invariant means: **routsi must never present
shell/file tools to a wire party as tools that cause routsi (or an upstream on
routsi's behalf) to execute them.** The agent bridge does the opposite of
exposing executable tools — it **reports already-completed actions** the
configured agent ran, autonomously, inside its own sandbox. routsi executes
nothing new; the wire client cannot cause any execution through the bridge.

We make this concrete with one hard rule:

> **Bridged agent tool events are emitted as a completed, non-actionable
> trace. routsi never emits `finish_reason:"tool_calls"` for an agent's own
> internal activity.** The turn still ends with `finish_reason:"stop"` and the
> agent's final text in `content`.

That single rule resolves the protocol problem *and* keeps the invariant:
opencode receives a faithful, richly-structured trace of what the agent did,
renders it in its steps/timeline UI, and is **never asked to execute
anything** — so nothing gets double-run.

### 2. Wire shape: reasoning trace, with an optional replay-as-completed-calls mode

The bridge surfaces each agent step (tool name, arguments, result/observation)
over two OpenAI-compatible channels, selected by config:

- **`reasoning` (default).** Stream steps as `reasoning_content` deltas (the
  channel the AI SDK — hence opencode — renders as a thinking/steps trace).
  Maximum client compatibility; zero risk of accidental execution because
  reasoning is never actionable. Final answer stays in `content`.
- **`tool_calls_trace` (opt-in).** Additionally emit each step as an assistant
  `tool_calls` entry *immediately followed, in the same response, by its
  `role:"tool"` result* — a self-contained completed pair the client displays
  but is never asked to act on. Non-standard rendering across clients; behind a
  flag, off by default, documented as best-effort.

Both modes are **disabled by default**. Text-only behavior is byte-for-byte
unchanged unless a model opts in.

### 3. Make `Backend` tool-aware (structurally), backward-compatibly

Introduce a structured result so a backend can report steps + final answer
without every backend caring:

```go
// Step is one completed agent action (already executed inside the agent).
type Step struct {
    ID     string          // synthetic call id
    Name   string          // tool name as the agent reported it
    Args   json.RawMessage // arguments, best-effort
    Result string          // observation/output, truncated
}

// Result is what a Backend produces. Text is the final answer; Steps is the
// agent's internal activity (empty for non-agent backends).
type Result struct {
    Text         string
    Steps        []Step
    FinishReason string // defaults to "stop"
}
```

`Backend` grows **one** optional method via a new interface, so existing
backends compile unchanged:

```go
// StepReporter is implemented by backends that can surface internal steps.
// Backends that don't implement it are treated exactly as today (text-only).
type StepReporter interface {
    CompleteWithSteps(ctx, req) (Result, error)
}
```

The envelope type-asserts `StepReporter`; absent it, the current
`Complete`/`Stream` text path runs untouched. No churn to forward, queue,
Completer, or the toolnexus text path.

### 4. Which backends bridge — gated by the spike (ADR gate 2)

Capturing internal steps requires the agent CLI to *emit* them. Feasibility
differs per agent and MUST be proven by a spike before implementation:

- **claude** (`-p`): `--output-format stream-json` emits structured
  `tool_use`/`tool_result` events — most promising.
- **codex** (`exec`): has a JSON event stream; needs verification it carries
  tool steps, not just the final message.
- **devin**: session/transcript API may expose step structure.
- **copilot**: likely final-text-only; would remain text-only, and that's fine.

Any agent whose CLI can't emit capturable steps stays text-only — no
degradation from today.

### 5. Config

Per-model opt-in, plus a global default:

```yaml
models:
  - name: devin-bridge
    type: devin
    tool_bridge: reasoning        # off (default) | reasoning | tool_calls_trace
```

`tool_bridge` absent/`off` ⇒ today's text-only envelope, unchanged.

## Alternatives considered

- **Translate-where-sound (client-driven tools on the toolnexus path only).**
  Genuine OpenAI function-calling by translating client `tools` → Anthropic/
  Gemini tool schema → `tool_use` → back to `tool_calls`, letting the client
  drive. Semantically the *cleanest* and it's real client-driven tool calling —
  but it only works for real-LLM translated upstreams, **not** for autonomous
  CLI agents (which own their loop and can't stop-and-hand-off). Rejected as
  *the* answer because the owner wants agent-level activity surfaced; retained
  as a **complementary future ADR** for the toolnexus path.
- **Prompt-convention tool-calls for CLI agents.** Inject client tool schemas
  into the agent prompt, ask it to emit a JSON `tool_calls` block, parse it
  back. Brittle, fights the agent's own tool loop, and still hits the
  double-execution trap if surfaced as actionable. Rejected.
- **Replay agent calls as actionable `tool_calls`.** Rejected outright: causes
  the client to re-execute already-performed side effects (the core safety
  bug). This is the anti-pattern the ADR is written to prevent.

## Consequences

- opencode (and any AI-SDK client) gets a real, structured view of what an
  agent-backed model did — not just an opaque final blob — while remaining a
  passive observer, so no side effect is ever double-run.
- The `Backend` contract gains an optional structured surface without breaking
  a single existing backend.
- Estimated-usage accounting must fold step text into token estimates.
- Streaming: steps interleave with the existing heartbeat-guarded SSE writer;
  all writes stay on the handler goroutine (the existing invariant).
- We are **not** delivering client-driven tool calling for agents (it's
  semantically impossible for an autonomous agent); we're delivering faithful
  agent-activity transparency. The README/docs must state this plainly so
  users don't expect opencode to *drive* devin's tools.

## Open questions

1. **Result truncation.** How much of each tool observation to surface before
   truncating (full output can be huge)? Propose a per-step byte cap.
2. **codex/devin step fidelity.** Do their JSON streams carry arguments +
   results, or only tool names? Spike decides how rich the trace can be.
3. **Non-streaming requests.** Emit steps as a `reasoning_content` field on the
   single `message`, or fold a compact trace into `content`? Lean: a
   `reasoning_content` field mirroring the streaming channel.
4. Should `auto`/dynamic routing to a bridged member inherit the member's
   `tool_bridge` setting, or be forced off unless the group opts in?
