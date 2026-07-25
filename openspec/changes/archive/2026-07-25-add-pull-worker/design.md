## Context

routsi already exposes an OpenAI-compatible surface where every model/agent is addressable
by name, backed by a `Backend` interface (`Complete`/`Stream`), a rules router, a sticky
store, in-process metrics, and inbound bearer/mTLS auth. Agent backends today shell out to a
locally installed, logged-in CLI on the proxy host. ADR-001 adds the inverse: remote workers
connect outbound and supply answers. This design covers the v1, additive slice agreed by the
owner on 2026-07-25 (ADR README): pull-worker queue (001) + agent-skill worker CLI (005) +
a minimal worker-status slice (004). The parked unified-registry/control-plane refactors
(ADR-002/003) are out of scope.

## Goals / Non-Goals

**Goals:**
- A `type: queue` model that is a first-class routable name over the existing OpenAI surface.
- An outbound, NAT-friendly worker protocol (register → long-poll → answer) with no inbound
  ports on the worker and no credentials on the proxy host.
- Fast failure when no worker is live, instead of a long block.
- Reuse of the existing `Backend`, router, stickiness, metrics, and inbound auth unchanged.
- A one-command worker loop shipped inside the existing CLI binary.
- Minimal, honest operational status (poll = heartbeat; served vs errored counts).

**Non-Goals:**
- Worker authentication (v1: none; reserved empty `workers.auth` placeholder only).
- Token streaming from a worker (v1 returns a whole answer, enveloped off `Complete`).
- Worker pools / multiple workers per queue (v1: one worker per queue).
- Durability / multi-instance (v1: in-memory, single-instance; in-flight jobs lost on restart).
- At-least-once redelivery / visibility timeouts (v1: at-most-once).
- Any change to existing providers, routing, or the parked ADR-002/003 refactors.

## Decisions

- **Broker in `internal/queue`, in-memory.** `Register(name)` (idempotent),
  `Submit(ctx, name, req) (answer, error)` (enqueue + block until answer or `maxWait` 5 min),
  `Poll(ctx, name, wait) (*Job, bool)` (worker long-poll), `Answer(name, jobID, content)`
  (unblock Submit). A broker interface leaves room to back it with Redis later.
- **A queue is a `Backend`.** `type: queue` wraps `Submit` behind `Complete`; routing,
  stickiness, `X-Selected-Model`, and metrics treat it like any other model. `Stream` is
  faked off `Complete` (same as the other agents).
- **Fast-503 liveness (ADR-001).** `Submit` returns 503 immediately if the queue has no
  recent poller — `lastSeen` older than ~30s (or never polled). Otherwise it blocks up to the
  5-minute cap and returns 504/error on timeout. The poll refreshes `lastSeen` (idle poll
  returns 204 roughly every 25s), so the heartbeat and the work channel are the same call
  (ADR-004).
- **Worker HTTP API, no auth in v1.** `POST /v1/workers/register {name}`;
  `GET /v1/workers/{name}/jobs?wait=25` → `200 {id, model, conversation_id, messages}` or
  `204`; `POST /v1/workers/{name}/jobs/{id} {content}` → `200`, or `409` on a duplicate/expired
  answer; `GET /v1/workers` → status list. `workers.auth` is a reserved empty config block so
  auth is a later non-breaking addition. The `--token` CLI flag is accepted but ignored.
- **Dynamic registration is the headline; config reservation is optional.** Registering a
  queue makes its name immediately routable and listed in `/v1/models` with no restart; a
  config-declared `type: queue` model reserves/documents a name before any worker connects
  (state `reserved`).
- **One worker per queue (ADR-001).** Keeps conversation stickiness clean: turn N+1 reaches
  the same worker, which still holds the session. A pool is a later ADR.
- **At-most-once (ADR-001).** If a worker dies mid-job, `Submit` times out and the job is
  dropped; no redelivery in v1.
- **Status is operational metadata only (ADR-004).** State machine
  `reserved → online → busy → stale`, plus `last_error`; names/timers/counts only, no prompt
  content, so `GET /v1/workers` shares the `/stats` guard. Clock is injected (`now()`) for
  deterministic state-transition tests, as the sticky store does.
- **Worker loop in the CLI (ADR-005).** `routsi worker run` registers once, long-polls,
  renders messages to a prompt, pipes to `--agent` on stdin, captures stdout, posts it back,
  and **fails loud** (non-zero exit + actionable message) on a hard error. `routsi worker
  scaffold` emits an editable curl-only shell script with the same contract.

## Risks / Trade-offs

- **No worker connected → callers get a fast 503.** Chosen over a long block; the tradeoff is
  that a queue with a briefly-restarting worker can reject requests during the gap.
- **Non-streaming.** A pull worker returns the whole answer; long answers have no partial
  output. True streaming needs a chunk-append endpoint — deferred.
- **In-memory / single-instance.** A proxy restart loses in-flight jobs; acceptable for the
  single-binary model, documented.
- **At-most-once.** A worker crash mid-job silently drops that one job (caller sees a timeout);
  a redelivery/visibility-timeout scheme is possible later.
- **No worker auth in v1.** Anyone who can reach `/v1/workers/register` can claim a queue
  name; mitigated by the reserved `workers.auth` placeholder and the existing inbound auth on
  the request side, and called out as a v1 limitation.
- **Liveness theatre.** A worker that polls but never answers looks "online"; the served-vs-
  errored counts in status are specifically there to expose it (ADR-004).
