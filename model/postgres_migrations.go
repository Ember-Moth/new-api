package model

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed migrations/*.sql
var postgresMigrationFS embed.FS

type embeddedPostgresMigration struct {
	ID            string
	File          string
	Transactional bool
}

const postgresMigrationTable = "schema_migrations"

var postgresSchemaMigrationBootstrap = embeddedPostgresMigration{
	ID:            "000_schema_migrations",
	File:          "000_schema_migrations.sql",
	Transactional: true,
}

var postgresMainMigrations = []embeddedPostgresMigration{
	{ID: "001_quota_rollups", File: "001_quota_rollups.sql", Transactional: true},
	{ID: "002_quota_rollups_backfill", File: "002_quota_rollups_backfill.sql", Transactional: true},
	{ID: "010_tokens_model_limits_text", File: "010_tokens_model_limits_text.sql", Transactional: true},
	{ID: "011_subscription_plans_price_amount_decimal", File: "011_subscription_plans_price_amount_decimal.sql", Transactional: true},
}

var postgresPerformanceMigrations = []embeddedPostgresMigration{
	{ID: "003_main_performance_indexes", File: "003_main_performance_indexes.sql", Transactional: false},
}

var postgresLogPartitionMigrations = []embeddedPostgresMigration{
	{ID: "101_log_partitioning", File: "101_log_partitioning.sql", Transactional: true},
}

var postgresLogPerformanceMigrations = []embeddedPostgresMigration{
	{ID: "102_log_performance_indexes", File: "102_log_performance_indexes.sql", Transactional: true},
}

var postgresLogMaintenanceMigrations = []embeddedPostgresMigration{
	{ID: "103_log_partition_maintenance", File: "103_log_partition_maintenance.sql", Transactional: true},
}

func runPostgresMainMigrations(db *gorm.DB) error {
	migrations := append([]embeddedPostgresMigration{}, postgresMainMigrations...)
	if common.GetEnvOrDefaultBool(postgresAutoCreatePerformanceIndexesEnv, true) {
		migrations = append(migrations, postgresPerformanceMigrations...)
	} else {
		common.SysLog("skipping optional PostgreSQL performance index migrations by configuration")
	}
	return runEmbeddedPostgresMigrations(db, migrations)
}

func runPostgresLogMigrations(db *gorm.DB) error {
	migrations := make([]embeddedPostgresMigration, 0, len(postgresLogPartitionMigrations)+len(postgresLogPerformanceMigrations)+len(postgresLogMaintenanceMigrations))
	mode, err := getPostgresLogPartitionMode()
	if err != nil {
		return err
	}
	if mode != postgresLogPartitionModeDisabled {
		migrations = append(migrations, postgresLogPartitionMigrations...)
	}
	if common.GetEnvOrDefaultBool(postgresAutoCreatePerformanceIndexesEnv, true) {
		migrations = append(migrations, postgresLogPerformanceMigrations...)
	} else {
		common.SysLog("skipping optional PostgreSQL log performance index migrations by configuration")
	}
	if mode != postgresLogPartitionModeDisabled {
		migrations = append(migrations, postgresLogMaintenanceMigrations...)
	}
	if err := runEmbeddedPostgresMigrations(db, migrations); err != nil {
		return err
	}
	if mode != postgresLogPartitionModeDisabled {
		return maintainPostgresLogPartitions(db)
	}
	return nil
}

func runEmbeddedPostgresMigrations(db *gorm.DB, migrations []embeddedPostgresMigration) error {
	if len(migrations) == 0 {
		return nil
	}
	if err := ensurePostgresMigrationTable(db); err != nil {
		return err
	}
	migrations = prependPostgresBootstrapMigration(migrations)
	for _, migration := range migrations {
		if err := runEmbeddedPostgresMigration(db, migration); err != nil {
			return err
		}
	}
	return nil
}

func ensurePostgresMigrationTable(db *gorm.DB) error {
	if db.Migrator().HasTable(postgresMigrationTable) {
		return nil
	}
	statements, err := readPostgresMigrationStatements(postgresSchemaMigrationBootstrap)
	if err != nil {
		return err
	}
	return execPostgresMigrationStatements(db, postgresSchemaMigrationBootstrap.ID, statements)
}

func prependPostgresBootstrapMigration(migrations []embeddedPostgresMigration) []embeddedPostgresMigration {
	for _, migration := range migrations {
		if migration.ID == postgresSchemaMigrationBootstrap.ID {
			return migrations
		}
	}
	result := make([]embeddedPostgresMigration, 0, len(migrations)+1)
	result = append(result, postgresSchemaMigrationBootstrap)
	result = append(result, migrations...)
	return result
}

func runEmbeddedPostgresMigration(db *gorm.DB, migration embeddedPostgresMigration) error {
	sqlBytes, statements, err := readPostgresMigration(migration)
	if err != nil {
		return err
	}
	checksum := postgresMigrationChecksum(sqlBytes)

	appliedChecksum, err := getAppliedPostgresMigrationChecksum(db, migration.ID)
	if err != nil {
		return err
	}
	if appliedChecksum != "" {
		if appliedChecksum != checksum {
			return fmt.Errorf("embedded PostgreSQL migration %s checksum changed; refusing to continue", migration.ID)
		}
		return nil
	}

	if len(statements) == 0 {
		return recordPostgresMigration(db, migration.ID, checksum, 0)
	}

	common.SysLog(fmt.Sprintf("running embedded PostgreSQL migration %s", migration.ID))
	start := time.Now()
	if migration.Transactional {
		err = db.Transaction(func(tx *gorm.DB) error {
			return execPostgresMigrationStatements(tx, migration.ID, statements)
		})
	} else {
		err = execPostgresMigrationStatements(db, migration.ID, statements)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Milliseconds()
	if err := recordPostgresMigration(db, migration.ID, checksum, elapsed); err != nil {
		return err
	}
	common.SysLog(fmt.Sprintf("completed embedded PostgreSQL migration %s in %dms", migration.ID, elapsed))
	return nil
}

func readPostgresMigrationStatements(migration embeddedPostgresMigration) ([]string, error) {
	_, statements, err := readPostgresMigration(migration)
	return statements, err
}

func readPostgresMigration(migration embeddedPostgresMigration) ([]byte, []string, error) {
	sqlBytes, err := postgresMigrationFS.ReadFile("migrations/" + migration.File)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read embedded PostgreSQL migration %s: %w", migration.ID, err)
	}
	statements, err := splitPostgresSQLStatements(string(sqlBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse embedded PostgreSQL migration %s: %w", migration.ID, err)
	}
	return sqlBytes, statements, nil
}

func postgresMigrationChecksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type postgresSchemaMigrationRecord struct {
	ID          string `gorm:"column:id;primaryKey;size:255"`
	Checksum    string `gorm:"column:checksum;size:64;not null"`
	AppliedAt   int64  `gorm:"column:applied_at;not null"`
	ExecutionMs int64  `gorm:"column:execution_ms;not null"`
}

func (postgresSchemaMigrationRecord) TableName() string {
	return postgresMigrationTable
}

func getAppliedPostgresMigrationChecksum(db *gorm.DB, id string) (string, error) {
	var record postgresSchemaMigrationRecord
	if err := db.Select("checksum").Where("id = ?", id).Take(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return record.Checksum, nil
}

func recordPostgresMigration(db *gorm.DB, id string, checksum string, executionMs int64) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"checksum", "applied_at", "execution_ms"}),
	}).Create(&postgresSchemaMigrationRecord{
		ID:          id,
		Checksum:    checksum,
		AppliedAt:   common.GetTimestamp(),
		ExecutionMs: executionMs,
	}).Error
}

func execPostgresMigrationStatements(db *gorm.DB, migrationID string, statements []string) error {
	for idx, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("failed to execute PostgreSQL migration %s statement %d: %w", migrationID, idx+1, err)
		}
	}
	return nil
}

func splitPostgresSQLStatements(sqlText string) ([]string, error) {
	var statements []string
	var current strings.Builder
	var dollarTag string
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(sqlText); i++ {
		ch := sqlText[i]
		next := byte(0)
		if i+1 < len(sqlText) {
			next = sqlText[i+1]
		}

		if inLineComment {
			current.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			current.WriteByte(ch)
			if ch == '*' && next == '/' {
				current.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(sqlText[i:], dollarTag) {
				current.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			current.WriteByte(ch)
			continue
		}
		if inSingleQuote {
			current.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					current.WriteByte(next)
					i++
					continue
				}
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			current.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					current.WriteByte(next)
					i++
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		if ch == '-' && next == '-' {
			current.WriteByte(ch)
			current.WriteByte(next)
			i++
			inLineComment = true
			continue
		}
		if ch == '/' && next == '*' {
			current.WriteByte(ch)
			current.WriteByte(next)
			i++
			inBlockComment = true
			continue
		}
		if ch == '\'' {
			current.WriteByte(ch)
			inSingleQuote = true
			continue
		}
		if ch == '"' {
			current.WriteByte(ch)
			inDoubleQuote = true
			continue
		}
		if ch == '$' {
			tag, ok := readPostgresDollarTag(sqlText[i:])
			if ok {
				current.WriteString(tag)
				i += len(tag) - 1
				dollarTag = tag
				continue
			}
		}
		if ch == ';' {
			statement := strings.TrimSpace(current.String())
			if statement != "" {
				statements = append(statements, statement)
			}
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}

	if dollarTag != "" {
		return nil, fmt.Errorf("unterminated dollar quote %s", dollarTag)
	}
	if inSingleQuote {
		return nil, fmt.Errorf("unterminated single quote")
	}
	if inDoubleQuote {
		return nil, fmt.Errorf("unterminated double quote")
	}
	if inBlockComment {
		return nil, fmt.Errorf("unterminated block comment")
	}
	statement := strings.TrimSpace(current.String())
	if statement != "" {
		statements = append(statements, statement)
	}
	return statements, nil
}

func readPostgresDollarTag(value string) (string, bool) {
	if len(value) < 2 || value[0] != '$' {
		return "", false
	}
	for i := 1; i < len(value); i++ {
		if value[i] != '$' {
			if !isPostgresDollarTagChar(value[i]) {
				return "", false
			}
			continue
		}
		return value[:i+1], true
	}
	return "", false
}

func isPostgresDollarTagChar(ch byte) bool {
	return ch == '_' ||
		(ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9')
}

func hasSeparateLogDB() bool {
	return strings.TrimSpace(os.Getenv("LOG_SQL_DSN")) != ""
}
