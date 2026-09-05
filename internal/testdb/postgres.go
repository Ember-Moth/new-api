// Package testdb isolates database tests in disposable PostgreSQL schemas.
package testdb

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// NewSchema creates a schema on TEST_POSTGRES_DSN and returns its connection
// URL and cleanup. It never drops or modifies objects outside that schema.
func NewSchema() (string, func() error, error) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		return "", nil, fmt.Errorf("TEST_POSTGRES_DSN is required for database tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", nil, fmt.Errorf("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgres.New(postgres.Config{
		DSN: dsn, PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return "", nil, err
	}
	connection, err := admin.DB()
	if err != nil {
		return "", nil, err
	}
	connection.SetMaxOpenConns(1)
	var serverVersion int
	if err := admin.Raw("SELECT current_setting('server_version_num')::integer").Scan(&serverVersion).Error; err != nil {
		_ = connection.Close()
		return "", nil, err
	}
	if serverVersion < 180000 {
		_ = connection.Close()
		return "", nil, fmt.Errorf("TEST_POSTGRES_DSN requires PostgreSQL 18 or newer")
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA ?", clause.Table{Name: schema}).Error; err != nil {
		_ = connection.Close()
		return "", nil, err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cleanup := func() error {
		defer connection.Close()
		return admin.Exec("DROP SCHEMA ? CASCADE", clause.Table{Name: schema}).Error
	}
	return parsed.String(), cleanup, nil
}

// DSN returns a connection URL whose schema is removed when the test ends.
func DSN(t testing.TB) string {
	t.Helper()
	dsn, cleanup, err := NewSchema()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	return dsn
}

// Open preserves the caller's GORM options and closes its connections before
// dropping the test schema. Prepared statements remain disabled like production.
func Open(t testing.TB, config *gorm.Config) (*gorm.DB, error) {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: DSN(t), PreferSimpleProtocol: true,
	}), config)
	if err != nil {
		return nil, err
	}
	connection, err := db.DB()
	if err != nil {
		return nil, err
	}
	connection.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return db, nil
}
