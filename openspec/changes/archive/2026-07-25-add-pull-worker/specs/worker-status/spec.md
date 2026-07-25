## ADDED Requirements

### Requirement: Per-queue operational state

routsi SHALL compute and expose an operational state for each worker queue, drawn from the
minimal set `reserved` (declared, never active), `online` (a worker polled recently, idle),
`busy` (a job is in flight), and `stale` (no poll within the ~30s freshness window). State
SHALL be derived from an injected clock so transitions are deterministically testable
(ADR-004).

#### Scenario: Reserved before any worker connects
- **WHEN** a `type: queue` model is declared in config but no worker has ever polled
- **THEN** its state is reported as `reserved`

#### Scenario: Online after a recent poll
- **WHEN** a worker polled within the freshness window and holds no in-flight job
- **THEN** the queue state is reported as `online`

#### Scenario: Busy while a job is in flight
- **WHEN** a job has been handed to the worker and no answer has yet been posted
- **THEN** the queue state is reported as `busy`

#### Scenario: Stale after the freshness window lapses
- **WHEN** the last poll is older than the ~30s freshness window
- **THEN** the queue state is reported as `stale`

### Requirement: Poll is the liveness heartbeat

Worker liveness SHALL be derived solely from the long-poll: every `GET /v1/workers/{name}/jobs`
refreshes `lastSeen`, and there is no separate heartbeat endpoint. A queue transitions to
`stale` when no poll arrives within the freshness window (ADR-004).

#### Scenario: Idle poll keeps the queue online
- **WHEN** a worker keeps long-polling and receiving `204` responses while idle
- **THEN** `lastSeen` keeps refreshing and the queue remains `online` rather than going `stale`

### Requirement: Worker status surface

routsi SHALL expose `GET /v1/workers` returning JSON listing, per queue, its state,
last-seen time, jobs served, jobs errored, and last_error (with age when present). The surface
SHALL carry only operational metadata (names, timers, counts) and no prompt content, and SHALL
be protected by the same guard as `/stats` (ADR-004).

#### Scenario: Status lists per-queue counters
- **WHEN** an operator calls `GET /v1/workers`
- **THEN** the response lists each queue's state, last-seen, jobs served, jobs errored, and
  last_error, and contains no prompt or message content

#### Scenario: Status shares the /stats guard
- **WHEN** inbound auth is configured and a request to `GET /v1/workers` lacks a valid token
- **THEN** the request is rejected exactly as a request to `/stats` would be

#### Scenario: Served-vs-errored exposes liveness theatre
- **WHEN** a worker polls (staying `online`) but never posts answers, so jobs time out
- **THEN** the jobs-errored count rises while jobs-served stays flat, making the
  "polls but never answers" case visible

### Requirement: Dashboard Workers panel

The embedded dashboard SHALL include a Workers panel that renders each queue's state (as a
state dot), last-seen, jobs served, jobs errored, and last error, polling the status surface
like the rest of the dashboard (ADR-004).

#### Scenario: Panel reflects queue state
- **WHEN** the dashboard polls the worker status surface
- **THEN** the Workers panel shows each queue with a state dot, last-seen, served, errored,
  and last error
