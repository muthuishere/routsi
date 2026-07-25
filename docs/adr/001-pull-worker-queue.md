# ADR-001: Pull-worker queue (remote agents answer via a broker)

- **Status:** Proposed (discussion)
- **Date:** 2026-07-23
- **Deciders:** owner + routsi
- **Part of the provider arc:** this is the pull-worker *kind* under
  [ADR-002](002-unified-provider-model.md); status per
  [ADR-004](004-provider-status-health.md); worker loop per
  [ADR-005](005-agent-skill-worker.md); remote registration per
  [ADR-003](003-control-plane-remote-cli.md).

## Context

Today an agent backend (`devin`, `codex`, `copilot`, `claude`) works by routsi
**shelling out** to that CLI on the proxy host. That requires the CLI installed *and
logged in* on the machine running routsi. It doesn't scale to "let anyone contribute
their agent": a teammate with a logged-in opencode/codex session on their laptop can't
serve requests through a central routsi without exposing their credentials to that host.

We want the **inverse**: a person opens their own agent anywhere (opencode, codex,
Claude Code, a custom script), runs a small loop that **registers a queue** with routsi
once, then **pulls questions, answers them locally, and posts answers back**. routsi
becomes a broker; workers connect outbound (NAT-friendly — no inbound ports on the
worker), using their own already-authenticated agent. A request routed to that queue
blocks until a worker answers.

This is the classic pull-based work queue / reverse-dispatch pattern (Celery workers,
GitHub Actions self-hosted runners, ngrok-style outbound tunnels). It complements — does
not replace — the local-CLI backends.

## Decision (proposed)

Add a **`type: queue`** backend and a small worker protocol.

**Broker** (in-memory, `internal/queue`, sketch already drafted):
- `Register(name)` — idempotent; creates the queue.
- `Submit(ctx, name, req) (answer, error)` — request path; enqueues a job, blocks until
  a worker answers or ctx/`maxWait` (5 min) elapses.
- `Poll(ctx, name, wait) (*Job, bool)` — worker long-poll for the next job.
- `Answer(name, jobID, content)` — worker returns the answer to the blocked Submit.

**Worker HTTP API** (under `/v1/workers/…`, guarded by the same bearer token as the API):
- `POST /v1/workers/register` `{ "name": "alices-opencode" }` → registers the queue.
- `GET  /v1/workers/{name}/jobs?wait=25` → long-poll; `200 {id, model, conversation_id,
  messages}` or `204` on timeout.
- `POST /v1/workers/{name}/jobs/{id}` `{ "content": "…" }` → deliver the answer.

**Addressing:** a registered queue is immediately a routable model named after the queue
(`alices-opencode`), listed in `/v1/models`. Dynamic — no config edit or restart.
Optionally also allow a config-declared `type: queue` model so a queue name is reserved /
documented even before a worker connects.

**The "agent skill":** a portable worker loop (`skills/routsi-worker/`) — a shell script
(curl-only) plus a README — parameterised by the agent command:

```sh
routsi-worker --url https://proxy --token $TOK --queue alices-opencode \
  --agent 'codex exec --skip-git-repo-check -'   # reads the question on stdin
```

It registers once, long-polls, pipes each question to `--agent`, posts stdout back. Any
tool that takes a prompt and prints an answer works (opencode, codex, claude -p, a python
script).

## Alternatives considered

1. **Only local CLIs (status quo).** Simple, but can't federate other people's
   logged-in agents; every agent must live on the proxy host. Rejected as the whole point.
2. **WebSocket / gRPC push to workers.** Lower latency, true streaming. More moving
   parts, harder for a shell-script worker, needs connection lifecycle management. Long-poll
   over plain HTTP is trivially scriptable and NAT-friendly; pick it for v1, leave WS as a
   later transport.
3. **External queue (Redis/NATS/SQS).** Durable, multi-instance. Adds an infra dependency
   and breaks the "single dependency-light binary" promise. In-memory first; a broker
   interface leaves room to back it with Redis later if multi-instance is needed.
4. **SSH reverse tunnel to each worker's local server.** Reuses ssh, but heavy per-worker
   setup and key management; doesn't give a clean job/answer abstraction.

## Consequences

Good:
- Anyone can contribute an agent with one loop; credentials never leave the worker.
- Workers are outbound-only (NAT/firewall friendly).
- Reuses the existing `Backend`, routing, stickiness, metrics, and auth unchanged — a
  queue is just another `Backend`.

Costs / open risks:
- **Streaming:** a pull worker returns a whole answer; v1 is non-streaming (faked off
  Complete, like the other agents). True token streaming needs a chunk-append endpoint —
  deferred.
- **Liveness:** if no worker is connected, requests block to `maxWait` then error. Need a
  fast-fail when a queue has zero recent pollers (return 503 quickly) — proposed:
  `Submit` errors immediately if `lastSeen` is stale / no worker ever polled.
- **At-least-once vs at-most-once:** if a worker dies mid-job the answer never comes and
  Submit times out; the job is dropped (at-most-once). A redelivery/visibility-timeout
  scheme is possible but adds complexity — v1 drops.
- **Conversation stickiness:** a queue is one logical model; multiple workers on the same
  queue means turn N+1 may hit a different worker (which lacks the session). Options: (a)
  one worker per queue (simple, documented); (b) sticky job→worker by conversation_id
  (needs worker identity). Propose (a) for v1.
- In-memory ⇒ single-instance and non-durable across restarts (in-flight jobs lost).
  Acceptable for the single-binary model; note it.

## Open questions for the owner

1. **Dynamic registration only, or also config-declared queues?** (I lean: support both —
   dynamic is the headline, config-declared reserves a name.)
2. **Zero-worker behaviour:** fast 503 when no worker has polled recently, vs block to
   timeout? (I lean: fast 503.)
3. **One-worker-per-queue for v1** (simplest sticky story), or allow a pool now?
4. **Worker skill form:** shell script only, or also a tiny Node file? (Earlier you said
   CLI-only; a curl shell script keeps it dependency-free.)
5. **Auth for workers:** reuse the same `auth.tokens_env` bearer (simplest), or a separate
   worker-token list?

Nothing lands until this is Accepted.
