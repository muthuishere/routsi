# ADR-010: Client-declared tool relay through the toolnexus translator

- **Status:** Proposed
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
4. Proxy-managed memory (`Ask` + ConversationStore) must store tool-call/tool-result
   turns structurally so multi-turn tool use survives the transcript.

## Alternatives

- **Keep dropping; tell users to use passthrough for tool use** — defensible short
  term, but proxy-managed memory + tools then can't coexist at all. Rejected.
- **Emulate via prompt-encoding** (ADR-011's technique) — pointless here; these
  upstreams have native tool APIs. Rejected.

## Consequences

- Depends on ADR-008 landing first.
- Depends on the toolnexus toolkit exposing pass-through tool declarations — spike
  004 verifies its API surface before this ADR can be Accepted.

## Open questions

- Does toolnexus's `Ask` conversation store round-trip non-text blocks today, or
  does it need upstream (toolnexus repo) work? → spike 004.
