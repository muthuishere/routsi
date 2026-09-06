-- Routsi analytics over an S3-compatible bucket.
-- ROUTSI_ANALYTICS_S3_GLOB example:
--   s3://routsi-analytics/routsi/usage/**/*.jsonl
-- Authentication is resolved by DuckDB's AWS credential chain; credentials
-- are never interpolated into this file or printed by Routsi.
INSTALL httpfs;
LOAD httpfs;

CREATE OR REPLACE SECRET routsi_analytics_s3 (
  TYPE s3,
  PROVIDER credential_chain
);

CREATE OR REPLACE VIEW routsi_usage AS
SELECT *
FROM read_ndjson_auto(getenv('ROUTSI_ANALYTICS_S3_GLOB'), union_by_name = true);

SELECT
  count(*) AS requests,
  sum(total_tokens) AS total_tokens,
  sum(prompt_tokens) AS prompt_tokens,
  sum(completion_tokens) AS completion_tokens,
  round(avg(latency_ms), 1) AS avg_latency_ms,
  approx_quantile(latency_ms, 0.95) AS p95_latency_ms,
  count(*) FILTER (WHERE status < 200 OR status >= 400) AS errors
FROM routsi_usage;

SELECT
  provider,
  selected_model,
  count(*) AS requests,
  sum(total_tokens) AS total_tokens,
  round(avg(latency_ms), 1) AS avg_latency_ms,
  approx_quantile(latency_ms, 0.95) AS p95_latency_ms
FROM routsi_usage
GROUP BY provider, selected_model
ORDER BY requests DESC;
