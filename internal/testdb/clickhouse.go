package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/google/uuid"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

// NewLogDatabase isolates ClickHouse fixtures in a disposable database. The
// primary PostgreSQL pool only coordinates migrations; logs never share it.
func NewLogDatabase(primary *gorm.DB) (*gorm.DB, string, func() error, error) {
	dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		return nil, "", nil, errors.New("TEST_CLICKHOUSE_DSN is required for log database tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return nil, "", nil, err
	}
	admin, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, "", nil, err
	}
	name := "test_logs_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec("CREATE DATABASE " + name); err != nil {
		_ = admin.Close()
		return nil, "", nil, err
	}
	cleanup := func() error {
		_, dropErr := admin.Exec("DROP DATABASE " + name)
		return errors.Join(dropErr, admin.Close())
	}
	parsed.Path = "/" + name
	coordinator, err := primary.DB()
	if err == nil {
		err = schema.UpClickHouse(parsed.String(), coordinator)
	}
	if err != nil {
		_ = cleanup()
		return nil, "", nil, err
	}
	logs, err := gorm.Open(clickhouse.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		_ = cleanup()
		return nil, "", nil, err
	}
	closeAll := func() error {
		pool, poolErr := logs.DB()
		if poolErr == nil {
			poolErr = pool.Close()
		}
		return errors.Join(poolErr, cleanup())
	}
	return logs, parsed.String(), closeAll, nil
}
func Logs(t testing.TB, primary *gorm.DB) *gorm.DB {
	t.Helper()
	db, _, cleanup, err := NewLogDatabase(primary)
	if err != nil {
		t.Fatalf("create ClickHouse log fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("clean ClickHouse log fixture: %v", err)
		}
	})
	return db
}
func ClearLogs(t testing.TB, db *gorm.DB) {
	t.Helper()
	if err := db.WithContext(context.Background()).Exec("TRUNCATE TABLE logs").Error; err != nil {
		t.Fatal(fmt.Errorf("truncate ClickHouse fixture: %w", err))
	}
}
