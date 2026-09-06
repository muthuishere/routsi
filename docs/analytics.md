---
title: S3 and DuckDB analytics
---

# S3 and DuckDB analytics

Routsi can persist operational usage data to an S3-compatible bucket without putting
storage latency on the inference path. S3 is the durable source of truth; DuckDB reads
the bucket directly for historical aggregation. MinIO is the included local-development
implementation of the same S3 contract.

## What the dashboard shows

The live dashboard shows requests, estimated input/output/total tokens, routing split,
error rate, average/maximum latency, most-used providers and models, escalations, and S3
pipeline health. Values reset when Routsi restarts; bucket data supplies durable history.

Token counts are labelled estimated unless an upstream exposes trustworthy usage.

## Enable the S3 event stream

```yaml
analytics:
  bucket: routsi-analytics
  prefix: routsi/usage
  region: us-east-1
  profile: default
  flush_interval: 10s
  batch_size: 100
```

Routsi uses the AWS SDK credential chain. Each immutable JSONL object is partitioned as:

```text
routsi/usage/year=YYYY/month=MM/day=DD/hour=HH/<timestamp>-<uuid>.jsonl
```

Events contain model/provider selection, routing metadata, status, latency, token counts,
whether tools were requested, and a truncated hash of the conversation identifier.
Prompts, responses, tool arguments, credentials, and raw conversation IDs are excluded.

## Credentials for any S3-compatible service

Credential values never belong in `models.yaml`. Reference environment-variable names:

```yaml
analytics:
  bucket: my-routsi-usage
  prefix: routsi/usage
  endpoint: https://s3.example.com
  region: us-east-1
  use_path_style: true
  access_key: ${ROUTSI_S3_ACCESS_KEY}
  secret_key: ${ROUTSI_S3_SECRET_KEY}
  session_token: ${ROUTSI_S3_SESSION_TOKEN} # optional
```

Routsi expands exact `${VAR}` references when analytics starts. Literal credential
values are rejected. `access_key` and `secret_key` must be configured together. The
older `*_env` field names remain accepted for compatibility. When these fields are omitted, Routsi
uses the normal AWS chain: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, AWS profiles,
`aws login`/SSO, web identity, container credentials, or instance roles.

Common configurations:

| Service | `endpoint` | `region` | `use_path_style` |
|---|---|---|---|
| AWS S3 | omit | bucket region | `false` |
| MinIO | `http://HOST:PORT` | `us-east-1` | `true` |
| Cloudflare R2 | `https://ACCOUNT_ID.r2.cloudflarestorage.com` | `auto` | `false` |
| Other S3-compatible | vendor endpoint | vendor value | usually `true` |

Use HTTPS for non-local endpoints. The configured endpoint and bucket may appear in
authenticated operational views; credential values never do.

If an upload fails, the batch is written with owner-only permissions under
`~/.config/routsi/analytics-spool`. Queue overflow is counted rather than blocking an
LLM response. Pipeline health appears in `/stats` and the dashboard.

## Local MinIO

The included Compose file uses obvious development-only credentials:

```sh
docker compose -f compose.analytics.yml up -d
```

Configure Routsi:

```yaml
analytics:
  bucket: routsi-analytics
  prefix: routsi/usage
  region: us-east-1
  endpoint: http://127.0.0.1:19000
  use_path_style: true
```

Set `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` to the development values declared in
the Compose file before starting Routsi. Do not reuse those credentials outside local
development.

The development API and console listen on ports `19000` and `19001` to avoid
colliding with common local MinIO installations.

## Aggregate with DuckDB

DuckDB remains outside the inference binary so Routsi can retain static, CGO-free
cross-platform releases. Install DuckDB, let its AWS credential chain access the bucket,
then run the shipped queries:

```sh
export ROUTSI_ANALYTICS_S3_GLOB='s3://routsi-analytics/routsi/usage/**/*.jsonl'
duckdb < examples/analytics/aggregate.sql
```

The queries produce global totals and per-provider/model request, token, average-latency,
p95-latency, and error aggregates. DuckDB's `httpfs` extension supports AWS S3 and MinIO;
the bucket layout also works with other compatible services such as R2.

DuckDB can also compact and upload the raw stream as partitioned Parquet:

```sh
export ROUTSI_ANALYTICS_PARQUET_URI='s3://routsi-analytics/routsi/parquet'
duckdb < examples/analytics/compact.sql
```

Schedule compaction independently of Routsi. It uses UUID filenames with append mode,
so inference and analytical uploads remain separate and neither requires a database
daemon.
