# PostgreSQL configuration and verification

The primary database requires PostgreSQL >= 9.6 and an explicit `SQL_DSN` URL:

```dotenv
SQL_DSN=postgresql://user:password@postgres-host:5432/new-api
```

`postgres://` and `postgresql://` are accepted. Missing configuration, `local`, file paths, and other database drivers are rejected at startup. Existing SQLite or MySQL installations must migrate their data to PostgreSQL before deploying this version; the application does not convert their data automatically.

Logs share the primary database unless `LOG_SQL_DSN` is set. Separate logs support PostgreSQL or ClickHouse:

```dotenv
LOG_SQL_DSN=postgresql://user:password@postgres-host:5432/new-api-log
# Alternative:
# LOG_SQL_DSN=clickhouse://default:password@clickhouse-host:9000/new_api_logs
```

The packaged Electron app reads `.env` from its application data directory and also accepts inherited environment variables. PostgreSQL data needs a separate backup; the application's local data directory does not contain the database.

## Development and tests

`make dev` starts the existing PostgreSQL/Redis development stack and the frontend. Database tests use a separate PostgreSQL connection supplied through `TEST_POSTGRES_DSN`. The test role must be able to create and drop schemas in that database. Fixtures create unique schemas through `internal/testdb`; they do not reuse application tables.

For example, start a disposable test server and run the backend suite:

```sh
docker run --rm --name new-api-postgres-test \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=new_api_test \
  -p 127.0.0.1:55432:5432 postgres:16-alpine
```

In another terminal, after PostgreSQL reports that it is ready:

```sh
TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:55432/new_api_test?sslmode=disable' make test
```

CI supplies a PostgreSQL 16 service and the same environment variable. Tests fail explicitly if a required test database is unavailable.

## Verification of database support removal — 2026-09-05

Real database versions:

- PostgreSQL **16.15**, initialized in `/tmp/new-api-postgres-data`, listening only on `127.0.0.1:55432`.
- ClickHouse **26.9.1.762**, with PostgreSQL as its application's primary database. Native and HTTP test connections used `127.0.0.1:59000` and `127.0.0.1:58123`.

Commands executed from the repository root unless noted:

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55432/new_api_test?sslmode=disable' make test
GOWORK=off go build ./...
go vet ./...
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
(cd web && bun run typecheck)
(cd web && bun run test src/features/setup/__tests__/database.test.tsx)
(cd web && bun run build)
(cd web && bunx --no-install oxlint -c .oxlintrc.json src/features/setup/components/database-step.tsx src/features/setup/components/complete-step.tsx src/features/setup/__tests__/database.test.tsx src/i18n/static-keys.ts)
(cd web && bun run i18n:sync)
node --check electron/main.js
```

All listed checks passed. `make test` includes both the root Go module and the independent RelayKit module. The setup page regression has four passing cases. PostgreSQL regression tests also cover missing/unsupported connection strings and shared/separate PostgreSQL log databases.

Full application startup verification used binaries built from the modified working tree, the original `eb99ab1b` checkout, and upstream release `v1.0.0-rc.33` (published 2026-09-05). The release source was obtained with:

```sh
gh api repos/QuantumNous/new-api/tarball/v1.0.0-rc.33 > /tmp/new-api-released.tar.gz
```

The isolated verification harness was run with:

```sh
python3 /tmp/verify-new-api-postgres.py
```

The harness created fresh databases, started each baseline binary with explicit `SQL_DSN` and separate `LOG_SQL_DSN`, inserted representative users, API tokens, subscription prices, and logs, then started the modified binary twice. It checked `/api/status` readiness and graceful shutdown, compared PostgreSQL column definitions, indexes and constraints before/after, checked Chinese text and 64-bit quota values, checked exact decimal prices, and confirmed duplicate token keys were still rejected.

| Scenario | Result |
| --- | --- |
| Fresh PostgreSQL main and separate log databases | Passed; both restarts preserved data and schema |
| PostgreSQL databases created by `eb99ab1b` | Passed; both restarts preserved data, indexes, constraints and uniqueness |
| PostgreSQL databases created by `v1.0.0-rc.33` | Passed; both restarts preserved data, indexes, constraints and uniqueness |
| Existing ClickHouse logs created by the original checkout | Passed; both restarts preserved `SHOW CREATE TABLE` and log contents |

Local verification output was written to `/tmp/new-api-database-verification/results.json` and `/tmp/new-api-database-verification.log`. These are temporary run artifacts; the repository's durable startup and migration regressions are in `model/database_test.go` and the existing migration tests.
