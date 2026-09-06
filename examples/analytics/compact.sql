-- Compact raw Routsi JSONL objects into partitioned Parquet and upload the
-- result through DuckDB. Required environment variables:
--   ROUTSI_ANALYTICS_S3_GLOB     s3://bucket/prefix/**/*.jsonl
--   ROUTSI_ANALYTICS_PARQUET_URI s3://bucket/routsi/parquet
INSTALL httpfs;
LOAD httpfs;

CREATE OR REPLACE SECRET routsi_analytics_s3 (
  TYPE s3,
  PROVIDER credential_chain
);

SET VARIABLE raw_glob = getenv('ROUTSI_ANALYTICS_S3_GLOB');
SET VARIABLE parquet_uri = getenv('ROUTSI_ANALYTICS_PARQUET_URI');

CREATE OR REPLACE TEMP TABLE usage_compaction AS
SELECT *,
       year(CAST(time AS TIMESTAMPTZ)) AS event_year,
       month(CAST(time AS TIMESTAMPTZ)) AS event_month,
       day(CAST(time AS TIMESTAMPTZ)) AS event_day
FROM read_ndjson_auto(getvariable('raw_glob'), union_by_name = true);

COPY usage_compaction TO (getvariable('parquet_uri')) (
  FORMAT parquet,
  PARTITION_BY (event_year, event_month, event_day),
  APPEND,
  FILENAME_PATTERN 'usage_{uuid}'
);
