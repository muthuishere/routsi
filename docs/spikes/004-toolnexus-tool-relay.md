# Spike 004: toolnexus declaration-only tool relay

De-risks **ADR-010**. Status: **partially verified** (API surface read 2026-07-29,
`toolnexus/golang v0.10.0` in module cache); live round trip not yet run.

## Question

Can toolnexus relay *client-declared* tool schemas to Anthropic/Gemini and surface
the model's `tool_use` back to routsi **without executing anything proxy-side**?

## Evidence so far (module source, `golang@v0.10.0`)

- Format adapters already exist: `ToOpenAI` / `ToAnthropic` / `ToGemini`
  (`adapters.go:41,57,70`) convert a `[]Tool` into each provider's native tool
  declaration shape — the translation half of ADR-010 is already written upstream.
- `CreateBuiltinTools()` (`builtin.go:1029`) is what `Builtins: false` suppresses —
  confirming builtins and client-declared tools are separable concepts in the
  library, matching ADR-010's distinction.
- Open: toolnexus `Tool` couples a schema **with a handler** (it's an executor
  toolkit). The unknown is whether `Ask`/the agent loop supports a
  *declaration-only* tool — i.e. stops and returns the model's `tool_use` instead
  of invoking a handler — or whether a no-op handler + loop-depth-0 config can fake
  it.

## Resolution (2026-07-30, source-verified)

`Ask`'s agent loop **hard-executes** every `tool_use` block the model returns
(client.go:1173 — "Execute all tool_use blocks in this turn concurrently") and
loops until `stopReason != "tool_use"` (client.go:1763). There is no
declaration-only/no-execute mode. **ADR-010 is blocked on upstream toolnexus
work** (a relay mode that surfaces tool_use instead of executing) — file it in
the toolnexus repo before accepting ADR-010. The experiment plan below stands
for once that mode exists.

## Experiment plan

1. Register a `Tool` whose handler returns a sentinel error / never runs; call
   `Ask` against a live Anthropic model (OpenRouter won't do — this path exists
   precisely for native Anthropic/Gemini) with a prompt that forces a call.
2. Inspect what `Ask` returns: raw `tool_use` block? handler invocation? Does the
   response object expose the call's id/name/arguments?
3. Check whether the ConversationStore round-trips a `tool_use` + `tool_result`
   pair structurally, or only text (`res.Text`).
4. If `Ask` insists on executing: measure the cost of dropping to toolnexus's
   lower-level client (`client.go`) for the tool-carrying case, keeping `Ask` for
   memory-only.

## Success criteria

Model's tool call reaches routsi as structured data (id/name/args) with zero
proxy-side execution, and a synthetic `tool_result` fed back continues the
conversation correctly.

## If it fails

ADR-010 needs upstream toolnexus work first (a `RelayTools`/no-execute mode) —
that's a muthuishere-owned repo, so feasible, but it changes sequencing: file the
toolnexus issue before accepting ADR-010.
