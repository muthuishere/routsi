# Claude Code direct-provider spike

This isolated spike tests whether a Go process can use the current macOS user's
existing Claude Code subscription login to issue a typed, streaming Messages
request. It is compatibility evidence, not production provider code.

## Security boundary

- Reads the generic-password item `Claude Code-credentials` at runtime through
  `/usr/bin/security`; no token is placed in argv, environment, files, logs, or
  errors.
- Keeps the Keychain payload only in memory and wipes the command-output byte
  buffer after parsing. Go strings cannot be reliably zeroed, so process
  isolation remains preferable for production credential handling.
- Does not print response text. The canary output contains only a boolean,
  HTTP status, model, stop reason, request id, text byte count, and usage.
- Non-200 response bodies and authorization headers are never printed.
- Does not implement the vendor-private refresh-token exchange. On HTTP 401 it
  re-reads the Keychain once, allowing a token refreshed by Claude Code to be
  picked up, then fails closed with `claude code login required`.

## Evidence and compatibility assumptions

Tested against locally installed Claude Code `2.1.259`. Read-only binary
inspection and the preceding capture spike identify:

- Keychain service: `Claude Code-credentials`, account: current macOS username.
- Payload envelope: `claudeAiOauth` with `accessToken`, `refreshToken`,
  `expiresAt`, and `scopes`.
- Inference endpoint: `POST https://api.anthropic.com/v1/messages?beta=true`.
- Streaming response: Anthropic Messages SSE.
- Subscription transport marker: `anthropic-beta: oauth-2025-04-20` with
  `x-app: cli` and a Claude CLI User-Agent.

These are private subscription interfaces and may change without notice. Do not
ship this behind Routsi until live compatibility, terms, model discovery,
refresh ownership, and redaction tests are accepted.

## Run

The command spends one small subscription request and requires an existing
Claude Code login for the same macOS user:

```sh
cd _spikes/claude-code-api
go test ./...
go run . --model claude-sonnet-5
```

Expected output shape (values vary):

```json
{"ok":true,"http_status":200,"model":"...","stop_reason":"end_turn","request_id":"...","text_bytes":23,"usage":{"input_tokens":0,"output_tokens":0}}
```

Exit code `0` means the marker was observed, `2` means a successful stream did
not contain the marker, and `1` means auth, transport, protocol, or HTTP failure.

## Deliberate gaps

- No direct refresh-token call or Keychain write.
- No bootstrap/model-capability discovery.
- No tools, thinking-block replay, conversation continuation, or OpenAI mapping.
- macOS Keychain only; another OS user/service account cannot reuse this login.
