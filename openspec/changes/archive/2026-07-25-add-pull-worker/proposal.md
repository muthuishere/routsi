## Why

Today an agent backend (`devin`, `codex`, `copilot`, `claude`) only works if that CLI is
installed **and logged in** on the routsi host, so a teammate with an already-authenticated
agent on their own laptop cannot serve requests through a central routsi without handing
over credentials. We want the inverse (ADR-001): a person runs a tiny outbound loop that
registers a named queue, long-polls for jobs, answers them with their local agent, and
posts the answer back. routsi becomes a broker; a queue is a routable model. This is
additive — nothing that works today changes (ADR README scope, 2026-07-25).

## What Changes

- Add a **`type: queue`** backend: a request routed to a queue name blocks until a worker
  answers, or **fast-503s** when no worker has polled recently (~30s freshness) or the
  5-minute cap elapses.
- Add a **worker HTTP API** under `/v1/workers/…` (no worker auth in v1):
  `POST /v1/workers/register`, `GET /v1/workers/{name}/jobs?wait=25`,
  `POST /v1/workers/{name}/jobs/{id}`, and `GET /v1/workers` (status list).
- Add config surface: a `type: queue` model reserves a queue name, and a top-level
  `workers: { auth: {} }` **reserved-empty placeholder** so worker auth can be added later
  without a breaking change (ADR-001/005 Decisions).
- Add a **minimal worker status slice** (ADR-004): per-queue state
  (`reserved`/`online`/`busy`/`stale`), last-seen, jobs served, jobs errored, last_error;
  the poll is the heartbeat; a dashboard Workers panel renders it.
- Add a **`routsi worker` CLI** (ADR-005): `routsi worker run --proxy URL --queue NAME
  --agent 'cmd'` and `routsi worker scaffold` (emit an editable shell script).

Design commitments (v1, all additive): one worker per queue; non-streaming (whole answer,
enveloped like the other agents); in-memory / single-instance; at-most-once delivery.

## Capabilities

### New Capabilities
- `pull-worker-queue`: the broker, the `type: queue` routable model, the worker
  registration/poll/answer HTTP API, blocking-submit with fast-503 liveness, and the
  `workers.auth` config placeholder (ADR-001).
- `worker-status`: per-queue operational state, liveness derived from the poll heartbeat,
  the `GET /v1/workers` JSON surface, and the dashboard Workers panel (ADR-004, minimal slice).
- `worker-cli`: the `routsi worker run` loop and `routsi worker scaffold` script emitter
  that turn any prompt-in/answer-out agent into a queue worker (ADR-005).

### Modified Capabilities
<!-- None. This change is additive; no existing requirement changes (ADR README, 2026-07-25). -->

## Impact

- New code: `internal/queue` (broker), `internal/server` (worker routes + models listing +
  dashboard panel), `internal/backend` (`type: queue` Backend), `internal/config`
  (`type: queue` + `workers.auth` placeholder), `internal/metrics`/status (worker state),
  `cmd/routsi` (`worker` subcommand).
- HTTP surface: adds `/v1/workers/*`; existing `/v1/*`, `/stats`, `/metrics`, `/health`,
  and the `Backend`/router/sticky/metrics/auth paths are unchanged.
- No new infrastructure dependency (in-memory broker); no worker auth yet (reserved).
- References: ADR-001 (transport), ADR-004 (status), ADR-005 (worker CLI/skill),
  docs/adr/README.md (additive-only scope).
