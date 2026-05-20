-- PostgreSQL embedded migration metadata.

CREATE TABLE IF NOT EXISTS schema_migrations (
  id varchar(255) PRIMARY KEY,
  checksum varchar(64) NOT NULL,
  applied_at bigint NOT NULL,
  execution_ms bigint NOT NULL
);
