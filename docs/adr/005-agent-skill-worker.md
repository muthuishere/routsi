# ADR-005: Agent-skill worker (turn any agent into a provider)

- **Status:** Accepted (2026-07-25)
- **Date:** 2026-07-25
- **Depends on:** ADR-001 (pull-worker transport), ADR-002 (provider), ADR-004 (status)

## Context

The point of the pull-worker (ADR-001) is that **anyone can contribute their logged-in
agent** — opencode, codex, Claude Code, a python script — without installing anything on
the proxy host. That needs a dead-simple "loop" the operator runs on their own machine:
register once, pull questions, answer with the local agent, post back. This is the
"agent skill."

## Decision (proposed)

Ship the worker loop as a **`routsi worker` subcommand** (the CLI is already the artifact
— no separate language runtime), and/or a scaffolded shell script for hackability:

```
routsi worker run \
  --proxy https://proxy:8080 \
  --token $ROUTSI_WORKER_TOKEN \
  --queue alices-opencode \
  --agent 'codex exec --skip-git-repo-check -'   # reads the question on stdin, prints answer
```

Behaviour:
1. **Register** the queue once (ADR-001/003).
2. **Long-poll** `GET /jobs?wait=25`.
3. On a job, render the messages to a prompt, pipe to `--agent` (stdin), capture stdout.
4. **Answer** `POST /jobs/{id}`.
5. **Fail loud** (ADR-004): on 401 print `token expired/invalid — regenerate with 'routsi
   token'` and exit non-zero; never silently spin.
6. Print human status each step (`registered ✓`, `answered job abc (1.2s)`).

Any tool that takes a prompt and prints an answer works — that's the whole contract.

## Alternatives considered

1. **Standalone shell script only.** Maximally hackable, zero deps, but drifts from the
   binary. Offer as `routsi worker scaffold` output for those who want to edit it.
2. **Node/TS worker.** Owner preference is CLI-only; a Go subcommand + optional shell
   script beats adding a JS runtime dependency.
3. **Library/SDK.** Over-scoped; the HTTP contract (ADR-001) is small enough to curl.

## Consequences

- Reuses the CLI binary → one distribution artifact, cross-platform.
- **Worker auth: none in v1** (owner, 2026-07-25) — reserved empty config placeholder,
  added later. The `--token` flag is accepted but optional/ignored for now so the skill
  interface is forward-compatible.
- v1 non-streaming (pull worker returns a whole answer); document it.
- One worker per queue in v1 (clean stickiness — ADR-001); pool later.
- The **agent skill wraps the built-in `routsi worker run`** (one artifact); optionally
  `routsi worker scaffold` emits an editable shell script.

## Open questions

1. `routsi worker run` (built-in) **and** `routsi worker scaffold` (emit editable script),
   or just one?
2. Prompt rendering: pass raw OpenAI `messages` JSON to `--agent`, or a flattened text
   transcript? (Agents differ.)
3. Auto-reconnect/backoff policy on transient network errors.
