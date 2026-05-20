-- PostgreSQL quota_data daily/monthly rollup structures.

CREATE UNIQUE INDEX IF NOT EXISTS uq_quota_data_hour
  ON quota_data (user_id, username, model_name, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS uq_quota_data_daily_bucket
  ON quota_data_daily (user_id, username, model_name, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS uq_quota_data_monthly_bucket
  ON quota_data_monthly (user_id, username, model_name, created_at);

CREATE OR REPLACE FUNCTION sync_quota_data_rollups()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  delta_count bigint;
  delta_quota bigint;
  delta_token_used bigint;
  day_bucket bigint;
  month_bucket bigint;
BEGIN
  IF TG_OP = 'INSERT' THEN
    delta_count := NEW."count";
    delta_quota := NEW.quota;
    delta_token_used := NEW.token_used;
  ELSIF TG_OP = 'UPDATE' THEN
    delta_count := NEW."count" - OLD."count";
    delta_quota := NEW.quota - OLD.quota;
    delta_token_used := NEW.token_used - OLD.token_used;
  ELSE
    RETURN NEW;
  END IF;

  IF delta_count = 0 AND delta_quota = 0 AND delta_token_used = 0 THEN
    RETURN NEW;
  END IF;

  day_bucket := NEW.created_at - (NEW.created_at % 86400);
  month_bucket := extract(epoch from date_trunc('month', to_timestamp(NEW.created_at) AT TIME ZONE 'UTC'))::bigint;

  INSERT INTO quota_data_daily (user_id, username, model_name, created_at, "count", quota, token_used)
  VALUES (NEW.user_id, NEW.username, NEW.model_name, day_bucket, delta_count, delta_quota, delta_token_used)
  ON CONFLICT (user_id, username, model_name, created_at) DO UPDATE SET
    "count" = quota_data_daily."count" + EXCLUDED."count",
    quota = quota_data_daily.quota + EXCLUDED.quota,
    token_used = quota_data_daily.token_used + EXCLUDED.token_used;

  INSERT INTO quota_data_monthly (user_id, username, model_name, created_at, "count", quota, token_used)
  VALUES (NEW.user_id, NEW.username, NEW.model_name, month_bucket, delta_count, delta_quota, delta_token_used)
  ON CONFLICT (user_id, username, model_name, created_at) DO UPDATE SET
    "count" = quota_data_monthly."count" + EXCLUDED."count",
    quota = quota_data_monthly.quota + EXCLUDED.quota,
    token_used = quota_data_monthly.token_used + EXCLUDED.token_used;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_quota_data_rollups ON quota_data;

CREATE TRIGGER trg_quota_data_rollups
AFTER INSERT OR UPDATE OF "count", quota, token_used ON quota_data
FOR EACH ROW
EXECUTE FUNCTION sync_quota_data_rollups();
