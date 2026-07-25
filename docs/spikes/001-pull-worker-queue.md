# Spike 001: Pull-worker queue broker

De-risks [ADR-001](../adr/001-pull-worker-queue.md). Throwaway — not wired into the
server. Sketch preserved at `001-pull-worker-queue.broker.go.txt` (moved out of the
build tree; rename to `.go` under `internal/queue/` to run it).

## Question

Can an in-memory broker make a **blocking HTTP request** (the request path) wait for an
**out-of-band answer** delivered by a **long-polling worker**, using only stdlib and
channels — no external queue, no WebSocket?

## What the spike did

Drafted `internal/queue` with four operations and confirmed it compiles (`go build`):

```go
Register(name)                              // idempotent queue create
Submit(ctx, name, req) (answer, error)      // request path: enqueue + block on a chan
Poll(ctx, name, wait) (*Job, bool)          // worker long-poll: read pending, track inflight
Answer(name, jobID, content) error          // worker: deliver to the blocked Submit's chan
```

Mechanism: each `Job` carries an `answer chan result`. `Submit` pushes the job onto a
buffered `pending` channel and blocks on `job.answer` (or ctx / 5-min cap). `Poll` reads
`pending`, moves the job to an `inflight` map keyed by id. `Answer` looks up the id and
sends on `job.answer`, unblocking `Submit`. Timeout path drops the inflight entry so a
late answer is discarded.

## Findings

- **Feasible with stdlib channels** — no Redis/NATS needed for single-instance. The
  channel-per-job rendezvous is ~120 lines.
- **Long-poll is trivially scriptable** — a worker is just `GET …/jobs?wait=25` in a loop,
  so the "agent skill" can be a curl shell script (no client library).
- **Confirmed open risks** (now tracked in the ADR, not yet solved):
  - zero-worker → request blocks to the 5-min cap unless we fast-fail on stale `lastSeen`;
  - worker dies mid-job → at-most-once (Submit times out, job dropped);
  - multi-worker on one queue breaks conversation stickiness (turn N+1 may hit a worker
    without the session);
  - streaming needs a chunk-append endpoint the sketch doesn't have.

## Verdict

The broker approach is sound and cheap. Proceed to OpenSpec **after ADR-001 is Accepted**
and its open questions (zero-worker behaviour, one-worker-per-queue, dynamic vs config
registration, worker auth) are decided.
