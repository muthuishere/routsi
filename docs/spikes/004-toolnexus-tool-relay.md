# Spike 004: toolnexus declaration-only tool relay

De-risks **ADR-010**. Status: **RESOLVED 2026-07-30** — mechanism identified and
proven upstream (toolnexus spikes 0001/0002); two upstream gaps + one routsi dep bump
remain. See the Resolution section below; the original experiment plan is historical.

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

## Resolution (2026-07-30) — relay is a USE of the §10 suspend/resume primitive

Superseded my first reading. The mechanism is not a new "relay mode" in the agent
loop; toolnexus already ships a no-execute-and-return path (**§10 suspend/resume**,
`resolvePending`, `client.go:1012`): a tool returns `Pending(Request{…})` and the run
halts with `status:"pending"` carrying the Request. **A declaration-only tool is one
that returns `Pending` on first call and returns the caller's output on
retry-with-answer** — a constructor plus a pinned wire shape, not a fork of the loop.
Credit: the toolnexus desk's spikes 0001 (source-verified) and 0002 (executable
stress test, `golang/relay_spike_test.go`, 14/14 green on UNMODIFIED library code).

Verified independently here, against the toolnexus source:

1. **The ConversationStore round-trip question is CLOSED — no upstream work.** `Ask`
   persists `res.Messages` verbatim (`client.go:648`) and the loop stores raw provider
   block maps including `tool_use` with `id`/`name`/`input` (`client.go:1153-1159`).
   ⇒ **ADR-010 item 4 is struck.**
2. **Declaration translation is already free** — `ToOpenAI`/`ToAnthropic`/`ToGemini`
   (`adapters.go:41,57,70`).
3. **The primitive is NOT in the version routsi pins.** `resolvePending`/`Pending`/
   `status:"pending"` are absent from `toolnexus/golang@v0.10.0` (our go.mod) and
   present in main/0.11.0. ⇒ **routsi-side prerequisite: bump the dependency.**
4. Per spike 0002, the single-call in-process case **works today unmodified**,
   including on the **Anthropic-native loop routsi actually translates to** (the
   `tool_result` references the original `tool_use` id natively) and on the streaming
   loop (a pending event is emitted, so a streaming proxy can push the call live).

## Two upstream gaps that block routsi specifically (measured, not argued)

routsi is the stateless-proxy case — each OpenAI turn is a separate HTTP request, so
it can never hold a `waitFor` closure and always takes §10's **durable** path. There:

- **F1 — no answer-carrying resume entry point.** The durable halt writes a
  *placeholder error* `tool_result` into the transcript (`client.go:1015`,
  `SPEC.md:1425-1431`, identical across all six ports) and nothing can inject an
  `Answer` into a persisted run. Fix (decided upstream, **F1-a**): add
  `RunWithAnswer`/`Ask(…, answer)` — additive. Scope sharpened by spike 0002: it must
  fill **every** outstanding `tool_result` slot of the halted turn (measured 2 of 3
  missing), and must *replace* the placeholder rather than append beside it.
- **F2 — parallel calls are dropped.** §10 deliberately surfaces only the *first*
  suspension (`pending_test.go:453,512` tests this as intended), so a 3-call turn
  relays 1. Measured on the durable path: *"3 tool_calls vs 1 tool_result (ids
  [c1])"* — identical on the Anthropic and streaming loops, so the gap is in the
  shared suspension path (one fix per port, not per provider style). Fix (**F2-a**):
  one `Request` carrying all N calls in `data.calls`. **Required, not sugar** — a
  conforming OpenAI client executes all N calls and returns N results in one
  follow-up, which is exactly the shape routsi already emits.

Status: toolnexus ADR-0010 is **Accepted** with F1-a/F2-a; implementation across six
ports is pending an owner ruling. routsi ADR-010 unblocks the day the **Go** port
lands (plus our dep bump) — the estimate is now much smaller than when filed.

## Experiment plan (historical — superseded by toolnexus spikes 0001/0002)

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
