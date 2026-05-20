-- PostgreSQL production performance indexes for logs.

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

ANALYZE logs;
