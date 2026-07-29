# Using opencode (or any OpenAI client) with devin / codex / claude through routsi

routsi exposes agent CLIs as OpenAI models — with **function calling** — so opencode
can use devin or codex as its brain. Two flavors, pick per agent:

## Flavor 1: one-shot agent models (`type: devin|codex|claude|copilot`)

routsi shells the CLI per turn and emulates tool calling via a fenced-JSON protocol
(ADR-011; the agent *decides* calls, your client *executes* them).

```yaml
# models.yaml
models:
  - name: codex
    type: codex
  - name: claude-fast
    type: claude
    upstream_model: haiku
```

```jsonc
// opencode.json (project root — run opencode from a real project dir, not a symlink)
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "routsi": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8080/v1" },
      "models": { "codex": {}, "claude-fast": {} }
    }
  },
  "model": "routsi/codex"
}
```

```sh
routsi serve
opencode --model routsi/codex     # opencode's write/edit/bash tools now work
```

**Caveat — prompt size.** One-shot CLIs take the whole transcript as an argument.
The devin CLI dies around ~80KB (opencode's system prompt can exceed that), so for
**opencode + devin use Flavor 2**; claude/codex tolerate more. Each tool-call round
is also a cold CLI spawn.

## Flavor 2: interactive worker (`devin-live` — recommended for devin)

A long-lived agent TUI joins routsi as a pull-worker queue; jobs are handed over as
files (no size limit), the session stays warm, and tool calls flow natively:

```sh
devin --permission-mode dangerous                # the TUI, in a scratch dir
routsi worker join --queue devin-live --workdir ~/devin-jobs \
  --notify 'tmux send-keys -t devin "Read $ROUTSI_JOB_FILE and follow its HOW TO ANSWER section." Enter'
opencode --model routsi/devin-live
```

`join` registers the queue, writes each job (conversation + tool schemas) to
`job-<id>.md`, runs your `--notify` command (any keystroke automation — tmux,
ghostty-sendkeys, even a headless `claude -p`), re-nudges every 45s until the
agent writes `answer-<id>.json` (`{"content"}` or `{"tool_calls":[...]}`), and
posts it back.

Full setup + reference driver: [`examples/interactive-worker/`](../examples/interactive-worker/).
Real sessions (opencode building/refactoring apps through devin, claude and codex
workers — 7–14 tool-call rounds each): [`docs/spikes/006`](spikes/006-interactive-worker-opencode.md).

## Routing between agents with a custom JS decider

`auto` can *choose the agent per request* via the external decider (ADR-007) plus a
dynamic group whose levels map to agents:

```yaml
decider:
  command: "node examples/decider-agents.js"
models:
  - name: auto-agent
    type: dynamic
    levels:
      low: claude-fast        # quick questions → cheap one-shot claude
      medium: codex           # code edits → codex
      high: devin-live        # big multi-step work → the live devin worker
```

`examples/decider-agents.js` reads the request JSON on stdin and answers
`{"level":"low|medium|high"}` — plain Node, no deps, swap in any logic you like
(keyword rules, a classifier call, budget caps). Point opencode at
`routsi/auto-agent` and every request lands on the agent your JS picked. Any
decider failure falls back to the built-in rules — routing never breaks.
