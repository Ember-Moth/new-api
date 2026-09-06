# PostgreSQL configuration and verification

The primary database requires PostgreSQL >= 18 and an explicit `SQL_DSN` URL:

```dotenv
SQL_DSN=postgresql://user:password@postgres-host:5432/new-api
```

This project targets a fresh deployment. For PostgreSQL 18 storage layout, DragonflyDB configuration, verification results, and the optimization roadmap, see [PostgreSQL 18 and DragonflyDB](./postgresql18-dragonfly.md).

`postgres://` and `postgresql://` are accepted. Missing configuration, `local`, file paths, and other database drivers are rejected at startup. Historical database migrations are outside the deployment scope.

Logs require a separate ClickHouse database and an explicit `LOG_SQL_DSN`. Missing configuration and PostgreSQL log URLs are rejected; logs never fall back to the primary database:

```dotenv
LOG_SQL_DSN=clickhouse://default:password@clickhouse-host:9000/new_api_logs
```

The backend reads `.env` from its working directory and also accepts environment variables. PostgreSQL and ClickHouse data need separate backups; the application's local data directory does not contain the database.

## Development and tests

`make dev` starts the existing PostgreSQL/ClickHouse/DragonflyDB development stack and the frontend. Database tests use a separate PostgreSQL connection supplied through `TEST_POSTGRES_DSN`. Log tests additionally require `TEST_CLICKHOUSE_DSN`. The PostgreSQL test role must be able to create and drop schemas; the ClickHouse test role must be able to create and drop disposable databases. Fixtures create unique schemas through `internal/testdb`; they do not reuse application tables.

For example, start a disposable test server and run the backend suite:

```sh
docker run --rm --name new-api-postgres-test \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=new_api_test \
  -p 127.0.0.1:55432:5432 postgres:18-alpine
```

Start a disposable ClickHouse server in another terminal:

```sh
docker run --rm --name new-api-clickhouse-test \
  -e CLICKHOUSE_USER=test -e CLICKHOUSE_PASSWORD=test \
  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 \
  -p 127.0.0.1:59000:9000 clickhouse/clickhouse-server:24.8
```

After both services report that they are ready:

```sh
TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:55432/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://test:test@127.0.0.1:59000/default' make test
```

CI supplies PostgreSQL 18 and ClickHouse services with both environment variables. Tests fail explicitly if a required test database is unavailable.
