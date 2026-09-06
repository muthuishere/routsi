# Spike 008: subscription-backed Claude Code and Devin Code adapters

- **Date:** 2026-09-03–05
- **Status:** Direct-provider feasibility spikes complete; Claude is conditionally
  feasible, Devin is blocked on authoritative protobuf descriptors
- **Scope:** locally authenticated Claude Code 2.1.259 and Devin CLI 2026.8.18

## Question

Can routsi expose Claude Code and Devin CLI subscriptions through its existing
OpenAI-compatible endpoint while reusing the user's manual login, selecting a model,
streaming replies, and surfacing tool calls—without copying credentials or reviving
vendor-specific backends in core?

## Reference pattern: OpenCode's Codex integration

The local OpenCode checkout at commit
`6ba101e0869fc53a18f24516cb4add11e51c01f4` has a useful product shape in
`packages/opencode/src/plugin/openai/codex.ts`:

1. Built-in registration, rather than a user-installed plugin.
2. An auth-aware model filter.
3. Authentication injected at the transport boundary.
4. Request URL/header translation behind the normal provider interface.
5. Refresh and connection cleanup owned by the plugin lifecycle.

Unlike a subprocess adapter, this direction necessarily reads the vendor-owned login
at the provider's runtime credential boundary. It does not copy credentials into
Routsi configuration or storage. OpenCode owns its Codex OAuth lifecycle; these spikes
do not establish equivalent ownership of Claude or Devin refresh protocols.

## Network-capture spike

A disposable Go MITM proxy launched only the tested CLI as its child. It used a
random loopback port, captured request and response streams to mode-0600 JSONL, and
installed a unique two-hour CA in the macOS login keychain only for the Devin run.
The trust entry and certificate file were removed immediately after the run. Raw
headers and bodies were not copied into this repository.

### Claude Code

- The authenticated Claude CLI completed a real `--print` request through the
  capture proxy.
- `--model sonnet` resolved to `claude-sonnet-5`.
- The successful run captured the inference call at
  `POST https://api.anthropic.com/v1/messages?beta=true`, plus bootstrap, account,
  quota, model/feature configuration, MCP registry and configured MCP traffic.
- `NODE_EXTRA_CA_CERTS` was enough to scope MITM trust to the child.
- The response is SSE and can be copied without delaying delivery.

The raw network protocol proves observability, but it is the wrong implementation
boundary: it exposes authorization headers and ties routsi to private subscription
endpoints.

### Devin CLI

- The authenticated Devin CLI completed a real prompt through the capture proxy.
- Eleven completed HTTP exchanges returned 200 (120,879 request bytes; 461,774
  response bytes).
- Inference/control uses protobuf and Connect RPC on `server.codeium.com`, notably
  `exa.api_server_pb.ApiServerService/GetChatMessage` with
  `application/connect+proto`.
- Model discovery uses `GetCliModelConfigs`; seat/team policy uses
  `SeatManagementService`; account identity also calls `GET api.devin.ai/v3/self`.
- The account exposed only the default `SWE-1.6 Slow`; explicit tested alternatives
  were rejected with `/upgrade`.
- Devin's Rust platform verifier required the temporary macOS trust entry;
  `SSL_CERT_FILE` alone did not establish trust.

Again, private Connect/protobuf traffic is diagnostic evidence, not an adapter API.

## Direct Claude Code API spike

`_spikes/claude-code-api` implements a small typed Go Messages/SSE client. It reads
the current user's `Claude Code-credentials` item using the absolute
`/usr/bin/security` path, retains only the access token and expiry, discards response
bodies on errors, and emits only structural canary metadata. It re-reads Keychain once
after HTTP 401 but deliberately does not implement Claude Code's private refresh
exchange or write Keychain state.

A live request using `claude-sonnet-5` passed credential loading and reached the
inference service; it returned HTTP 429 due to the account's current quota/rate state.
Therefore transport/auth feasibility is plausible, but a successful SSE completion is
not yet proven. The spike now requires an SSE content type, ordered `message_start`,
and terminal `message_stop`, rejecting truncated HTTP-200 streams.

## Direct Devin Code API spike

`_spikes/devin-code-api` implements a typed Connect request shell and read-only binary
provenance scan. The installed binary proves method-name and media-type markers only;
it does not contain a recoverable authoritative descriptor set. The guessed empty
`GetCliModelConfigs` request reached `server.codeium.com` and returned HTTP 400. This
does not prove authentication or request-schema correctness, and direct chat was not
attempted.

The spike now pins the exact vendor host, rejects redirects, requires TLS 1.2+, bounds
credential and response reads, and fails closed for symlinks, wrong ownership, or
group/world-readable credential files. The currently installed Devin credential file
is mode `0644`, so the hardened spike intentionally refuses live use until the owner
or Devin changes it to owner-only permissions. Inspection mode does not read it.

## CLI fallback: Claude stream-json

Claude Code can be kept as a supervised process using bidirectional NDJSON:

```text
claude -p
  --input-format stream-json
  --output-format stream-json
  --include-partial-messages
  --replay-user-messages
  --permission-prompts none
  --tools ""
```

Do not use `--bare`: it disables OAuth/keychain reuse and requires a separately
provisioned API key or helper.

Useful output frames:

- `system/init`: session id, selected model and capabilities;
- `stream_event`: native partial text/tool deltas;
- `assistant`: Messages-shaped content, usage and stop reason;
- `result`: authoritative turn boundary, status, usage, cost and session id.

The protocol also exposes an in-band `set_model` control request and interruption.
For the first implementation, selecting the startup `--model` and keying a process
by `(conversation, model)` is simpler; in-band switching can follow after a versioned
compatibility test.

Credential boundary: spawn as the logged-in OS user and let Claude read/refresh its
own OAuth/keychain state. Routsi never opens, parses, copies, returns or logs it.

## CLI fallback: Devin ACP

`devin acp` is a long-lived newline-delimited JSON-RPC 2.0 server over stdio.
With `ACP_BACKEND` unset it uses environment/stored CLI credentials; setting
`ACP_BACKEND` makes the host the credential source and defeats this reuse mode.

Observed protocol-version-1 flow:

1. `initialize`
2. `session/list`, then `session/new` or `session/load`
3. `session/set_config_option` for `mode=ask` and an advertised model
4. `session/prompt`
5. consume `session/update` notifications
6. `session/cancel` on caller cancellation

`agent_message_chunk` maps to OpenAI content deltas. `usage_update` and the final
prompt response supply usage/stop metadata. Model values must come from the session's
advertised `model.options`, because team/account policy can narrow the CLI's general
model list.

Credential boundary: keep `ACP_BACKEND` unset and let Devin own stored credentials,
refresh and policy. Routsi consumes stdout JSON-RPC only; stderr is diagnostic and
must be capped/redacted.

## Tool-call finding

There are two different meanings of “tool call”:

1. **OpenAI client-executed tools.** The API returns `finish_reason: tool_calls`; the
   caller executes them and sends `role: tool` results on the next request.
2. **Agent-local tools.** Claude/Devin execute filesystem, terminal or MCP work inside
   their own process and emit progress events.

Claude `tool_use` frames and Devin ACP `tool_call` notifications do not automatically
mean (1). Exposing agent-local activity as OpenAI tool calls would cause the caller to
execute an operation a second time.

Routsi already has the safe v1 answer: `tools: emulated`. It passes the caller's tool
manifest in the adapter job, constrains the agent to return the existing fenced JSON
contract, and normalizes that result into OpenAI `tool_calls`. Use `tools: native` only
after a separate MCP/ACP client-tool bridge proves that the operation is suspended for
the caller rather than locally executed.

Agent-local calls may be exposed separately as opt-in trace events or response
metadata; they must never masquerade as client-executed OpenAI function calls.

## Lifecycle and concurrency findings

- One process may serve multiple sequential turns, but never concurrent turns for
  the same session.
- Map explicit Routsi conversation ids to native session ids. Never use `fp-*`
  routing fingerprints as backend-memory keys.
- Use one child per active conversation initially, a global concurrency semaphore,
  idle eviction, and native session resume/load after restart.
- Do not resume one native session concurrently from two children.
- Cancellation first uses the native control message, then TERM/KILL after a deadline.
- A routsi service running as another OS user cannot assume access to the logged-in
  user's keychain or CLI state. Subscription adapters are local/per-user by default.
- Claude/Devin may load user hooks, skills, MCP configuration and project rules. A
  predictable API preset should use safe/isolated configuration while retaining auth.

## Verdict

Both supported subprocess protocols are feasible and remain valuable diagnostic and
fallback surfaces. The owner rejected them as Routsi's target architecture on
2026-09-04: the desired result is direct built-in subscription providers comparable to
OpenCode's Codex provider, not an external command, persistent CLI child, or sidecar.

Claude is the only candidate ready for the next spike: repeat the canary after quota
reset, then prove model discovery, refresh ownership, and native tool round-trips.
Devin direct integration is blocked: binary strings are not a schema, HTTP 400 is not
a typed protocol proof, and the local credential file currently fails ADR-015's
permission gate. Raw request replay and guessed protobuf remain forbidden.

## Remaining proof before production implementation

- Claude direct: successful live SSE canary, bootstrap-backed model discovery, refresh
  behavior, 401 single-flight, native tool/tool-result round-trip, cancellation and
  OpenAI translation fixtures.
- Devin direct: authoritative reviewed descriptors, owner-only credential storage,
  successful typed discovery, assignment/model selection, chat streaming and
  cancellation. Do not continue with guessed wire fields.
- Both: end-to-end no-secret-output tests, compatibility/version gates, localhost-only
  exposure and kill switches.
