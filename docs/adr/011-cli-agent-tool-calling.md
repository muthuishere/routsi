# ADR-011: Tool calling for CLI agents (claude / codex / devin / copilot)

- **Status:** Proposed
- **Date:** 2026-07-29
- **Deciders:** owner + routsi

## Context

CLI agents flatten everything to one prompt string (`renderTranscript`,
`internal/backend/devin.go:138-149`; `cliagent.go:53-57`) and return stdout text.
A client sending OpenAI `tools` gets them silently dropped.

The impedance mismatch is real: OpenAI tool calling is **client-executed** — the
model emits `tool_calls`, the HTTP client runs the tool and posts a `tool` result
message. A CLI agent is the opposite: it *is* an executor, expecting to run tools
itself (via MCP) inside one process run. Bridging means making the agent *emit* a
call and *wait a whole HTTP turn* for the result.

Capability evidence (local CLIs, checked 2026-07-29):

| CLI | MCP | Structured output |
|---|---|---|
| claude | `--mcp-config`, `--allowedTools` | `--json-schema`, `--output-format json` |
| codex | MCP config supported | `--output-schema <FILE>`, `--json` events |
| devin | `devin mcp` subcommand, ACP server mode | — (text) |
| copilot | `--additional-mcp-config`, GitHub MCP toolsets | — |

## Decision

Two mechanisms, phased:

**Phase A (v1) — structured-output emulation** for claude + codex:
1. When a request carries `tools`, the backend injects a rendered tool manifest into
   the prompt ("You may call these functions; to call one, answer ONLY with …") and
   constrains the reply with `claude --json-schema` / `codex --output-schema` to a
   union schema: `{answer: string} | {tool_call: {name, arguments}}`.
2. A `tool_call` reply becomes an OpenAI `tool_calls` response (ADR-008 `Result`,
   `finish_reason:"tool_calls"`), with a generated `call_<id>`.
3. The client's follow-up `tool` message is rendered into the next prompt
   (`renderTranscript` learns `tool` role + `tool_calls` turns) — one-shot CLIs need
   no process persistence; sessioned CLIs (devin `-r`) just continue.
4. devin/copilot (no schema flag): same prompt protocol via a **fenced-JSON
   contract** — spike 002 round 2 proved both return clean, parse-valid fenced
   blocks including 3-call parallel batches, so they are default-on `emulated`
   too (not best-effort). On parse failure the text is returned as a plain
   answer, never a fabricated call. devin's turn 2 rides its **native session
   resume** (`-r <sid>`), no transcript re-render — the cleanest continuation of
   the four; copilot output needs fence-extraction past its terminal footer.
5. **Parallel calls are v1**: the schema's `tool_calls` is an array; all four CLIs
   emitted correct 3-call batches (spike 002 round 2). codex requires the
   OpenAI-strict schema rendering (`additionalProperties:false`, all-required,
   `arguments` as JSON-string — which is OpenAI's own wire shape), so the backend
   keeps two schema renderings: lax (claude) and strict (codex). codex turns are
   slow (minutes) — per-agent timeout config required.

**Phase B (later) — MCP trampoline**: routsi runs an ephemeral MCP server exposing
the client's declared tools; the CLI is launched with it attached; when the agent
invokes a tool, the MCP handler *blocks*, routsi returns `tool_calls` to the HTTP
client, and the matching follow-up request (conversation id + `tool_call_id`)
resolves the blocked MCP call with the result. True agentic multi-call turns, at the
cost of holding the CLI process across HTTP turns (timeout/eviction policy needed).
Gated on spike 003 proving the blocking round-trip.

Config: `tools: off|emulated|mcp` per agent model entry; default `emulated` for
**all four** agents (with ADR-008's explicit 400 when off).

## Alternatives

- **Reject tools on all CLI agents** — honest but makes agents second-class in the
  catalog; emulation is cheap and schema-constrained output makes it reliable.
  Kept only as the `off` setting.
- **MCP-only (skip emulation)** — process-lifetime management is the hard part;
  don't make it the entry price. Rejected for v1.

## Consequences

- Emulated tool calls consume agent turns — a 3-call chain = 3 CLI invocations
  (devin sessions amortize; one-shot claude re-renders the transcript each time).
- The agent could ignore the protocol; schema constraint makes that near-impossible
  on claude/codex, and the fallback (treat as text) is safe.
- `X-Selected-Model` disclosure joined by `X-Tool-Mode: emulated|mcp` for
  observability.

## Open questions

- ~~Parallel tool calls in one turn~~ — resolved by spike 002 round 2: array
  schema, all four CLIs emit correct multi-call batches; v1 supports it.
- Does `claude --json-schema` compose with `--mcp-config` in the same run (Phase B
  hybrid)? → spike 003.
