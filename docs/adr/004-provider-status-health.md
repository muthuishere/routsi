# ADR-004: Provider status & health

- **Status:** Proposed (discussion)
- **Date:** 2026-07-25
- **Depends on:** ADR-002 (providers have state)

## Context

"It's working" from a human is worthless (owner: *"sometime it will fail badly, token
expired whatever"*). Operators need to see, at a glance, which providers/workers are
alive and — when they're not — **why**: auth, network, or crashed. routsi already has a
dashboard (`/`), `/stats`, and `/metrics`; this ADR extends them to providers.

## Decision (proposed)

Every provider has a computed **state**:

`reserved` (declared, never active) · `online` (healthy/idle) · `busy` (in-flight job) ·
`stale` (no signal recently) · `error` (last op failed) · `offline`.

**Liveness signal by kind:**
- **pull-worker:** the long-poll *is* the heartbeat — `lastSeen` refreshes on every
  `GET /jobs` (idle poll returns 204 every ~25s). No separate heartbeat endpoint. Stale
  after ~30s without a poll.
- **forward/translate:** periodic health via the existing discovery ping (`GET /models`).
- **local-agent:** liveness = last invocation result.

**Failure taxonomy** (the three-way diagnosis — Vel):
- **auth** — expired/invalid token. An unauthenticated worker never registers, so it
  can't be shown by name; surface an **aggregate auth-rejection counter** ("N in 5m").
  Aggregate-only, no token oracle (Senthil).
- **network** — worker can't reach proxy → goes `stale`.
- **crashed / mid-job** — in-flight job times out → `error` + `last_error`.

**Surfaces:**
- `GET /v1/workers` (JSON) — per-provider state, last-seen, jobs served, jobs errored,
  last_error(+age); guarded like `/stats`.
- **Dashboard Workers panel** — state dot, last-seen, served, errored, last error; plus a
  top-level "auth rejections (5m)" tile.
- **Worker script fails loud** (ADR-005): prints actionable cause, exits non-zero on 401.

## Alternatives considered

1. **Explicit `/heartbeat` endpoint.** Redundant — the poll already proves liveness.
   Consider only if a worker can hold a job longer than the poll window.
2. **Trust worker self-reported status.** Defeats the purpose ("don't let people *say*
   it's working").

## Consequences

- Served-vs-timed-out counts expose the **"polls but never answers"** liveness-theatre case.
- Clock injected (`now()`) for deterministic state-transition tests (as sticky store does).
- Status is operational metadata only (names/timers/counts) — no prompt content — so it
  shares the `/stats` guard.

## Open questions

1. Forward **health-check cadence** (every N seconds? only on error?).
2. Do we want an explicit heartbeat for **long-running jobs** past the poll window?
3. Retention of `last_error` / counters — rolling window vs since-start.
