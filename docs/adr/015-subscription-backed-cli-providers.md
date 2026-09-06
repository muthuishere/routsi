# ADR-015 — Built-in subscription-backed Claude Code and Devin providers

Status: **Accepted** (owner-directed, 2026-09-04)

Supersedes ADR-014 for Claude Code and Devin. ADR-014 continues to govern the old
one-shot CLI backends and the Codex/Copilot paths until separately reconsidered.

## Context

Routsi must expose the owner's existing Claude Code and Devin subscriptions through
its OpenAI-compatible endpoint. The required experience is the one OpenCode achieved
for ChatGPT Codex in `packages/opencode/src/plugin/openai/codex.ts`: a built-in
provider owns authentication integration, model capability filtering, request
translation, native streaming and connection lifecycle. It is not a shell command
configured by the user and it is not a separately managed API sidecar.

ADR-013/014 moved CLI integrations toward external commands and adapters because the
existing implementations were cold `-p` invocations with buffered output and emulated
tools. That result is insufficient here. It does not provide direct subscription
protocol use, native streaming, capability discovery, or one coherent provider
lifecycle inside routsi.

Spike 008 established the protocol evidence:

- Claude Code subscription traffic uses an authenticated Messages-compatible stream
  at `api.anthropic.com/v1/messages?beta=true`, accompanied by bootstrap, account,
  quota and feature/configuration calls.
- Devin uses protobuf/Connect services at `server.codeium.com`, including
  `ApiServerService/GetChatMessage`, `GetCliModelConfigs`, and seat/team policy calls,
  plus `api.devin.ai/v3/self`.
- Both existing local logins worked. Model availability is account constrained:
  Claude resolved `sonnet` to `claude-sonnet-5`; the tested Devin Free account exposed
  only `SWE-1.6 Slow` for use.

The capture proves feasibility, not protocol stability. Both integrations consume
subscription protocols rather than public API-key billing endpoints.

## Decision

Implement two first-class built-in provider modules:

```yaml
models:
  - name: claude-code-api
    type: subscription
    provider: claude-code
    upstream_model: sonnet
    auth: existing-login

  - name: devin-code-api
    type: subscription
    provider: devin-code
    upstream_model: swe-1-6-slow
    auth: existing-login
```

`type: subscription` is the common provider class. `provider` selects a built-in
implementation registered in one provider registry, analogous to OpenCode's internal
plugin registration. Do not add provider-specific cases throughout config, discovery
and `server.New`.

The model names above are ordinary Routsi catalog entries. Clients invoke them through
the existing OpenAI surface:

```http
POST /v1/chat/completions
POST /v1/responses
GET  /v1/models
```

The first implementation must retain `/v1/chat/completions` compatibility. Responses
API support may normalize into the same internal request/event model rather than grow
a second provider path.

### System shape

```text
OpenAI client
    │ chat/responses request
    ▼
Routsi HTTP auth + router + conversation policy
    ▼
subscription provider registry
    ├── claude-code provider
    │     ├── existing-login credential source
    │     ├── account/bootstrap/model capability
    │     └── Messages/SSE transport
    └── devin-code provider
          ├── existing-login credential source
          ├── account/seat/model capability
          └── Connect/protobuf transport
    ▼
Routsi normalized events → OpenAI JSON/SSE/tool_calls
```

Each provider implements one common internal contract:

```go
type SubscriptionProvider interface {
    Health(context.Context) ProviderHealth
    Models(context.Context) ([]ProviderModel, error)
    Complete(context.Context, ProviderRequest) (ProviderResult, error)
    Stream(context.Context, ProviderRequest, func(ProviderEvent) error) error
    Close() error
}
```

The registry owns construction and cleanup. Provider implementations own only their
credential source, subscription protocol and translation; Routsi core continues to own
public API authentication, routing, OpenAI envelopes, metrics and audit.

### Existing-login credential sources

The providers use the credentials already created by the vendor CLI login. Routsi does
not create a parallel login database.

Credential handling is isolated behind a small runtime-only interface:

```go
type CredentialSource interface {
    AccessToken(context.Context) (AccessToken, error)
    Refresh(context.Context) (AccessToken, error)
    Identity(context.Context) (AccountIdentity, error)
}
```

Rules:

- Provider code may resolve the vendor's supported OS credential location/keychain
  entry at runtime. No credential value appears in YAML, argv, source, tests, logs,
  errors, metrics, captures or API responses.
- Tokens are short-lived in memory, never persisted by Routsi and zeroed/released when
  replaced where practical.
- Refresh is single-flight per account so concurrent requests cannot race token
  rotation.
- File-backed credential reads require owner-only permissions; symlinks, group/world
  readable files and unexpected ownership fail closed.
- Keychain access is requested under the logged-in user. A daemon running as another
  OS user is unsupported unless that user completes its own vendor login.
- Auth failures produce `provider_auth_required`; Routsi never falls back silently to
  an API key or another account.
- The credential-format/refresh contract must be established by a dedicated spike
  before implementation. Network capture alone is not sufficient evidence.

### Claude Code provider

The Claude provider translates OpenAI chat messages, system/developer instructions,
tools, tool results, sampling controls and streaming intent into the subscription
Messages request observed in Spike 008.

- Model aliases are resolved through the Claude bootstrap/capability response rather
  than a permanent hard-coded list.
- Anthropic text deltas map to OpenAI content deltas.
- Native `tool_use` blocks map to OpenAI `tool_calls`; OpenAI `role: tool` messages map
  back to native `tool_result` blocks. The provider must not execute tools itself.
- Native usage and stop reasons are preserved. `tool_use` maps to
  `finish_reason: tool_calls`, `max_tokens` to `length`, and normal completion to
  `stop`.
- Signed thinking/redacted-thinking blocks are never rewritten or replayed as ordinary
  content.
- Subscription-specific headers and client metadata are produced by the provider and
  compatibility-tested against a pinned Claude Code version/capture fixture.

### Devin provider

The Devin provider implements only the minimum Connect/protobuf surface proven by
fixtures and live compatibility tests:

- identity and account/team policy;
- model configuration discovery;
- chat message request/stream response;
- conversation/session handle and cancellation if the protocol supports them.

Models come from `GetCliModelConfigs` intersected with seat/team policy. A catalog model
that the current account cannot use is not advertised. A requested unavailable model
fails explicitly—never `/upgrade` text wrapped as a successful completion.

Devin protobuf schemas live as reviewed generated code from pinned descriptors. Hand
written wire offsets and replaying captured opaque blobs are forbidden. Descriptor
provenance and compatibility version are recorded beside the generated code.

Devin-internal tool activity is **not** automatically OpenAI client tool calling. It
may be exposed as opt-in trace events. It becomes OpenAI `tool_calls` only if the
protocol demonstrably returns a suspended function request for the caller to execute.
Until that is proven, client-declared tools use Routsi's existing `tools: emulated`
contract or return an explicit unsupported-tools error; they are never silently
dropped and never double-executed.

### OpenAI compatibility

The normalized provider event model carries:

- text and reasoning deltas;
- tool-call start/argument delta/completion;
- usage updates;
- native finish reason;
- provider request/session ids;
- typed retryable, auth, quota, model-unavailable and protocol errors.

Routsi converts these into its existing `api.Result`, `ToolCall`, chat completion and
SSE shapes. Raw provider frames and authorization headers are excluded by default.
Optional debug captures are bounded, mode 0600 and structurally redacted before write.

### Routing, sessions and concurrency

- A concrete `claude-code-api` or `devin-code-api` model remains a hard bypass under
  Routsi's existing routing rules.
- Only explicit conversation ids may own provider session state. `fp-*` routing
  fingerprints remain forbidden as memory keys.
- Requests within one provider conversation are serialized. Independent conversations
  may run concurrently under per-provider limits.
- Model selection is explicit per request/catalog model. No silent model downgrade.
- Cancellation propagates to the native HTTP stream/Connect call.
- Retry occurs only before the first response byte/event and only for errors classified
  retryable by the provider.

## Relationship to earlier ADRs

- **Supersedes ADR-014 for Claude and Devin:** their old one-shot CLI types remain
  deprecated, but direct built-in subscription providers replace the proposed external
  command/sidecar destination.
- **Does not supersede ADR-013 generally:** command/socket/queue adapters remain the
  extension mechanism for arbitrary agents. Subscription providers are a deliberate
  built-in product capability with deeper auth and protocol ownership.
- **Reuses ADR-008/010:** structured tool-call types and stateless OpenAI↔provider tool
  translation remain the contract to preserve.

## Alternatives considered

### External command adapter or supervised CLI process

Rejected by the owner for Claude/Devin. It keeps authentication safely inside the CLI
but does not provide the desired direct in-process provider, and the existing command
transport cold-spawns and buffers each request.

### Separate `claude-code-api` / `devin-code-api` sidecars

Rejected as the default. Routsi could forward to them, but users would have to install,
configure and supervise another service. The requested capability belongs inside
Routsi.

### Replay raw captured requests

Rejected. Captures are fixtures and discovery evidence. Replaying authorization values
or opaque protobuf messages would be unsafe and version-fragile.

### Keep fenced-JSON tools as the permanent implementation

Rejected. It remains a compatibility fallback, not the target. Claude's native
Messages tool blocks should translate directly; Devin needs a separate proof before
claiming native client tools.

## Consequences

- Routsi becomes responsible for two private subscription protocols and their
  credential-format/refresh compatibility.
- Users get one binary and one OpenAI endpoint, with no API key and no extra sidecar.
- Native streaming, model discovery and Claude tool calls become possible without CLI
  process overhead.
- Vendor changes can break the providers. Compatibility fixtures, version gates and a
  kill switch are mandatory.
- The credential-reading blast radius grows. Provider modules must be auditable,
  locally bound by default, and excluded from prompt/content logging.
- Subscription terms, quotas and fair-use policies still apply. Routsi must identify
  these models as subscription-backed and must not represent them as API-billed or
  unlimited.

## Implementation gates

1. Credential-source spike for each provider, proving locate/read/refresh without ever
   printing or persisting token values.
2. Claude minimal-request replay built from typed data (not raw capture), followed by
   streaming, model discovery and native tool-result round trips.
3. Devin descriptor/protobuf provenance, model discovery and minimal chat stream.
4. Golden translation fixtures covering text, tool calls/results, usage, finish
   reasons, errors and partial streams.
5. Auth race, refresh single-flight, permission/ownership, redaction and logout tests.
6. Localhost-only default, Routsi bearer auth required before any non-loopback bind,
   and an operator kill switch per subscription provider.
7. Live compatibility matrix pinned to tested Claude Code/Devin CLI versions, with
   fail-closed behavior on incompatible protocol drift.

## Open questions

- Are the vendor subscription credential and refresh protocols permitted/stable enough
  for Routsi to own, or must a vendor-provided credential broker remain in the final
  design?
- Can Devin supply reviewed protobuf descriptors for the required Connect services, or
  must the direct provider remain experimental?
- Should `/v1/responses` land in the first implementation or follow after the chat
  completion provider is stable?
- What provider-identification headers should Routsi expose so callers can distinguish
  subscription quota from metered API usage?

