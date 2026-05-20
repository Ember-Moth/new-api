-- PostgreSQL production performance indexes for non-log tables.

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

ANALYZE abilities;
ANALYZE tasks;
ANALYZE midjourneys;
ANALYZE top_ups;
ANALYZE quota_data;
ANALYZE quota_data_daily;
ANALYZE quota_data_monthly;
ANALYZE user_subscriptions;
