## 1. Broker (`internal/queue`) — ADR-001

- [x] 1.1 Define `Job{ID, Model, ConversationID, Messages}` and a broker interface with
  `Register(name)`, `Submit(ctx, name, req) (answer, error)`, `Poll(ctx, name, wait) (*Job, bool)`,
  `Answer(name, jobID, content) error`.
- [x] 1.2 Implement the in-memory broker: per-queue job channel + pending-answer map, injected
  `now()` clock, idempotent `Register`.
- [x] 1.3 `Submit`: fast-fail with a 503-mapped error when `lastSeen` is stale (>~30s) or the
  queue never polled; otherwise block until `Answer` or the 5-minute cap, then time out (drop
  the job, at-most-once).
- [x] 1.4 `Poll`: block up to `wait`, refresh `lastSeen` on every poll, return `(job, true)` or
  `(nil, false)`; `Answer`: deliver once, reject unknown/answered/expired ids as a 409-mapped error.
- [x] 1.5 Table-driven tests with a fake clock: fast-503, block-then-answer, wait-cap timeout,
  duplicate/expired 409, idle-poll heartbeat keeps the queue live.

## 2. `type: queue` Backend + config — ADR-001

- [x] 2.1 Add `type: queue` to config parsing; a declared queue reserves a name. Add top-level
  `workers:` with reserved-empty `auth: {}` placeholder (no enforcement in v1).
- [x] 2.2 Validate `type: queue` members/names at server start (post-discovery), consistent with
  existing default/tier/dynamic validation.
- [x] 2.3 Implement a queue `Backend`: `Complete` wraps `broker.Submit`; `Stream` faked off
  `Complete`; envelope + estimated usage like the other agents; set `X-Selected-Model`.
- [x] 2.4 List registered/reserved queues in `GET /v1/models` (dynamic, no restart).
- [x] 2.5 Tests: queue routes as a model, `X-Selected-Model` set, 503 surfaced when no worker,
  answer flows back through `Complete`.

## 3. Worker HTTP API (`internal/server`) — ADR-001

- [x] 3.1 `POST /v1/workers/register {name}` → idempotent register, make routable.
- [x] 3.2 `GET /v1/workers/{name}/jobs?wait=25` → `200 {id, model, conversation_id, messages}` or `204`.
- [x] 3.3 `POST /v1/workers/{name}/jobs/{id} {content}` → `200`, or `409` on duplicate/expired.
- [x] 3.4 Confirm the worker endpoints are unauthenticated in v1 (reserved `workers.auth`), while
  the request-side `/v1/*` guard is unchanged.
- [x] 3.5 `httptest` tests for register/poll(200,204)/answer(200,409) and the models listing.

## 4. Worker status (minimal ADR-004)

- [x] 4.1 Track per-queue state (`reserved`/`online`/`busy`/`stale`), last-seen, jobs served,
  jobs errored, last_error(+age); derive state from the injected clock (poll = heartbeat).
- [x] 4.2 `GET /v1/workers` → JSON status list; operational metadata only (no prompt content);
  guard it exactly like `/stats`.
- [x] 4.3 Add a dashboard Workers panel (state dot, last-seen, served, errored, last error),
  polling the status surface.
- [x] 4.4 Tests: state transitions on the fake clock (reserved→online→busy→stale),
  served-vs-errored counts, `/stats` guard applied to `/v1/workers`.

## 5. Worker CLI (`cmd/routsi`) — ADR-005

- [x] 5.1 Add `routsi worker run --proxy --queue --agent [--token]`: register once, long-poll,
  render messages → prompt on `--agent` stdin, capture stdout, post answer.
- [x] 5.2 Fail loud: actionable message + non-zero exit on hard errors; per-step human status
  (registered ✓, answered job <id> (<elapsed>)). `--token` accepted but ignored in v1.
- [x] 5.3 Add `routsi worker scaffold`: emit an editable curl-only shell script with the same contract.
- [x] 5.4 Tests: `worker run` against an `httptest` proxy (register→poll→answer round-trip);
  `worker scaffold` emits a non-empty script.

## 6. Wire-up, docs, verification

- [x] 6.1 Update `models.yaml` sample with a `type: queue` model and the `workers: { auth: {} }` block.
- [x] 6.2 Update AGENTS.md / CLAUDE.md with the queue backend, worker API, `routsi worker`, and the
  v1 caveats (non-streaming, one-worker-per-queue, in-memory, at-most-once, no worker auth).
- [x] 6.3 `go vet ./...` + `go test ./...` (`task test`) pass.
- [x] 6.4 Live-verify: register a queue, run `routsi worker run` against a real agent, route a
  request end-to-end, confirm fast-503 with no worker and the dashboard Workers panel.
