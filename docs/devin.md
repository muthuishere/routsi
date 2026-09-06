---
title: Use Devin with routsi
---

# Use Devin with routsi

Routsi connects directly to Devin's authenticated Cascade service. It does not launch
the Devin CLI during inference and does not emulate tool calls in prompts.

## Prerequisite

Complete Devin's normal login once as the OS user that runs Routsi. Routsi reuses the
resulting local authentication state from `~/.local/share/devin/credentials.toml`.
The file must be a regular, non-symlink file readable only by its owner.

The `devin` executable does not need to be on `PATH` after authentication has been
established. Never copy the credential into `models.yaml`.

## Configure the direct adapter

```yaml
models:
  - name: devin
    type: subscription
    provider: devin
```

Routsi performs the native sequence itself:

1. `GetUserJwt` exchanges the stored session credential for a short-lived user JWT.
2. `GetCliModelConfigs` discovers the account's available model UIDs.
3. `AssignModel` resolves adaptive/router models when required.
4. `GetChatMessage` streams protobuf frames over Connect HTTP.

Call it through the normal OpenAI-compatible endpoint:

```sh
curl http://127.0.0.1:11080/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model":"devin",
    "messages":[{"role":"user","content":"Explain this repository."}]
  }'
```

## Select a model

Leave `upstream_model` empty to use Devin's advertised default. To select an account-
available model, use its label or wire UID:

```yaml
  - name: devin-swe
    type: subscription
    provider: devin
    upstream_model: SWE-1.6 Slow
```

An unavailable or disabled model fails explicitly; Routsi does not silently substitute
a premium model.

## Native tool calls

Send ordinary OpenAI function tools. Routsi maps their names, descriptions, JSON
schemas, and strictness into Devin's `ChatToolDefinition` protobuf and maps streamed
`ChatToolCall` deltas back into OpenAI `tool_calls`:

```sh
curl http://127.0.0.1:11080/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model":"devin",
    "messages":[{"role":"user","content":"Get the weather for Chennai."}],
    "tools":[{"type":"function","function":{
      "name":"get_weather",
      "description":"Get weather for a city",
      "parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}
    }}]
  }'
```

No tool manifest is added to the prompt.

## Live E2E

```sh
go test ./internal/server -run '^TestLiveDevinDirect' -count=1 -v
```

The live tests cover both exact text completion and a schema-checked native tool call.
They skip when authentication state is missing or unsafe, and when `-short` is used.
They do not require or invoke the Devin executable.

## Optional CLI example

The generic CLI wrapper remains only as an editable example under
`examples/adapters/cli.js`. A user may configure it as `type: command`, but it is not
the Devin integration described on this page and is not built into Routsi's provider.

See [ADR-015](adr/015-subscription-backed-cli-providers.md) and
[Spike 008](spikes/008-subscription-cli-protocols.md) for protocol provenance and the
security boundary.
