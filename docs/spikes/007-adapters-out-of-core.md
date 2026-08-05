# Spike 007 — real agents through out-of-core adapters

Date: 2026-08-05 · ADR: [013](../adr/013-adapter-contract.md), [014](../adr/014-deprecate-builtin-cli-agents.md)
Status: **done — 3/4 agents proven live, 1 blocked by its own service**

## Question

Can the four agents routsi ships Go code for (`claude`, `codex`, `copilot`, `devin`) be
driven entirely from **outside the binary**, through `type: command` + a user-editable JS
adapter, with tool calling intact? If yes, ADR-014's deprecation is safe.

## Setup

One routsi on `:9930`, four `type: command` models, all pointing at the same shipped
adapter, distinguished only by an env var:

```yaml
  - name: claude-adapter
    type: command
    command: ADAPTER_CLI=claude node $ROUTSI_ADAPTERS/cli.js
    tools: emulated
    timeout: 4m
```

Request: one `get_weather` tool (`city` required, `unit` enum c|f), prompt "What is the
weather in Berlin right now? Use the tool."

## Results

| agent | plain answer | tool call | latency | notes |
|---|---|---|---|---|
| **claude** | ✅ `ADAPTER_OK` | ✅ `{"city":"Berlin","unit":"c"}` | 6s / 7.5s | clean first try |
| **codex** | — | ✅ `{"city":"Berlin","unit":"c"}` | 14s | much faster than spike 002's "minutes" |
| **copilot** | — | ✅ `{"city":"Berlin","unit":"c"}` | 29s | needed the argv fix below |
| **devin** | ❌ | ❌ | 9s | **service-side**, see below; now on `--prompt-file` |

All three successes returned `finish_reason: "tool_calls"`, an argument-exact JSON string,
and a synthesized call id — identical wire output to the built-in Go backends, with zero
vendor code in the binary.

## Findings

**1. Agents disagree about how the prompt arrives — and it is not documented anywhere.**
The first run failed on two of four. Measured behaviour:

| agent | prompt via | evidence when wrong |
|---|---|---|
| claude | **stdin** | works |
| codex | **stdin** | works |
| copilot | **argv** (value of `-p`) | `error: Invalid command format. It looks like your prompt was not quoted…` |
| devin | **file** (`--prompt-file`) | with stdin: `thread 'main' panicked at chisel/src/repl_mode.rs:478:45: called Option::unwrap() on a None value` |

devin *panics* on an unread open stdin, so every non-stdin mode must close the child's
stdin rather than merely skip writing to it.

**devin takes a prompt file, and that is the right mode.** Reading `devin --help` closely:
`-p/--print` is the *non-interactive switch*, not the prompt — the prompt is a positional
(`-- <PROMPT>...`) or, better, `--prompt-file <FILE>`. Using the file avoids both the
~80KB argv ceiling measured in spike 006 and all shell-quoting hazards, so `cli.js` writes
the prompt to a temp file and passes `--prompt-file` (cleaning up on exit). Verified: the
invocation is accepted — devin now fails with its *service* error rather than a usage
error or a panic.

copilot has no equivalent: `-p, --prompt <text>` takes a string only. It therefore stays on
argv and remains the one agent exposed to the argv ceiling on large system prompts.

Encoded as a per-agent `prompt: "stdin" | "file" | "argv"` (+ `promptFlag`) field in
`cli.js`, with the reason in a comment.

**2. devin is blocked upstream, not by the adapter.** `Agent error: Permission denied:
We're currently facing high demand for this model.` Reproduced **identically outside
routsi**, both as `devin -p "…"` and as `devin -p --prompt-file /tmp/…` — same error, same
shape — so the adapter invocation is correct and devin's account/capacity is the blocker.
Not a spike failure; retest when the account frees up.

**3. `$ROUTSI_ADAPTERS` makes configs portable.** `command: node $ROUTSI_ADAPTERS/cli.js`
resolves because routsi exports the templates dir into the adapter's environment and the
command runs under `sh -c`. No home paths in models.yaml, so a config is shareable.

**4. Bootstrap-and-never-clobber holds.** Templates land in
`<config dir>/adapters` on first `serve` (and via `routsi install --adapters`). A file
edited by the user is preserved across restarts — verified by appending a marker to
`echo-tools.js`, restarting, and finding it intact. `--force` restores the shipped copy.

## Conclusion

ADR-014's deprecation is safe. Every agent routsi carries Go code for is reachable from
outside the binary with identical tool-calling output, and adding a fifth (openclaw,
opencode, anything) is an entry in a JS map rather than a routsi release.

The one capability not yet reproduced out-of-core is **devin's per-conversation session
mapping** (`devin -r <session>`), which `devin.go` still owns. That remains ADR-014's
gating item for actually deleting the built-in types, and needs the `conversation_id` →
session-handle store noted as an open question in ADR-013.
