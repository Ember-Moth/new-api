package model

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	postgresLogPartitioningEnv         = "POSTGRES_LOG_PARTITIONING"
	postgresLogPartitionMonthsAheadEnv = "POSTGRES_LOG_PARTITION_MONTHS_AHEAD"
	postgresLogPartitionMonthsBackEnv  = "POSTGRES_LOG_PARTITION_MONTHS_BACK"
	defaultLogPartitionMonthsAhead     = 3
	defaultLogPartitionMonthsBack      = 1
)

type postgresLogPartitionMode string

const (
	postgresLogPartitionModeAuto     postgresLogPartitionMode = "auto"
	postgresLogPartitionModeEnabled  postgresLogPartitionMode = "enabled"
	postgresLogPartitionModeDisabled postgresLogPartitionMode = "disabled"
)

func preparePostgresLogTable(db *gorm.DB) (bool, error) {
	mode, err := getPostgresLogPartitionMode()
	if err != nil {
		return false, err
	}

	relkind, err := getCurrentSchemaTableRelkind(db, "logs")
	if err != nil {
		return false, err
	}

	if mode == postgresLogPartitionModeDisabled {
		if relkind == "p" {
			common.SysLog("PostgreSQL logs table is partitioned, skipping GORM AutoMigrate for logs")
			return false, nil
		}
		return true, nil
	}

	switch relkind {
	case "":
		common.SysLog("creating PostgreSQL partitioned logs table")
		if err := createPostgresPartitionedLogsTable(db); err != nil {
			return false, err
		}
		if err := ensurePostgresLogPartitions(db, time.Now().UTC()); err != nil {
			return false, err
		}
		return false, nil
	case "p":
		if err := ensurePostgresLogPartitions(db, time.Now().UTC()); err != nil {
			return false, err
		}
		return false, nil
	case "r":
		if mode == postgresLogPartitionModeEnabled {
			return false, fmt.Errorf("%s=true requires logs to be a PostgreSQL partitioned table; run docs/installation/postgresql-log-partitioning.sql first or set %s=auto", postgresLogPartitioningEnv, postgresLogPartitioningEnv)
		}
		common.SysLog("PostgreSQL logs table is not partitioned; keeping existing heap table. Run docs/installation/postgresql-log-partitioning.sql to migrate")
		return true, nil
	default:
		return false, fmt.Errorf("unsupported logs table relkind %q", relkind)
	}
}

func getPostgresLogPartitionMode() (postgresLogPartitionMode, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(postgresLogPartitioningEnv)))
	if value == "" || value == "auto" {
		return postgresLogPartitionModeAuto, nil
	}
	if parsed, err := strconv.ParseBool(value); err == nil {
		if parsed {
			return postgresLogPartitionModeEnabled, nil
		}
		return postgresLogPartitionModeDisabled, nil
	}
	return "", fmt.Errorf("%s must be one of auto, true, or false", postgresLogPartitioningEnv)
}

func getCurrentSchemaTableRelkind(db *gorm.DB, tableName string) (string, error) {
	var relkind string
	err := db.Raw(`
SELECT COALESCE((
	SELECT c.relkind::text
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = current_schema()
	  AND c.relname = ?
	  AND c.relkind IN ('r', 'p')
	LIMIT 1
), '')`, tableName).Scan(&relkind).Error
	return relkind, err
}

func isPostgresLogsPartitioned(db *gorm.DB) (bool, error) {
	relkind, err := getCurrentSchemaTableRelkind(db, "logs")
	if err != nil {
		return false, err
	}
	return relkind == "p", nil
}

func createPostgresPartitionedLogsTable(db *gorm.DB) error {
	if err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS logs_id_seq AS bigint`).Error; err != nil {
		return fmt.Errorf("failed to create logs_id_seq: %w", err)
	}
	if err := db.Exec(`
CREATE TABLE IF NOT EXISTS logs (
	id bigint NOT NULL DEFAULT nextval('logs_id_seq'::regclass),
	user_id bigint,
	created_at bigint NOT NULL,
	type bigint,
	content text,
	username text DEFAULT '',
	token_name text DEFAULT '',
	model_name text DEFAULT '',
	quota bigint DEFAULT 0,
	prompt_tokens bigint DEFAULT 0,
	completion_tokens bigint DEFAULT 0,
	use_time bigint DEFAULT 0,
	is_stream boolean,
	channel_id bigint,
	channel_name text,
	token_id bigint DEFAULT 0,
	"group" text,
	ip text DEFAULT '',
	request_id varchar(64) DEFAULT '',
	upstream_request_id varchar(128) DEFAULT '',
	other text
) PARTITION BY RANGE (created_at)`).Error; err != nil {
		return fmt.Errorf("failed to create partitioned logs table: %w", err)
	}
	if err := db.Exec(`ALTER SEQUENCE logs_id_seq OWNED BY logs.id`).Error; err != nil {
		return fmt.Errorf("failed to bind logs_id_seq ownership: %w", err)
	}
	return nil
}

func ensurePostgresLogPartitions(db *gorm.DB, now time.Time) error {
	monthsAhead := common.GetEnvOrDefault(postgresLogPartitionMonthsAheadEnv, defaultLogPartitionMonthsAhead)
	monthsBack := common.GetEnvOrDefault(postgresLogPartitionMonthsBackEnv, defaultLogPartitionMonthsBack)
	return ensurePostgresLogPartitionRange(db, now, monthsBack, monthsAhead)
}

func ensurePostgresLogPartitionRange(db *gorm.DB, now time.Time, monthsBack int, monthsAhead int) error {
	if monthsBack < 0 {
		monthsBack = 0
	}
	if monthsAhead < 0 {
		monthsAhead = 0
	}
	if monthsBack > 120 {
		monthsBack = 120
	}
	if monthsAhead > 120 {
		monthsAhead = 120
	}

	current := monthStartUTC(now)
	for offset := -monthsBack; offset <= monthsAhead; offset++ {
		start := current.AddDate(0, offset, 0)
		if err := createPostgresLogPartition(db, start); err != nil {
			return err
		}
	}
	return nil
}

func createPostgresLogPartition(db *gorm.DB, start time.Time) error {
	start = monthStartUTC(start)
	end := start.AddDate(0, 1, 0)
	partitionName := postgresLogPartitionName(start)
	statement := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF logs FOR VALUES FROM (%d) TO (%d)`,
		quotePostgresIdentifier(partitionName),
		start.Unix(),
		end.Unix(),
	)
	if err := db.Exec(statement).Error; err != nil {
		return fmt.Errorf("failed to create logs partition %s: %w", partitionName, err)
	}
	return nil
}

func monthStartUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func postgresLogPartitionName(start time.Time) string {
	start = monthStartUTC(start)
	return fmt.Sprintf("logs_y%04dm%02d", start.Year(), int(start.Month()))
}

func parsePostgresLogPartitionName(name string) (time.Time, bool) {
	if len(name) != len("logs_y2006m01") || !strings.HasPrefix(name, "logs_y") || name[10] != 'm' {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(name[6:10])
	if err != nil {
		return time.Time{}, false
	}
	month, err := strconv.Atoi(name[11:13])
	if err != nil {
		return time.Time{}, false
	}
	if year < 1970 || month < 1 || month > 12 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), true
}

func dropOldPostgresLogPartitions(ctx context.Context, db *gorm.DB, targetTimestamp int64) (int64, error) {
	partitioned, err := isPostgresLogsPartitioned(db)
	if err != nil || !partitioned {
		return 0, err
	}

	var partitionNames []string
	if err := db.WithContext(ctx).Raw(`
SELECT child.relname
FROM pg_inherits
JOIN pg_class parent ON parent.oid = pg_inherits.inhparent
JOIN pg_namespace parent_ns ON parent_ns.oid = parent.relnamespace
JOIN pg_class child ON child.oid = pg_inherits.inhrelid
JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
WHERE parent_ns.nspname = current_schema()
  AND child_ns.nspname = current_schema()
  AND parent.relname = 'logs'
ORDER BY child.relname`).Scan(&partitionNames).Error; err != nil {
		return 0, err
	}

	var total int64
	for _, partitionName := range partitionNames {
		start, ok := parsePostgresLogPartitionName(partitionName)
		if !ok {
			continue
		}
		if start.AddDate(0, 1, 0).Unix() > targetTimestamp {
			continue
		}

		quotedName := quotePostgresIdentifier(partitionName)
		var count int64
		if err := db.WithContext(ctx).Table(quotedName).Count(&count).Error; err != nil {
			return total, err
		}
		if err := db.WithContext(ctx).Exec("DROP TABLE " + quotedName).Error; err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
