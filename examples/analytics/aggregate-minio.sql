-- Local MinIO variant. Values are consumed from the environment at runtime;
-- do not replace them with literal production credentials.
INSTALL httpfs;
LOAD httpfs;

CREATE OR REPLACE SECRET routsi_analytics_minio (
  TYPE s3,
  KEY_ID getenv('AWS_ACCESS_KEY_ID'),
  SECRET getenv('AWS_SECRET_ACCESS_KEY'),
  ENDPOINT '127.0.0.1:19000',
  URL_STYLE 'path',
  USE_SSL false
);

CREATE OR REPLACE VIEW routsi_usage AS
SELECT *
FROM read_ndjson_auto(getenv('ROUTSI_ANALYTICS_S3_GLOB'), union_by_name = true);

SELECT provider, selected_model, count(*) requests,
       sum(total_tokens) total_tokens,
       round(avg(latency_ms), 1) avg_latency_ms,
       approx_quantile(latency_ms, 0.95) p95_latency_ms
FROM routsi_usage
GROUP BY provider, selected_model
ORDER BY requests DESC;
