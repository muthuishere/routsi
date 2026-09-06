---
title: Create a custom adapter
---

# Create a custom adapter

## Built-in native adapters need no registration

Routsi's direct providers self-register inside the binary. For example,
`provider: devin` and `provider: claude-bedrock` need no adapter script, handler, or
`routsi install --adapters` step. A model entry only selects the provider and its
upstream model/account settings.

The shipped `models.yaml` keeps the Devin, Claude-on-AWS, local-Llama, and JavaScript
examples commented. Consequently, installing or starting Routsi does not initialize
them, inspect their login state, or send provider traffic. Uncomment only the model you
intend to expose.

The command contract below is for third-party or project-specific executables. New
built-in Go providers implement `backend.SubscriptionFactory` and call
`backend.RegisterSubscription` from their package initialization; the generic server
path discovers them automatically.

A Routsi adapter turns any trusted executable into an OpenAI-compatible model. The
simplest production-supported transport is `type: command`: Routsi starts the command
for each request, writes one JSON job to stdin, and reads one answer from stdout.

## 1. Create the adapter

Save this minimal Node.js adapter as `adapters/example.mjs`:

```js
let input = ""
for await (const chunk of process.stdin) input += chunk

const job = JSON.parse(input)
const last = job.messages.at(-1)?.content ?? job.prompt

process.stdout.write(JSON.stringify({
  content: `Adapter received: ${last}`,
}))
```

An adapter may instead write plain text; Routsi treats the complete stdout as assistant
content. Keep logs on stderr because stdout is the protocol channel.

## 2. Register it

```yaml
models:
  - name: my-adapter
    type: command
    provider: local
    command: node ./adapters/example.mjs
    workdir: /absolute/path/to/your/project
    timeout: 2m
    tools: native
```

`command` runs through `sh -c`. `workdir` defaults to Routsi's managed work directory,
so set it explicitly when the command uses relative paths.

Start Routsi and test the adapter:

```sh
routsi serve

curl http://127.0.0.1:11080/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"my-adapter","messages":[{"role":"user","content":"hello"}]}'
```

## Job contract

Routsi writes one JSON object with these fields:

```json
{
  "id": "cmd_ab12",
  "model": "my-adapter",
  "upstream_model": "optional-native-model",
  "conversation_id": "optional-conversation-id",
  "stream": false,
  "prompt": "rendered complete transcript",
  "messages": [{"role":"user","content":"hello"}],
  "tools": [],
  "tool_choice": null
}
```

The common values are also available as `ROUTSI_MODEL`, `ROUTSI_UPSTREAM_MODEL`,
`ROUTSI_CONVERSATION_ID`, `ROUTSI_JOB_ID`, and `ROUTSI_STREAM`.

## Answer contract

Return plain text or a JSON object:

```json
{"content":"normal assistant answer"}
```

For client-executed tools, return the simplified form:

```json
{
  "tool_calls": [
    {"name":"get_weather","arguments":{"city":"Chennai"}}
  ]
}
```

Routsi generates missing call IDs and emits an OpenAI response with
`finish_reason: "tool_calls"`. The full OpenAI tool-call shape is accepted too.

## Choose a tool mode

| mode | behavior |
|---|---|
| `native` | The request's tools reach the adapter and it may return `tool_calls`. |
| `emulated` | Routsi embeds a fenced JSON tool contract in `prompt` and parses the response. |
| `off` | Requests containing tools fail with HTTP 400 instead of silently dropping them. |

Use `native` only when the adapter returns a suspended tool request for the API client.
Do not translate tools that the underlying agent already executed locally; that would
cause the operation to run twice.

## Validate failure behavior

Before routing real traffic, check all four cases:

1. Plain text answer.
2. Structured `content` answer.
3. A tool call with exact JSON arguments and the following `role: tool` turn.
4. Timeout or non-zero exit without secrets appearing in stderr.

Command streaming is currently buffered. Use a [`queue`](adr/001-pull-worker-queue.md)
for remote or interactive workers. The long-lived socket adapter described by
[ADR-013](adr/013-adapter-contract.md) is planned, not shipped.

## Security boundary

An adapter runs with Routsi's OS permissions and inherited environment. Only configure
commands you would run yourself. Read credentials from their environment variable names
at runtime; never print them, place literal values in YAML, or return them in errors.

For ready-made CLI wrappers, see
[`examples/adapters/`](https://github.com/muthuishere/routsi/tree/main/examples/adapters).
