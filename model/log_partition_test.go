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

	partitioned, err := isPostgresLogsPartitioned(db)
	require.NoError(t, err)
	assert.True(t, partitioned)
	require.NoError(t, ensurePostgresLogPerformanceIndexes(db))

	log := &Log{
		UserId:    1001,
		CreatedAt: time.Now().UTC().Unix(),
		Type:      LogTypeSystem,
		Content:   "partition insert test",
	}
	require.NoError(t, db.Create(log).Error)
	assert.NotZero(t, log.Id)
}

func TestPreparePostgresLogTableKeepsExistingHeapTableInAutoMode(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_existing_heap")
	t.Setenv(postgresLogPartitioningEnv, "auto")
	require.NoError(t, db.AutoMigrate(&Log{}))

	autoMigrateLog, err := preparePostgresLogTable(db)
	require.NoError(t, err)
	assert.True(t, autoMigrateLog)

	partitioned, err := isPostgresLogsPartitioned(db)
	require.NoError(t, err)
	assert.False(t, partitioned)
}

func TestPreparePostgresLogTableRequiresMigrationWhenForced(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_required")
	t.Setenv(postgresLogPartitioningEnv, "true")
	require.NoError(t, db.AutoMigrate(&Log{}))

	autoMigrateLog, err := preparePostgresLogTable(db)
	require.Error(t, err)
	assert.False(t, autoMigrateLog)
}

func TestDeleteOldLogDropsWholePostgresPartitions(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "log_partition_cleanup")
	require.NoError(t, createPostgresPartitionedLogsTable(db))
	require.NoError(t, ensurePostgresLogPartitionRange(db, time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC), 1, 1))

	oldLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = oldLogDB
	})

	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).Unix(), Type: LogTypeSystem}).Error)
	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC).Unix(), Type: LogTypeSystem}).Error)
	require.NoError(t, db.Create(&Log{UserId: 1001, CreatedAt: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix(), Type: LogTypeSystem}).Error)

	deleted, err := DeleteOldLog(context.Background(), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix(), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	var januaryPartitionExists bool
	require.NoError(t, db.Raw(`SELECT to_regclass('logs_y2026m01') IS NOT NULL`).Scan(&januaryPartitionExists).Error)
	assert.False(t, januaryPartitionExists)

	var remaining int64
	require.NoError(t, db.Model(&Log{}).Count(&remaining).Error)
	assert.EqualValues(t, 2, remaining)
}
