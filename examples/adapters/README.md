# Adapters

An **adapter** makes something that isn't an OpenAI API answer as one: a CLI agent, a
script, a workflow runner, a live session. routsi core carries no vendor-specific code —
the adapter is your file (ADR-013).

One schema, three transports:

| transport | type | adapter is | spawn cost | use it for |
|---|---|---|---|---|
| **exec** | `command` | a script routsi runs per request | ~1–5ms | one-shot CLIs, JS files, workflows |
| **socket** | `adapter` | a long-lived sidecar on a unix socket | zero | stateful things (planned) |
| **queue** | `queue` | a worker that dials *in* | zero | remote / interactive agents (`routsi worker join`) |

## exec (`type: command`)

```yaml
models:
  - name: demo-adapter
    type: command
    command: node ./examples/adapters/echo-tools.js
    workdir: .            # optional — default is a managed cache dir
    timeout: 5m           # optional
    tools: native         # native (default) | emulated | off
```

routsi writes a **job** to the adapter's stdin:

```json
{ "id": "cmd_ab12…", "model": "demo-adapter", "upstream_model": "",
  "conversation_id": "c-1", "stream": false,
  "prompt": "<rendered transcript>",
  "messages": [ … ], "tools": [ … ], "tool_choice": null }
```

`prompt` is the whole conversation already rendered, so an adapter can ignore `messages`
entirely and still be correct. The same values are also in the environment —
`ROUTSI_MODEL`, `ROUTSI_UPSTREAM_MODEL`, `ROUTSI_CONVERSATION_ID`, `ROUTSI_JOB_ID`,
`ROUTSI_STREAM` — so a shell one-liner needs no JSON parsing:

```yaml
  - name: hello
    type: command
    command: 'cat >/dev/null; echo "hi from $ROUTSI_MODEL"'
```

The adapter writes the **answer** to stdout — a JSON object, or anything else (taken
verbatim as the answer text):

```json
{"content": "…"}
{"tool_calls": [{"name": "get_weather", "arguments": {"city": "Paris"}}]}
```

Both the simplified shape above and the full OpenAI shape
(`{"id","type","function":{"name","arguments":"<json string>"}}`) are accepted; routsi
synthesizes missing ids and re-encodes arguments.

### Tool modes

- **`native`** (default) — `tools` is passed through in the job; the adapter returns
  `tool_calls`. Use when the adapter can reason about tool schemas.
- **`emulated`** — routsi folds a fenced-JSON tool manifest into `prompt` and parses the
  reply. Use to give a plain text-in/text-out CLI tool calling for free.
- **`off`** — a request carrying `tools` fails with HTTP 400 rather than silently dropping
  them (ADR-008).

## Shipped templates

routsi ships default adapters **inside the binary** and lays them down in
`~/.config/routsi/adapters` on first `serve` — or explicitly:

```
routsi install --adapters          # write any missing template
routsi install --adapters --force  # restore the shipped versions, discarding edits
```

They are yours to edit. An existing file is **never** overwritten, so your changes survive
upgrades and restarts.

- **`cli.js`** — generic CLI adapter. `ADAPTER_CLI=claude|codex|copilot|devin|openclaw`
  picks the agent; add your own to the `AGENTS` map. Replaces the deprecated built-in
  `type: devin|codex|copilot|claude` (ADR-014). Note the per-agent
  `prompt: "stdin"|"file"|"argv"` field — claude/codex take stdin, devin takes
  `--prompt-file` (its `-p` is the non-interactive switch, not the prompt), and copilot
  only accepts `-p <text>` on argv. devin also panics on an open stdin it never reads.
  All measured in [spike 007](../../docs/spikes/007-adapters-out-of-core.md).
- **`echo-tools.js`** — the whole contract on one screen, with a real tool call.

Reference them without hardcoding a home path — `$ROUTSI_ADAPTERS` is exported into every
adapter's environment, and `command` runs under `sh -c`:

```yaml
  - name: claude-adapter
    type: command
    command: ADAPTER_CLI=claude node $ROUTSI_ADAPTERS/cli.js
    tools: emulated
```

Proven live (spike 007): claude 7.5s, codex 14s, copilot 29s — all three returned an
argument-exact `get_weather` tool call with `finish_reason: tool_calls`, with no
vendor-specific code in the binary.

## Reaching Claude / Codex / Copilot

For subscription-backed access with native tool calling, MCP servers and real permissions,
point routsi at an OpenAI-compatible bridge such as
[ccproxy-api](https://github.com/CaddyGlow/ccproxy-api) instead of driving the CLI:

```yaml
  - name: claude-sdk
    type: forward
    base_url: http://127.0.0.1:8000/sdk/v1
```

That path is better than anything routsi's own CLI backends did, which is why they are
deprecated.

## Security

`command` runs an arbitrary local process with routsi's environment and privileges — the
same trust level as `decider.command`. **Do not point it at anything you would not run
yourself.** The child inherits routsi's environment, which may hold provider keys; the
adapters here read only what they need and never echo them.

Per-request process spawn is right at agent latencies (seconds) and wrong for high-QPS
models — use the queue transport when that matters.
