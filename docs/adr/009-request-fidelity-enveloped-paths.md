# ADR-009: Request fidelity on enveloped paths (params, response_format, images)

- **Status:** Proposed
- **Date:** 2026-07-29
- **Deciders:** owner + routsi

## Context

Beyond tools (ADR-008), enveloped backends drop every non-text request feature:

- `temperature`, `top_p`, `stop`, `max_tokens`, `response_format` are not modeled on
  `api.ChatRequest` (`internal/api/types.go:42-49`) — they survive only in `req.Raw`
  and no enveloped backend reads them.
- Multimodal content parts vanish: `Message.Text()` (types.go:21-38) concatenates only
  `.text` parts — an `image_url` part contributes an **empty string**, silently.
- The raw `Forward` path passes all of this through; the same model behind a
  `conversation_id` (server.go:211-219) loses it all.

## Decision

1. Model the common sampling params on `ChatRequest` (`Temperature *float64`,
   `TopP *float64`, `Stop json.RawMessage`, `MaxTokens *int`,
   `ResponseFormat json.RawMessage`) — decoded once, available to every backend.
2. **Per-backend policy is explicit, never silent** (extends ADR-008's capability
   declaration):
   - *toolnexus translator*: map params through the toolkit where its API accepts
     them; `response_format: json_*` → provider-native JSON mode if available.
   - *CLI agents*: params that can't map (temperature on `claude -p`, etc.) follow a
     config knob `unsupported_params: ignore|reject` (default `ignore`, but the
     selected-model response header is joined by `X-Dropped-Params` listing what was
     ignored — observable, not silent).
   - *workers*: params ride along in the job payload (ADR-012); the worker decides.
3. Multimodal: `Message.Text()` stops lying. Content-part arrays are preserved as
   structured parts; backends that are text-only either reject image parts (400,
   per the same `unsupported_params` knob) or receive a `[image omitted]` marker —
   never a silent empty string.

## Alternatives

- **Pass `req.Raw` to every backend and let each re-parse** — N parsers, N drift
  bugs. Rejected.
- **Reject everything unmapped** — breaks SDK defaults (many SDKs always send
  `temperature`). Rejected as default; available via `reject`.

## Consequences

- `X-Dropped-Params` makes the proxy's lossiness auditable — dashboards/metrics can
  count drops per model.
- Usage estimation can account for image parts (flat per-image token constant).

## Open questions

- Should `response_format: json_schema` on CLI agents map to `claude --json-schema` /
  `codex --output-schema`? (Natural fit — fold into spike 002.)
