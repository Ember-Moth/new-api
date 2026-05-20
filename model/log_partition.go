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
		common.SysLog("PostgreSQL logs table does not exist; embedded migration will create a monthly partitioned logs table")
		return false, nil
	case "p":
		return false, nil
	case "r":
		common.SysLog("PostgreSQL logs table is a heap table; embedded migration will convert it to monthly partitions after AutoMigrate")
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

func maintainPostgresLogPartitions(db *gorm.DB) error {
	monthsAhead := common.GetEnvOrDefault(postgresLogPartitionMonthsAheadEnv, defaultLogPartitionMonthsAhead)
	monthsBack := common.GetEnvOrDefault(postgresLogPartitionMonthsBackEnv, defaultLogPartitionMonthsBack)
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

	return db.Exec("SELECT ensure_log_partitions(?, ?)", monthsBack, monthsAhead).Error
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
