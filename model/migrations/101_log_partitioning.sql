-- PostgreSQL logs monthly partitioning migration.

SET LOCAL statement_timeout = 0;
SET LOCAL lock_timeout = '10s';

DO $$
DECLARE
  logs_relkind text;
  legacy_table text := 'logs_heap_before_partition';
  min_ts bigint;
  max_ts bigint;
  now_ts bigint := extract(epoch from (now() AT TIME ZONE 'UTC'))::bigint;
  cursor_month date;
  end_month date;
  partition_name text;
  from_ts bigint;
  to_ts bigint;
BEGIN
  SELECT c.relkind::text
  INTO logs_relkind
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = current_schema()
    AND c.relname = 'logs'
    AND c.relkind IN ('r', 'p')
  LIMIT 1;

  IF logs_relkind = 'r' THEN
    IF to_regclass(legacy_table) IS NOT NULL THEN
      RAISE EXCEPTION 'backup table % already exists; inspect and remove it before rerunning', legacy_table;
    END IF;

    IF EXISTS (SELECT 1 FROM logs WHERE created_at IS NULL LIMIT 1) THEN
      RAISE EXCEPTION 'logs.created_at contains NULL values; fix them before partition migration';
    END IF;

    LOCK TABLE logs IN ACCESS EXCLUSIVE MODE;
    EXECUTE 'ALTER SEQUENCE IF EXISTS logs_id_seq OWNED BY NONE';
    EXECUTE format('ALTER TABLE %I RENAME TO %I', 'logs', legacy_table);
  ELSIF logs_relkind IS NULL THEN
    RAISE NOTICE 'logs table does not exist; creating a partitioned table';
  ELSIF logs_relkind = 'p' THEN
    RAISE NOTICE 'logs table is already partitioned; only ensuring monthly partitions';
  ELSE
    RAISE EXCEPTION 'unsupported logs relkind: %', logs_relkind;
  END IF;

  IF logs_relkind IS DISTINCT FROM 'p' THEN
    EXECUTE 'CREATE SEQUENCE IF NOT EXISTS logs_id_seq AS bigint';
    EXECUTE $create_logs$
CREATE TABLE logs (
  id bigint NOT NULL DEFAULT nextval('logs_id_seq'::regclass),
  user_id bigint,
  created_at bigint NOT NULL,
  type bigint,
  content text,
  username text DEFAULT '',
  token_name text DEFAULT '',
  model_name text DEFAULT '',
  quota bigint DEFAULT 0,
  prompt_tokens bigint DEFAULT 0,
  completion_tokens bigint DEFAULT 0,
  use_time bigint DEFAULT 0,
  is_stream boolean,
  channel_id bigint,
  channel_name text,
  token_id bigint DEFAULT 0,
  "group" text,
  ip text DEFAULT '',
  request_id varchar(64) DEFAULT '',
  upstream_request_id varchar(128) DEFAULT '',
  other text
) PARTITION BY RANGE (created_at)
$create_logs$;
    EXECUTE 'ALTER SEQUENCE logs_id_seq OWNED BY logs.id';
  END IF;

  IF logs_relkind = 'r' THEN
    EXECUTE format('SELECT min(created_at), max(created_at) FROM %I', legacy_table)
      INTO min_ts, max_ts;
  ELSE
    SELECT min(created_at), max(created_at) INTO min_ts, max_ts FROM logs;
  END IF;

  min_ts := COALESCE(min_ts, now_ts);
  max_ts := GREATEST(COALESCE(max_ts, now_ts), now_ts);
  cursor_month := (date_trunc('month', to_timestamp(min_ts) AT TIME ZONE 'UTC') - interval '1 month')::date;
  end_month := (date_trunc('month', to_timestamp(max_ts) AT TIME ZONE 'UTC') + interval '4 months')::date;

  WHILE cursor_month < end_month LOOP
    partition_name := 'logs_y' || to_char(cursor_month, 'YYYY') || 'm' || to_char(cursor_month, 'MM');
    from_ts := extract(epoch from (cursor_month::timestamp AT TIME ZONE 'UTC'))::bigint;
    to_ts := extract(epoch from ((cursor_month + interval '1 month')::timestamp AT TIME ZONE 'UTC'))::bigint;
    EXECUTE format(
      'CREATE TABLE IF NOT EXISTS %I PARTITION OF logs FOR VALUES FROM (%s) TO (%s)',
      partition_name,
      from_ts,
      to_ts
    );
    cursor_month := (cursor_month + interval '1 month')::date;
  END LOOP;

  IF logs_relkind = 'r' THEN
    EXECUTE format($copy_logs$
INSERT INTO logs (
  id, user_id, created_at, type, content, username, token_name, model_name,
  quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id,
  channel_name, token_id, "group", ip, request_id, upstream_request_id, other
)
SELECT
  id, user_id, created_at, type, content, username, token_name, model_name,
  quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id,
  channel_name, token_id, "group", ip, request_id, upstream_request_id, other
FROM %I
ORDER BY id
$copy_logs$, legacy_table);

    PERFORM setval('logs_id_seq', GREATEST((SELECT COALESCE(max(id), 1) FROM logs), 1), true);
    EXECUTE format('DROP TABLE %I', legacy_table);
  END IF;
END $$;
