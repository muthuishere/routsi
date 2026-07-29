# ADR-012: Tools through the pull-worker queue

- **Status:** Accepted — minimal slice shipped 2026-07-29 (owner-directed): Job carries tools/tool_choice, answer accepts content+tool_calls (both shapes), broker returns api.Result. Capabilities-at-register still open. Live-proven: opencode built a todo app through an interactive-devin worker (spike 006).
- **Date:** 2026-07-29
- **Deciders:** owner + routsi

## Context

The pull-worker path (ADR-001/005) is doubly lossy today:

- `queue.Job` (`internal/queue/queue.go:28-36`) carries only `ID/Model/
  ConversationID/Messages` — `tools`, sampling params, and `req.Raw` are not
  carried, so a worker can't even *see* that tools were requested.
- The answer channel is `result{content string}` (queue.go:38) and the worker CLI
  posts a plain `content` (`cmd/routsi/worker.go:81`) — no path to return a tool
  call. `workerJob` (worker.go:26-29) even drops `model` and `conversation_id`.
- `promptFrom` (worker.go:232-250) re-flattens messages worker-side.

Yet workers are the *best*-placed backend for tools: the worker is a live agent
session (Claude Code/codex) that natively understands tool schemas.

## Decision

1. **Job payload carries the full request** (ADR-008/009 types): add `Tools`,
   `ToolChoice`, sampling params, and restore `Model`/`ConversationID` to the
   worker-visible job JSON. Additive fields — old workers ignore them.
2. **Answers become structured**: the answer POST body grows from
   `{content}` to `{content?, tool_calls?, finish_reason?}`. The broker's result
   channel carries the ADR-008 `Result`. `{content}` alone stays valid — existing
   workers keep working unchanged.
3. **Capability at registration**: `POST /v1/workers/register` gains
   `"capabilities": ["tools", ...]` (optional, default none). A `tools`-carrying
   request routed to a queue whose worker didn't declare `tools` → ADR-008's
   explicit 400, not a silent drop. `GET /v1/workers` surfaces the declared
   capabilities.
4. **Skill + helpers updated**: `routsi worker poll` prints the tools in the job
   JSON; `routsi worker answer` accepts `--tool-call name:json-args` (or a JSON
   body via stdin); SKILL.md teaches Mode A agents to emit tool calls when the job
   asks for them.

## Alternatives

- **Ship `req.Raw` opaque in the job** — workers each re-parse the OpenAI body;
  no capability negotiation; version skew invisible. Rejected in favor of typed
  additive fields (Raw can ride along *as well* for forward-compat).

## Consequences

- Wire-additive: no version bump needed for old workers; new fields are opt-in.
- At-most-once + non-streaming semantics (ADR-001) are unchanged; a tool-call
  round trip is simply two jobs on the same conversation id.
- Worker auth is still v1-absent — a malicious worker could now return
  `tool_calls` the *client* will execute. Documented loudly in SKILL.md/README;
  real mitigation is the reserved `workers.auth` block, unchanged in scope.

## Open questions

- Should the broker validate returned `tool_calls` against the job's declared
  tool names (cheap sanity + partial mitigation of the above)? Recommend yes.
