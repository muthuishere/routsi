# ADR-016 — S3 is the durable analytics store; DuckDB is the query layer

**Status:** Accepted

## Context

Routsi needs durable token, usage, latency, provider, and model analytics without making
an object store or analytical database part of the synchronous inference path. Its
release pipeline also cross-compiles a static Go binary with `CGO_ENABLED=0`.

## Decision

Routsi asynchronously batches privacy-safe usage events into immutable, time-partitioned
JSONL objects in an S3-compatible bucket. A bounded channel isolates request handling;
failed uploads fall back to an owner-only local spool. Queue overflow and upload failure
are observable, but never fail inference.

DuckDB queries the bucket through `httpfs`. It is an operational analytics dependency,
not linked into the Routsi binary, preserving static cross-platform releases. MinIO is
the local/self-hosted S3 implementation rather than a distinct storage architecture.

## Consequences

- The bucket is the durable source of truth; live in-memory dashboard totals reset.
- Historical dashboards can later consume materialized DuckDB aggregates without
  changing the event contract.
- JSONL favors reliable append-only ingestion. DuckDB may compact older partitions to
  Parquet as an independent job.
- Prompts, responses, tool arguments, secrets, and raw conversation IDs remain outside
  analytics by default.
