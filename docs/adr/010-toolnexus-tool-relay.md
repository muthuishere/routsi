# ADR-010: Client-declared tool relay through the toolnexus translator

- **Status:** Proposed — **mechanism settled, unblocks when toolnexus ships F1-a/F2-a in the Go port** (upstream ADR-0010 Accepted; see spike 004). Relay is a *use of the §10 suspend/resume primitive*, not a new translator path — the Decision below is amended accordingly.
- **Date:** 2026-07-29
- **Deciders:** owner + routsi

## Context

The toolnexus backend (`internal/backend/toolnexus.go`) translates OpenAI-shaped
requests to Anthropic/Gemini upstreams. It creates its toolkit with `Builtins: false`
(toolnexus.go:34) under the doctrine "a translator must never expose shell/file
tools" — and currently that doctrine is implemented by dropping **all** tools,
including the client's own declared function schemas.

Those are two different things:

- **Builtins** — tools *executed by toolnexus on the proxy host* (shell, file I/O).
  Exposing these from a proxy is a security hole. Stays off. Non-negotiable.
- **Client-declared tools** — schemas the *caller* sends in `tools:`; the model only
  *emits a call*, and the **client executes it**. Nothing runs on the proxy. Relaying
  these is exactly what the raw forward path already does safely.

Dropping the latter means an Anthropic model behind the translator can never do
function calling, while the same request to OpenRouter passthrough works.

## Decision

1. Relay `req.Tools`/`req.ToolChoice` (ADR-008 types) through toolnexus's
   translation layer to the provider's native tool format (Anthropic `tools` /
   `tool_use`, Gemini `functionDeclarations`).
2. Translate responses back: provider `tool_use` blocks → OpenAI `tool_calls` in the
   ADR-008 `Result`; `tool` role messages / `tool_call_id` results in the request →
   provider-native tool-result blocks (replacing today's text flattening in
   `split()`, toolnexus.go:89-108).
3. `Builtins: false` stays. A config guard additionally rejects any client tool
   whose name collides with a toolnexus builtin name, so a future toolkit change
   can't be socially engineered into executing something proxy-side.
4. ~~Proxy-managed memory (`Ask` + ConversationStore) must store tool-call/tool-result
   turns structurally~~ — **STRUCK 2026-07-30: already true upstream, no work needed.**
   `Ask` persists `res.Messages` verbatim (`client.go:648`) and the loop stores raw
   provider `tool_use` blocks with ids (`client.go:1153-1159`). Verified in spike 004.

**Amended mechanism (2026-07-30).** Implement relay as a **declaration-only tool over
§10 suspend/resume**, not as a bespoke pass-through in the translator: each client tool
becomes a toolkit tool that returns `Pending(Request{…})` on first call, so the run
halts with the model's call as structured data and nothing executes proxy-side; on the
next HTTP turn routsi resumes with the client's tool results as the `Answer`, which
become real `tool_result` blocks. This reuses one hardened primitive instead of adding a
second mechanism, and works on the Anthropic-native loop routsi translates to. Requires
upstream **F1-a** (`RunWithAnswer`/`Ask(…, answer)`, filling every outstanding
`tool_result` slot and replacing the placeholder error) and **F2-a** (one `Request`
carrying all N parallel calls), plus a routsi-side bump from `toolnexus/golang@v0.10.0`
to a version that has the primitive (≥0.11.0).

## Alternatives

- **Keep dropping; tell users to use passthrough for tool use** — defensible short
  term, but proxy-managed memory + tools then can't coexist at all. Rejected.
- **Emulate via prompt-encoding** (ADR-011's technique) — pointless here; these
  upstreams have native tool APIs. Rejected.

## Consequences

- Depends on ADR-008 landing first (done — minimal slice shipped).
- Depends on toolnexus F1-a + F2-a in the Go port, and on routsi bumping the toolnexus
  dependency to ≥0.11.0. Declaration translation (`ToAnthropic`/`ToGemini`) and the
  ConversationStore round-trip are already free upstream.
- The relay tool set is per-request (the client declares tools per call), so the toolkit
  must be built per request rather than cached per model.

## Open questions

- ~~Does toolnexus's `Ask` conversation store round-trip non-text blocks today?~~
  **Answered: yes, verified — no upstream work** (spike 004).
- Anthropic transcript replayability: on a durable halt the assistant message keeps all
  N `tool_use` blocks while the follow-up carries only the first `tool_result`, which
  Anthropic rejects. Upstream has this filed as an observation; routsi's replay shape is
  exactly what triggers it, so confirm it is fixed on the relay path before shipping.
