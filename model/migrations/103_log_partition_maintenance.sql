-- PostgreSQL logs monthly partition maintenance function.

CREATE OR REPLACE FUNCTION ensure_log_partitions(months_back integer DEFAULT 1, months_ahead integer DEFAULT 3)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
  safe_months_back integer := LEAST(GREATEST(COALESCE(months_back, 1), 0), 120);
  safe_months_ahead integer := LEAST(GREATEST(COALESCE(months_ahead, 3), 0), 120);
  logs_relkind text;
  min_ts bigint;
  max_ts bigint;
  current_month date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
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

  IF logs_relkind IS DISTINCT FROM 'p' THEN
    RAISE EXCEPTION 'logs table must be partitioned before ensuring log partitions';
  END IF;

  SELECT min(created_at), max(created_at)
  INTO min_ts, max_ts
  FROM logs;

  cursor_month := LEAST(
    COALESCE(date_trunc('month', to_timestamp(min_ts) AT TIME ZONE 'UTC')::date, current_month),
    (current_month - make_interval(months => safe_months_back))::date
  );
  end_month := (
    GREATEST(
      COALESCE(date_trunc('month', to_timestamp(max_ts) AT TIME ZONE 'UTC')::date, current_month),
      (current_month + make_interval(months => safe_months_ahead))::date
    ) + interval '1 month'
  )::date;

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
END;
$$;
