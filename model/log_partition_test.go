package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparePostgresLogTableCreatesPartitionedLogsForNewSchema(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_new")
	t.Setenv(postgresLogPartitioningEnv, "")

	autoMigrateLog, err := preparePostgresLogTable(db)
	require.NoError(t, err)
	assert.False(t, autoMigrateLog)

	require.NoError(t, runPostgresLogMigrations(db))

	partitioned, err := isPostgresLogsPartitioned(db)
	require.NoError(t, err)
	assert.True(t, partitioned)

	log := &Log{
		UserId:    1001,
		CreatedAt: time.Now().UTC().Unix(),
		Type:      LogTypeSystem,
		Content:   "partition insert test",
	}
	require.NoError(t, db.Create(log).Error)
	assert.NotZero(t, log.Id)
}

func TestEmbeddedLogMigrationConvertsExistingHeapTableInAutoMode(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_existing_heap")
	t.Setenv(postgresLogPartitioningEnv, "auto")
	require.NoError(t, db.AutoMigrate(&Log{}))

	autoMigrateLog, err := preparePostgresLogTable(db)
	require.NoError(t, err)
	assert.True(t, autoMigrateLog)
	require.NoError(t, db.AutoMigrate(&Log{}))
	require.NoError(t, runPostgresLogMigrations(db))

	partitioned, err := isPostgresLogsPartitioned(db)
	require.NoError(t, err)
	assert.True(t, partitioned)
}

func TestEmbeddedLogMigrationConvertsExistingHeapTableWhenForced(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_required")
	t.Setenv(postgresLogPartitioningEnv, "true")
	require.NoError(t, db.AutoMigrate(&Log{}))

	autoMigrateLog, err := preparePostgresLogTable(db)
	require.NoError(t, err)
	assert.True(t, autoMigrateLog)
	require.NoError(t, db.AutoMigrate(&Log{}))
	require.NoError(t, runPostgresLogMigrations(db))

	partitioned, err := isPostgresLogsPartitioned(db)
	require.NoError(t, err)
	assert.True(t, partitioned)
}

func TestDeleteOldLogDropsWholePostgresPartitions(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_cleanup")
	t.Setenv(postgresLogPartitioningEnv, "true")
	t.Setenv(postgresLogPartitionMonthsBackEnv, "1")
	t.Setenv(postgresLogPartitionMonthsAheadEnv, "1")
	require.NoError(t, runPostgresLogMigrations(db))

	oldLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = oldLogDB
	})

	baseMonth := monthStartUTC(time.Now().UTC())
	oldMonth := baseMonth.AddDate(0, -1, 0)
	nextMonth := baseMonth.AddDate(0, 1, 0)

	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: oldMonth.Add(14 * 24 * time.Hour).Unix(), Type: LogTypeSystem}).Error)
	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: baseMonth.Add(14 * 24 * time.Hour).Unix(), Type: LogTypeSystem}).Error)
	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: nextMonth.Add(14 * 24 * time.Hour).Unix(), Type: LogTypeSystem}).Error)

	deleted, err := DeleteOldLog(context.Background(), baseMonth.Unix(), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	oldPartitionName := postgresLogPartitionName(oldMonth)
	var januaryPartitionExists bool
	require.NoError(t, db.Raw(`SELECT to_regclass(?) IS NOT NULL`, oldPartitionName).Scan(&januaryPartitionExists).Error)
	assert.False(t, januaryPartitionExists)

	var remaining int64
	require.NoError(t, db.Model(&Log{}).Count(&remaining).Error)
	assert.EqualValues(t, 2, remaining)
}
