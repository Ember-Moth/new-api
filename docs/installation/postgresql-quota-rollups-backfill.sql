-- new-api PostgreSQL quota_data daily/monthly rollup backfill.
--
-- Run after the application has migrated quota_data_daily and
-- quota_data_monthly. The script locks quota_data writes during the rebuild so
-- the summary tables are exactly aligned with the hourly source table.

SET statement_timeout = 0;
SET lock_timeout = '10s';

BEGIN;

LOCK TABLE quota_data IN SHARE MODE;
LOCK TABLE quota_data_daily, quota_data_monthly IN ACCESS EXCLUSIVE MODE;

TRUNCATE TABLE quota_data_daily, quota_data_monthly RESTART IDENTITY;

INSERT INTO quota_data_daily (
  user_id, username, model_name, created_at, "count", quota, token_used
)
SELECT
  user_id,
  username,
  model_name,
  created_at - (created_at % 86400) AS created_at,
  sum("count") AS "count",
  sum(quota) AS quota,
  sum(token_used) AS token_used
FROM quota_data
GROUP BY user_id, username, model_name, created_at - (created_at % 86400);

INSERT INTO quota_data_monthly (
  user_id, username, model_name, created_at, "count", quota, token_used
)
SELECT
  user_id,
  username,
  model_name,
  extract(epoch from date_trunc('month', to_timestamp(created_at) AT TIME ZONE 'UTC'))::bigint AS created_at,
  sum("count") AS "count",
  sum(quota) AS quota,
  sum(token_used) AS token_used
FROM quota_data
GROUP BY
  user_id,
  username,
  model_name,
  extract(epoch from date_trunc('month', to_timestamp(created_at) AT TIME ZONE 'UTC'))::bigint;

COMMIT;

ANALYZE quota_data_daily;
ANALYZE quota_data_monthly;
