-- new-api PostgreSQL production performance indexes.
--
-- Run with autocommit enabled. Do not wrap this file in BEGIN/COMMIT because
-- CREATE INDEX CONCURRENTLY is not allowed inside a transaction block.
--
-- Example:
--   psql "$SQL_DSN" -v ON_ERROR_STOP=1 -f docs/installation/postgresql-performance-indexes.sql

SET statement_timeout = 0;
SET lock_timeout = '5s';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_abilities_lookup
  ON abilities ("group", model, enabled, priority DESC, weight DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_user_id_id
  ON tasks (user_id, id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_submit_time_id
  ON tasks (submit_time, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_polling_claim
  ON tasks (polling_at, id)
  WHERE progress <> '100%' AND status NOT IN ('FAILURE', 'SUCCESS');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_timeout_claim
  ON tasks (submit_time, polling_at, id)
  WHERE progress <> '100%' AND status NOT IN ('FAILURE', 'SUCCESS');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_unfinished_submit_time
  ON tasks (submit_time, id)
  WHERE progress <> '100%' AND status NOT IN ('FAILURE', 'SUCCESS');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_midjourneys_polling_claim
  ON midjourneys (polling_at, id)
  WHERE progress <> '100%';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_top_ups_user_create_id
  ON top_ups (user_id, create_time DESC, id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_top_ups_user_id_id_desc
  ON top_ups (user_id, id DESC);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_quota_data_hour
  ON quota_data (user_id, username, model_name, created_at);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_quota_data_daily_bucket
  ON quota_data_daily (user_id, username, model_name, created_at);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_quota_data_monthly_bucket
  ON quota_data_monthly (user_id, username, model_name, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_user_id_created
  ON quota_data (user_id, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_username_created
  ON quota_data (username, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_created_model
  ON quota_data (created_at, model_name);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_daily_user_id_created
  ON quota_data_daily (user_id, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_daily_username_created
  ON quota_data_daily (username, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_daily_created_model
  ON quota_data_daily (created_at, model_name);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_monthly_user_id_created
  ON quota_data_monthly (user_id, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_monthly_username_created
  ON quota_data_monthly (username, created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_data_monthly_created_model
  ON quota_data_monthly (created_at, model_name);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_subscriptions_status_end_id
  ON user_subscriptions (status, end_time, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_subscriptions_status_next_reset
  ON user_subscriptions (status, next_reset_time, id);

-- PostgreSQL does not support CREATE INDEX CONCURRENTLY on a partitioned parent
-- table, so logs parent indexes intentionally use regular CREATE INDEX.
CREATE INDEX IF NOT EXISTS idx_logs_type_created_at
  ON logs (type, created_at);

CREATE INDEX IF NOT EXISTS idx_created_at_id
  ON logs (id, created_at);

CREATE INDEX IF NOT EXISTS idx_created_at_type
  ON logs (created_at, type);

CREATE INDEX IF NOT EXISTS idx_logs_channel_id
  ON logs (channel_id);

CREATE INDEX IF NOT EXISTS idx_logs_group
  ON logs ("group");

CREATE INDEX IF NOT EXISTS idx_logs_ip
  ON logs (ip);

CREATE INDEX IF NOT EXISTS idx_logs_model_name
  ON logs (model_name);

CREATE INDEX IF NOT EXISTS idx_logs_request_id
  ON logs (request_id);

CREATE INDEX IF NOT EXISTS idx_logs_token_id
  ON logs (token_id);

CREATE INDEX IF NOT EXISTS idx_logs_token_name
  ON logs (token_name);

CREATE INDEX IF NOT EXISTS idx_logs_upstream_request_id
  ON logs (upstream_request_id);

CREATE INDEX IF NOT EXISTS idx_logs_user_id
  ON logs (user_id);

CREATE INDEX IF NOT EXISTS idx_logs_user_id_id_desc
  ON logs (user_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_logs_user_type_id
  ON logs (user_id, type, id DESC);

CREATE INDEX IF NOT EXISTS idx_logs_username
  ON logs (username);

CREATE INDEX IF NOT EXISTS idx_user_id_id
  ON logs (user_id, id);

CREATE INDEX IF NOT EXISTS index_username_model_name
  ON logs (model_name, username);

CREATE INDEX IF NOT EXISTS idx_logs_created_at_id_desc
  ON logs (created_at DESC, id DESC);

-- Optional fuzzy-search indexes. Enable these only when admin search by
-- contains matching is frequent enough to justify extra write amplification.
--
-- CREATE EXTENSION IF NOT EXISTS pg_trgm;
--
-- CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_top_ups_trade_no_trgm
--   ON top_ups USING gin (trade_no gin_trgm_ops);
--
-- CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_username_trgm
--   ON logs USING gin (username gin_trgm_ops);
--
-- CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_token_name_trgm
--   ON logs USING gin (token_name gin_trgm_ops);
--
-- CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_model_name_trgm
--   ON logs USING gin (model_name gin_trgm_ops);

ANALYZE abilities;
ANALYZE tasks;
ANALYZE midjourneys;
ANALYZE top_ups;
ANALYZE quota_data;
ANALYZE quota_data_daily;
ANALYZE quota_data_monthly;
ANALYZE user_subscriptions;
ANALYZE logs;
