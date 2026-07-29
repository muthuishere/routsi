# Spike 003: MCP trampoline — blocking a CLI agent's tool call across HTTP turns

De-risks **ADR-011 Phase B**. Status: **planned, not yet run** (Phase B is gated on
this; Phase A needs only spike 002).

## Question

Can routsi expose the client's declared tools as an ephemeral MCP server, have the
CLI agent invoke one, **block** that MCP call while returning `tool_calls` to the
HTTP client, and resolve it when the follow-up request arrives — without the CLI
timing out or the process table leaking?

## Capability evidence (checked 2026-07-29)

All four CLIs attach MCP servers: `claude --mcp-config` (+ `--allowedTools` to
whitelist just the trampoline tools), codex MCP config, `devin mcp`, copilot
`--additional-mcp-config`. toolnexus even ships an MCP server builder
(`mcpserve.go:80 buildMcpServer` with an `OnCall` hook) that could host the
trampoline.

## Experiment plan

1. Minimal stdio MCP server exposing `get_weather`; handler parks on a channel.
2. Launch `claude -p --mcp-config <cfg> --allowedTools "mcp__trampoline__*" "…"`;
   measure: does the CLI tolerate a handler that takes 30s / 5min? What is its MCP
   call timeout, and is it configurable?
3. Process lifetime: the `claude` process must stay alive between HTTP turn 1
   (tool_calls returned) and turn 2 (tool result posted). Measure memory per parked
   process; design eviction (conversation TTL reuses `internal/sticky` semantics).
4. Failure paths: client never sends the result (leak), client sends a mismatched
   `tool_call_id`, CLI exits mid-park.
5. Hybrid check: does `--json-schema` compose with `--mcp-config` in one run
   (final answer still schema-constrained after MCP tool use)?

## Success criteria

A parked MCP call survives ≥120s and resolves correctly; a crashed/expired park
produces a clean 504-style error to the HTTP client, not a hung request; ≤1 CLI
process per active tool-using conversation, reaped on TTL.

## Known risks going in

- CLI-side MCP timeouts may be short and non-configurable → Phase B dies here.
- One live process per conversation is a real resource model change for routsi
  (today every backend call is stateless) — if the spike succeeds, ADR-011 Phase B
  needs a process-pool section before Accepted.
