// Package schema owns versioned SQL migrations. GORM models do not create or
// alter the application's production schema.
package schema

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/clickhouse"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed database/postgres/*.sql database/postgres_log/*.sql database/clickhouse_log/*.sql
var sqlFiles embed.FS

type Scope string

const (
	Main Scope = "main"
	Logs Scope = "logs"
)

type postgresMigrationDriver struct {
	*postgres.Postgres
	conn *sql.Conn
}

func (driver *postgresMigrationDriver) Run(sqlFile io.Reader) error {
	err := driver.Postgres.Run(sqlFile)
	if err == nil {
		return nil
	}
	// A file with BEGIN/COMMIT can fail before COMMIT. End that transaction so
	// migrate can unlock the session and return a clean connection to the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, rollbackErr := driver.conn.ExecContext(ctx, "ROLLBACK")
	return errors.Join(err, rollbackErr)
}

// NewPostgres leases one connection from the application pool. Callers must
// Close the returned migrator; closing it releases that connection, not the pool.
func NewPostgres(db *sql.DB, scope Scope) (*migrate.Migrate, error) {
	path, table := "database/postgres", "schema_migrations"
	switch scope {
	case Main:
	case Logs:
		path, table = "database/postgres_log", "schema_migrations_logs"
	default:
		return nil, fmt.Errorf("unknown PostgreSQL migration scope %q", scope)
	}
	source, err := iofs.New(sqlFiles, path)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	driver, err := postgres.WithConnection(context.Background(), conn, &postgres.Config{MigrationsTable: table})
	if err != nil {
		_ = conn.Close()
		_ = source.Close()
		return nil, err
	}
	client, err := migrate.NewWithInstance("iofs", source, "postgres", &postgresMigrationDriver{Postgres: driver, conn: conn})
	if err != nil {
		_ = driver.Close()
		_ = source.Close()
		return nil, err
	}
	return client, nil
}

func UpPostgres(db *sql.DB, scope Scope) error {
	client, err := NewPostgres(db, scope)
	if err != nil {
		return err
	}
	return applyAndClose(client)
}

// UpClickHouse serializes startup migrations across masters using the primary
// PostgreSQL database. The ClickHouse migrate driver only locks within a process.
func UpClickHouse(dsn string, coordinator *sql.DB) (err error) {
	lock, err := coordinator.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := lock.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if _, err := lock.Exec("SET LOCAL lock_timeout = '15s'"); err != nil {
		return err
	}
	if _, err := lock.Exec("SELECT pg_advisory_xact_lock(hashtext('new-api:clickhouse-log-migrations'))"); err != nil {
		return err
	}

	source, err := iofs.New(sqlFiles, "database/clickhouse_log")
	if err != nil {
		return err
	}
	// An independent pool lets migrate close its driver without closing LOG_DB.
	connection, err := sql.Open("clickhouse", dsn)
	if err != nil {
		_ = source.Close()
		return err
	}
	connection.SetMaxOpenConns(1)
	driver, err := clickhouse.WithInstance(connection, &clickhouse.Config{
		MigrationsTable: "schema_migrations_logs",
	})
	if err != nil {
		_ = connection.Close()
		_ = source.Close()
		return err
	}
	client, err := migrate.NewWithInstance("iofs", source, "clickhouse", driver)
	if err != nil {
		_ = driver.Close()
		_ = source.Close()
		return err
	}
	return applyAndClose(client)
}

func applyAndClose(client *migrate.Migrate) error {
	err := client.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		err = nil
	}
	sourceErr, databaseErr := client.Close()
	return errors.Join(err, sourceErr, databaseErr)
}
