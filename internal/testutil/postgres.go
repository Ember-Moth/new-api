package testutil

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var ErrMissingPostgresDSN = errors.New("TEST_POSTGRES_DSN is not set")

func ConfigurePostgresTestGlobals() {
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
}

func OpenPostgresTestDB(t testing.TB, name string) *gorm.DB {
	t.Helper()

	db, cleanup, err := OpenPostgresTestDBFromEnv(name)
	if errors.Is(err, ErrMissingPostgresDSN) {
		t.Skip("set TEST_POSTGRES_DSN to run PostgreSQL database tests")
	}
	if err != nil {
		t.Fatalf("failed to open PostgreSQL test database: %v", err)
	}
	t.Cleanup(cleanup)
	return db
}

func OpenPostgresTestDBFromEnv(name string) (*gorm.DB, func(), error) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		return nil, nil, ErrMissingPostgresDSN
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil, nil, fmt.Errorf("TEST_POSTGRES_DSN must start with postgres:// or postgresql://")
	}

	ConfigurePostgresTestGlobals()

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{})
	if err != nil {
		return nil, nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Minute)

	schema := "test_" + sanitizeIdentifier(name)
	quotedSchema := quoteIdentifier(schema)
	if err := db.Exec("DROP SCHEMA IF EXISTS " + quotedSchema + " CASCADE").Error; err != nil {
		_ = sqlDB.Close()
		return nil, nil, err
	}
	if err := db.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		_ = sqlDB.Close()
		return nil, nil, err
	}
	if err := db.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
		_ = sqlDB.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = db.Exec("DROP SCHEMA IF EXISTS " + quotedSchema + " CASCADE").Error
		_ = sqlDB.Close()
	}
	return db, cleanup, nil
}

func TruncateTables(t testing.TB, db *gorm.DB, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	quoted := make([]string, 0, len(tables))
	for _, table := range tables {
		quoted = append(quoted, quoteIdentifier(table))
	}
	if err := db.Exec("TRUNCATE TABLE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatalf("failed to truncate test tables: %v", err)
	}
}

func sanitizeIdentifier(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		result = "db"
	}
	if len(result) > 48 {
		result = result[:48]
	}
	return result
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
