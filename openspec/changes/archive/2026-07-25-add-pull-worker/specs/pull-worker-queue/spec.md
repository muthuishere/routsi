## ADDED Requirements

### Requirement: Queue-backed routable model

routsi SHALL treat a registered worker queue as a routable model addressed by the queue
name over the OpenAI-compatible surface. A request whose model resolves to a queue SHALL be
handled by the queue Backend, which enqueues a job and blocks until a worker answers or the
liveness/timeout rules below fire. The selected model SHALL be disclosed via
`X-Selected-Model` exactly as for any other backend (ADR-001).

#### Scenario: Request routed to a live queue is answered by a worker
- **WHEN** a chat-completions request names a queue that has a recently-polling worker
- **THEN** routsi enqueues a job for that queue, the worker receives it on its long-poll,
  and the worker's posted answer is returned to the caller as a normal OpenAI-format
  (non-streaming, enveloped) completion with `X-Selected-Model` set to the queue name

#### Scenario: Queue name appears in the model catalog
- **WHEN** a client calls `GET /v1/models` after a queue has been registered
- **THEN** the queue name is listed as an available model with no restart or config edit

### Requirement: Dynamic worker registration

routsi SHALL expose `POST /v1/workers/register` accepting `{ "name": "<queue>" }` that
registers (idempotently creates) the named queue and makes it immediately routable. No
worker authentication is required in v1 (ADR-001 Decisions).

#### Scenario: First registration creates the queue
- **WHEN** a worker sends `POST /v1/workers/register` with a queue name that does not exist
- **THEN** routsi creates the queue, marks it routable, and responds success

#### Scenario: Re-registration is idempotent
- **WHEN** a worker registers a name that is already registered
- **THEN** routsi responds success without creating a duplicate queue or dropping in-flight state

### Requirement: Worker job long-poll

routsi SHALL expose `GET /v1/workers/{name}/jobs?wait=<seconds>` that long-polls for the
next job on the queue. It SHALL return `200` with a job body `{ id, model, conversation_id,
messages }` when a job is available, or `204 No Content` when the wait window elapses with no
job. Each poll SHALL refresh the queue's `lastSeen` heartbeat (ADR-004).

#### Scenario: Job delivered within the wait window
- **WHEN** a worker long-polls a queue that has (or receives, during the wait) a pending job
- **THEN** routsi responds `200` with the job's `id`, `model`, `conversation_id`, and `messages`

#### Scenario: Idle poll times out with 204
- **WHEN** a worker long-polls a queue with no pending job for the full `wait` window
- **THEN** routsi responds `204` and the poll counts as a heartbeat refreshing `lastSeen`

### Requirement: Worker answer delivery

routsi SHALL expose `POST /v1/workers/{name}/jobs/{id}` accepting `{ "content": "<answer>" }`
that delivers a worker's answer to the blocked request. It SHALL return `200` on the first
valid answer and `409 Conflict` when the job id is unknown, already answered, or expired
(duplicate/expired). Delivery is at-most-once (ADR-001).

#### Scenario: First answer unblocks the caller
- **WHEN** a worker posts an answer for a job that is still awaiting a response
- **THEN** routsi returns `200` and the blocked caller receives that content as the completion

#### Scenario: Duplicate or expired answer is rejected
- **WHEN** a worker posts an answer for a job id that was already answered or has expired
- **THEN** routsi returns `409` and does not alter the already-delivered (or timed-out) result

### Requirement: Fast-fail liveness and wait cap

The queue Backend SHALL fast-fail a request with `503 Service Unavailable` when the queue has
no recent poller — no worker has ever polled, or the last poll is older than the ~30s
freshness window — instead of blocking. When a worker is live, a request SHALL block at most
5 minutes before failing (ADR-001).

#### Scenario: No live worker returns 503 immediately
- **WHEN** a request is routed to a queue whose `lastSeen` is stale (older than ~30s) or that
  no worker has ever polled
- **THEN** routsi returns `503` promptly without waiting for the 5-minute cap

#### Scenario: Live worker but no answer within the cap
- **WHEN** a queue has a recently-polling worker but no answer is posted within the 5-minute
  cap
- **THEN** routsi stops blocking and returns a timeout error, dropping the job (at-most-once)

### Requirement: Single-instance, one-worker-per-queue semantics

The v1 broker SHALL be in-memory and single-instance, and SHALL assume one worker per queue.
In-flight jobs are not durable across a proxy restart. This preserves conversation
stickiness (turn N+1 reaches the same worker holding the session) (ADR-001).

#### Scenario: Conversation stays on one worker
- **WHEN** a conversation with a stable `conversation_id` sends successive turns to a queue
- **THEN** each turn's job carries that `conversation_id` to the single worker on that queue

#### Scenario: In-flight jobs are lost on restart
- **WHEN** the proxy restarts while jobs are enqueued or awaiting answers
- **THEN** those in-flight jobs are dropped and their callers receive an error (documented v1 limit)

### Requirement: Reserved worker-auth configuration placeholder

Configuration SHALL support declaring a `type: queue` model that reserves a queue name, and a
top-level `workers` block with a reserved-empty `auth: {}` placeholder. In v1 the placeholder
imposes no authentication; it exists so worker auth can be added later without a breaking
change (ADR-001/005 Decisions).

#### Scenario: Config-declared queue reserves a name
- **WHEN** a `type: queue` model is declared in config and validated at server start
- **THEN** the queue name is reserved and listed as a model even before any worker connects

#### Scenario: Empty workers.auth is accepted and enforces no auth
- **WHEN** config contains `workers: { auth: {} }`
- **THEN** the server starts, imposes no worker authentication, and accepts unauthenticated
  worker registration, polling, and answers
