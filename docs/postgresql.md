# PostgreSQL configuration and verification

The primary database requires PostgreSQL >= 18 and an explicit `SQL_DSN` URL:

```dotenv
SQL_DSN=postgresql://user:password@postgres-host:5432/new-api
```

This project targets a fresh deployment. For PostgreSQL 18 storage layout, DragonflyDB configuration, verification results, and the optimization roadmap, see [PostgreSQL 18 and DragonflyDB](./postgresql18-dragonfly.md).

`postgres://` and `postgresql://` are accepted. Missing configuration, `local`, file paths, and other database drivers are rejected at startup. Historical database migrations are outside the deployment scope.

Logs share the primary database unless `LOG_SQL_DSN` is set. Separate logs support PostgreSQL or ClickHouse:

```dotenv
LOG_SQL_DSN=postgresql://user:password@postgres-host:5432/new-api-log
# Alternative:
# LOG_SQL_DSN=clickhouse://default:password@clickhouse-host:9000/new_api_logs
```

The backend reads `.env` from its working directory and also accepts environment variables. PostgreSQL data needs a separate backup; the application's local data directory does not contain the database.

## Development and tests

`make dev` starts the existing PostgreSQL/DragonflyDB development stack and the frontend. Database tests use a separate PostgreSQL connection supplied through `TEST_POSTGRES_DSN`. The test role must be able to create and drop schemas in that database. Fixtures create unique schemas through `internal/testdb`; they do not reuse application tables.

For example, start a disposable test server and run the backend suite:

```sh
docker run --rm --name new-api-postgres-test \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=new_api_test \
  -p 127.0.0.1:55432:5432 postgres:18-alpine
```

In another terminal, after PostgreSQL reports that it is ready:

```sh
TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:55432/new_api_test?sslmode=disable' make test
```

CI supplies a PostgreSQL 18 service and the same environment variable. Tests fail explicitly if a required test database is unavailable.
