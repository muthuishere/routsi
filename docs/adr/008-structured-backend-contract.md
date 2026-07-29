# ADR-008: Structured backend contract + tool-call wire types

- **Status:** Accepted — minimal slice shipped 2026-07-29 (owner-directed): `api.Result`/`ToolCall` types, optional `backend.ResultBackend`, envelope `tool_calls` (stream+non-stream). Full Backend-interface migration still open.
- **Date:** 2026-07-29
- **Deciders:** owner + routsi

## Context

Tool calling (OpenAI `tools` / `tool_calls`) works on exactly one path today: the raw
`Forward` relay (`internal/backend/forward.go:34-43`), which passes the whole body
through untouched. Every enveloped backend drops it, because the loss is structural:

- The `Backend` interface is **string-only**: `Complete(ctx, *ChatRequest) (string, error)`
  (`internal/backend/backend.go:20`). There is no slot for a backend to *return* a tool
  call, whatever the upstream could do.
- `api.Message` (`internal/api/types.go:14-17`) has only `role`+`content` — no
  `tool_calls`, `tool_call_id`, or `name`, so incoming assistant-tool-call turns and
  `tool` result turns can't even be represented; `Message.Text()` (types.go:21-38)
  flattens them to text.
- `api.RespMessage` (types.go:100-103) has no `tool_calls`; `NewCompletion`/`NewChunk`
  hardcode `finish_reason:"stop"` (types.go:106, 119).
- Worse, an explicit `conversation_id` flips even a *forward* model into the enveloped
  path (`internal/server/server.go:211-219`) — so the same model silently loses tools
  the moment the client opts into proxy-managed memory.

ADR-009..012 (fidelity, translator, CLI agents, workers) all need somewhere to put
tools. This ADR is that foundation.

## Decision

1. **Extend the wire types** (`internal/api`):
   - `Message` gains `ToolCalls []ToolCall`, `ToolCallID string`, `Name string`
     (all `omitempty`). New `ToolCall{ID, Type, Function{Name, Arguments}}`.
   - `ChatRequest` gains `Tools json.RawMessage` and `ToolChoice json.RawMessage`
     (opaque relay — routsi never interprets tool schemas).
   - `RespMessage` gains `ToolCalls []ToolCall` and `Content` becomes a `*string`
     (OpenAI emits `content: null` alongside tool calls).
2. **Replace the string result with a structured one**:
   ```go
   type Result struct {
       Text      string
       ToolCalls []api.ToolCall
       FinishReason string // "" ⇒ "stop"
   }
   Complete(ctx, *ChatRequest) (Result, error)
   Stream(ctx, *ChatRequest, func(Delta)) error  // Delta = text delta OR tool-call delta
   ```
   Backends that stay text-only return `Result{Text: s}` — mechanical migration.
3. **Envelope builders** (`NewCompletion`/`NewChunk` and the streaming path in
   `server.go:429-521`) accept `Result`/`Delta`, emit `tool_calls` and the correct
   `finish_reason` (`tool_calls` when calls are present).
4. **Capability declaration:** each backend reports `SupportsTools() bool` (or a
   capability struct). When a request carries `tools` and the resolved backend
   doesn't support them, routsi returns a clear 400 (`"model X does not support
   tools"`) rather than silently dropping — silent loss is the current bug class.

## Alternatives

- **Keep string contract, sniff JSON out of text** — fragile, ambiguous, leaks
  emulation details into every backend. Rejected.
- **Raw-bytes contract for all backends** — makes translators/CLI agents re-parse
  everything; loses the typed envelope that memory/metrics rely on. Rejected.

## Consequences

- One-time mechanical churn across `internal/backend/*` and the worker CLI.
- Purely additive on the wire: text-only responses serialize byte-identically
  (guard with golden tests).
- `EstimateUsage` (types.go:82) should start counting tool schema/argument bytes.

## Open questions

- `Content *string` vs keeping `string` + custom marshal — decide in spike 002.
- Streaming tool-call deltas (OpenAI's index/argument-fragment shape) needed in v1,
  or non-stream-only first? Recommend: non-stream first, stream in a follow-up.
