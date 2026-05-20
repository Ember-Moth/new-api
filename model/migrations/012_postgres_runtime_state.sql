-- PostgreSQL-backed runtime state for cross-replica coordination and no-Redis fallback.

CREATE TABLE IF NOT EXISTS runtime_rate_limits (
  "key" text PRIMARY KEY,
  "count" integer NOT NULL DEFAULT 0,
  window_start bigint NOT NULL,
  expires_at bigint NOT NULL,
  updated_at bigint NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runtime_rate_limits_expires_at
  ON runtime_rate_limits (expires_at);

CREATE TABLE IF NOT EXISTS verification_codes (
  purpose text NOT NULL,
  "key" text NOT NULL,
  code text NOT NULL,
  created_at bigint NOT NULL,
  expires_at bigint NOT NULL,
  PRIMARY KEY (purpose, "key")
);

CREATE INDEX IF NOT EXISTS idx_verification_codes_expires_at
  ON verification_codes (expires_at);

CREATE OR REPLACE FUNCTION notify_new_api_runtime_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  PERFORM pg_notify('new_api_runtime_events', TG_ARGV[0]);
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS trg_options_runtime_notify ON options;
CREATE TRIGGER trg_options_runtime_notify
AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON options
FOR EACH STATEMENT
EXECUTE FUNCTION notify_new_api_runtime_event('options');

DROP TRIGGER IF EXISTS trg_channels_runtime_notify ON channels;
CREATE TRIGGER trg_channels_runtime_notify
AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON channels
FOR EACH STATEMENT
EXECUTE FUNCTION notify_new_api_runtime_event('channels');

DROP TRIGGER IF EXISTS trg_abilities_runtime_notify ON abilities;
CREATE TRIGGER trg_abilities_runtime_notify
AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON abilities
FOR EACH STATEMENT
EXECUTE FUNCTION notify_new_api_runtime_event('channels');
