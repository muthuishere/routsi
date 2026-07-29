# Interactive agent as a routsi worker (with tool calling)

Turn a **long-lived interactive agent TUI** (devin, Claude Code, codex) into a routsi
worker, so any OpenAI client — opencode, an SDK, curl — can use it as a model,
**including OpenAI function/tool calling**. Live-proven 2026-07-29: opencode built and
refactored apps through devin/claude/codex workers, 7–14 tool-call rounds per task
(docs/spikes/006-interactive-worker-opencode.md).

## Why interactive instead of `type: devin` (one-shot `-p`)?

- **No prompt-size ceiling.** One-shot CLIs take the transcript as an argument; the
  devin CLI dies (SIGPIPE) around ~80KB — a real client like opencode sends far more
  (its system prompt embeds tool schemas and skills). The worker hands the job over
  as a **file**; size is irrelevant.
- **Warm session.** No cold spawn per turn; the agent keeps its own context.
- **Tool calls.** The queue path carries `tools` in the job and accepts `tool_calls`
  in the answer — one-shot backends are text-only today.

## How it works

```
OpenAI client ──/v1/chat/completions──▶ routsi ──queue──▶ driver ──job file──▶ agent TUI
      ◀── tool_calls / content ◀──────────────◀─answer──◀── answer file ◀──────┘
```

1. `routsi worker register -queue devin-live` — the queue becomes model `devin-live`.
2. The driver (`worker-driver.js`) long-polls `GET /v1/workers/devin-live/jobs?wait=25`.
3. Each job (conversation + the client's **tool schemas**) is written to
   `job-<id>.md`; the driver types one short line into the TUI: *"Read job-<id>.md
   and follow its HOW TO ANSWER section."*
4. The agent writes `answer-<id>.json` — either
   `{"content":"..."}` or `{"tool_calls":[{"name":"...","arguments":{...}}]}`.
5. The driver POSTs it to `/v1/workers/devin-live/jobs/<id>`; routsi normalizes to
   OpenAI wire shape (`finish_reason:"tool_calls"`, string-encoded arguments,
   synthesized call ids). The **client** executes the tools and sends results back
   as the next job — the worker never touches the client's files.

## Run it

The reference driver drives the TUI through the ghostty-sendkeys automation
(github.com/muthuishere — a Ghostty fork whose surfaces accept scripted keystrokes);
any keystroke automation (tmux `send-keys`, expect) can replace those three lines.

```sh
# 1. proxy side
routsi serve                                    # any config

# 2. agent side: an interactive TUI in its own terminal, yolo mode, in a scratch dir
devin --permission-mode dangerous               # or: claude --dangerously-skip-permissions / codex

# 3. the glue — built into the CLI (registers the queue itself):
routsi worker join --proxy http://proxy:8080 --queue devin-live --workdir ~/devin-jobs \
  --notify 'tmux send-keys -t devin "Read $ROUTSI_JOB_FILE and follow its HOW TO ANSWER section." Enter'
```

`--notify` is any command that tells your agent about the job (it gets
`ROUTSI_JOB_ID` / `ROUTSI_JOB_FILE` / `ROUTSI_ANSWER_FILE` in env, runs in
`--workdir`, and is re-run every `--nudge` until the answer file appears). It
even works fully headless — `--notify 'claude -p --dangerously-skip-permissions
"Read $ROUTSI_JOB_FILE and follow its HOW TO ANSWER section."'` was live-verified
returning tool_calls. `worker-driver.js` in this directory is the original
ghostty-sendkeys prototype the subcommand was distilled from — keep it only if
you need custom pacing logic.

Then from any OpenAI client: `{"model":"devin-live", "tools":[...], ...}`.

## Operational notes (learned the hard way)

- The driver **re-nudges the TUI every 45s** while waiting — devin's free plan
  throttles ("high demand"); the nudge is idempotent.
- The driver is single-threaded: concurrent client requests (opencode fires
  title-gen + main together) can expire at the broker's `workers.max_wait`
  (default 5m). Raise it, or rely on the client's retry.
- One worker per queue; run one TUI+driver pair per agent
  (`devin-live`, `claude-live`, `codex-live` all on one proxy works fine).
- The agent's cwd is its scratch space (job/answer files land there) — point it
  somewhere disposable, never at the client's project.
