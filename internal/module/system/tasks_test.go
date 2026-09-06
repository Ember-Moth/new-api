package system_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

func TestLogCleanupManagementCompletesOnSupportedLogDatabases(t *testing.T) {
	for _, kind := range []common.DatabaseType{common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse} {
		t.Run(string(kind), func(t *testing.T) {
			database, err := testdb.Open(t, &gorm.Config{})
			require.NoError(t, err)
			pool, err := database.DB()
			require.NoError(t, err)
			require.NoError(t, schema.UpPostgres(pool, schema.Main))
			require.NoError(t, schema.UpPostgres(pool, schema.Main))
			logDB := database
			if kind == common.DatabaseTypePostgreSQL {
				require.NoError(t, schema.UpPostgres(pool, schema.Logs))
				require.NoError(t, schema.UpPostgres(pool, schema.Logs))
			} else {
				dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
				if dsn == "" {
					t.Skip("TEST_CLICKHOUSE_DSN is required")
				}
				admin, err := sql.Open("clickhouse", dsn)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, admin.Close()) })
				name := "task_cleanup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				_, err = admin.Exec("CREATE DATABASE " + name)
				require.NoError(t, err)
				t.Cleanup(func() { _, err := admin.Exec("DROP DATABASE " + name); require.NoError(t, err) })
				parsed, err := url.Parse(dsn)
				require.NoError(t, err)
				parsed.Path = "/" + name
				require.NoError(t, schema.UpClickHouse(parsed.String(), pool))
				require.NoError(t, schema.UpClickHouse(parsed.String(), pool))
				logDB, err = gorm.Open(clickhouse.Open(parsed.String()), &gorm.Config{})
				require.NoError(t, err)
				logPool, err := logDB.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, logPool.Close()) })
			}
			previousLog, previousType := model.LOG_DB, common.LogDatabaseType()
			model.LOG_DB = logDB
			common.SetLogDatabaseType(kind)
			t.Cleanup(func() { model.LOG_DB = previousLog; common.SetLogDatabaseType(previousType) })
			now := common.GetTimestamp()
			for _, record := range []model.Log{
				{CreatedAt: now - 100, Content: "old-one", Type: model.LogTypeConsume},
				{CreatedAt: now - 100, Content: "old-two", Type: model.LogTypeConsume},
				{CreatedAt: now, Content: "retained", Type: model.LogTypeConsume},
			} {
				require.NoError(t, logDB.Create(&record).Error)
			}
			tasks := system.New(system.Dependencies{DB: database, NodeName: "cleanup-test", Master: true, Logs: system.LogOperations{Count: model.CountOldLog, DeleteBatch: model.DeleteOldLogBatch}})
			handler := systemhttp.New(tasks)
			router := gin.New()
			router.POST("/tasks/cleanup", handler.CreateLogCleanupSystemTask)
			router.GET("/tasks/current", handler.GetCurrentSystemTask)
			router.GET("/tasks/list", handler.ListSystemTasks)
			router.GET("/tasks/:task_id", handler.GetSystemTask)
			creation := httptest.NewRecorder()
			router.ServeHTTP(creation, httptest.NewRequest(http.MethodPost, "/tasks/cleanup?target_timestamp="+strconv.FormatInt(now-50, 10), nil))
			require.Equal(t, http.StatusOK, creation.Code)
			var created struct {
				Success bool                      `json:"success"`
				Data    system.SystemTaskResponse `json:"data"`
			}
			require.NoError(t, common.Unmarshal(creation.Body.Bytes(), &created))
			require.True(t, created.Success, creation.Body.String())
			duplicate, err := tasks.StartLogCleanupTask(t.Context(), now-50)
			require.NoError(t, err)
			assert.Equal(t, created.Data.TaskID, duplicate.TaskID)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tasks.StartSystemTaskRunner(ctx)
			var completed *system.SystemTask
			require.Eventually(t, func() bool {
				completed, err = tasks.GetSystemTaskByTaskID(t.Context(), created.Data.TaskID)
				if err != nil || completed == nil || completed.Status != system.SystemTaskStatusSucceeded {
					return false
				}
				var locks int64
				return database.Model(&system.SystemTaskLock{}).Where("task_id = ?", completed.TaskID).Count(&locks).Error == nil && locks == 0
			}, 10*time.Second, 20*time.Millisecond)
			var state system.LogCleanupState
			require.NoError(t, completed.DecodeState(&state))
			assert.Equal(t, int64(2), state.Processed)
			assert.Equal(t, int64(0), state.Remaining)
			assert.Equal(t, 100, state.Progress)
			var result system.LogCleanupResult
			require.NoError(t, common.UnmarshalJsonStr(completed.Result, &result))
			assert.Equal(t, int64(2), result.DeletedCount)
			remaining, err := model.CountOldLog(t.Context(), now-50)
			require.NoError(t, err)
			assert.Zero(t, remaining)
			var retained int64
			require.NoError(t, logDB.Model(&model.Log{}).Where("content = ?", "retained").Count(&retained).Error)
			assert.Equal(t, int64(1), retained)
			detail := httptest.NewRecorder()
			router.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/tasks/"+completed.TaskID, nil))
			assert.Equal(t, http.StatusOK, detail.Code)
			assert.Contains(t, detail.Body.String(), `"status":"succeeded"`)
			active, err := tasks.GetActiveSystemTask(t.Context(), system.SystemTaskTypeLogCleanup)
			require.NoError(t, err)
			assert.Nil(t, active)
		})
	}
}

func TestInstanceReportingUpdatesOneNodeAndGuardsStaleDeletion(t *testing.T) {
	database, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	cpuUsage := 12.5
	config := system.InstanceReportConfig{Node: common.NodeIdentity{Name: "node-a"}, Version: "test-version", StartedAt: 1000, Resources: func() contract.SystemInstanceResources {
		return contract.SystemInstanceResources{CPU: contract.SystemInstanceResourceUsage{UsagePercent: cpuUsage}, Storage: contract.SystemInstanceStorageMetrics{TotalBytes: 100, UsedBytes: 20, FreeBytes: 80, UsedPercent: 20}}
	}}
	service := system.New(system.Dependencies{DB: database, Master: true, InstanceReport: config})
	require.NoError(t, service.ReportCurrentSystemInstance(t.Context()))
	var first entity.SystemInstance
	require.NoError(t, database.First(&first, "node_name = ?", "node-a").Error)
	cpuUsage = 25
	require.NoError(t, service.ReportCurrentSystemInstance(t.Context()))
	var count int64
	require.NoError(t, database.Model(&entity.SystemInstance{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var refreshed entity.SystemInstance
	require.NoError(t, database.First(&refreshed, "node_name = ?", "node-a").Error)
	assert.Equal(t, first.CreatedAt, refreshed.CreatedAt)
	var info contract.SystemInstanceInfo
	require.NoError(t, common.UnmarshalJsonStr(refreshed.Info, &info))
	assert.Equal(t, "node-a", info.Node.Name)
	assert.Equal(t, "test-version", info.Runtime.Version)
	assert.Equal(t, int64(1000), info.Runtime.StartedAt)
	assert.True(t, info.Role.IsMaster)
	assert.Equal(t, 25.0, info.Resources.CPU.UsagePercent)
	assert.Equal(t, uint64(100), info.Resources.Storage.TotalBytes)
	now := common.GetTimestamp()
	require.NoError(t, service.UpsertSystemInstance(t.Context(), "stale-node", map[string]string{"version": "old"}, 1, now-1000))
	require.NoError(t, service.UpsertSystemInstance(t.Context(), "boundary-node", nil, 1, now-90))
	deleted, err := service.DeleteStaleSystemInstance(t.Context(), "boundary-node", now)
	require.NoError(t, err)
	assert.False(t, deleted)
	deleted, err = service.DeleteStaleSystemInstance(t.Context(), "boundary-node", now+1)
	require.NoError(t, err)
	assert.True(t, deleted)
	// A heartbeat that refreshed an old row must protect it from the delete predicate.
	require.NoError(t, service.UpsertSystemInstance(t.Context(), "revived-node", nil, 1, now-1000))
	require.NoError(t, service.UpsertSystemInstance(t.Context(), "revived-node", nil, 2, now))
	deleted, err = service.DeleteStaleSystemInstance(t.Context(), "revived-node", now)
	require.NoError(t, err)
	assert.False(t, deleted)
	handler := systemhttp.New(service)
	router := gin.New()
	router.GET("/instances", handler.ListSystemInstances)
	router.DELETE("/instances/stale", handler.DeleteStaleSystemInstances)
	router.DELETE("/instances/:node_name", handler.DeleteStaleSystemInstance)
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/instances", nil))
	require.Equal(t, http.StatusOK, list.Code)
	var response struct {
		Success bool                              `json:"success"`
		Data    []contract.SystemInstanceResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(list.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Len(t, response.Data, 3)
	assert.Equal(t, "stale-node", response.Data[2].NodeName)
	assert.Equal(t, contract.SystemInstanceStatusStale, response.Data[2].Status)
	alive := httptest.NewRecorder()
	router.ServeHTTP(alive, httptest.NewRequest(http.MethodDelete, "/instances/node-a", nil))
	assert.Contains(t, alive.Body.String(), `"success":false`)
	cleanup := httptest.NewRecorder()
	router.ServeHTTP(cleanup, httptest.NewRequest(http.MethodDelete, "/instances/stale", nil))
	assert.Contains(t, cleanup.Body.String(), `"deleted_count":1`)
	require.NoError(t, database.Model(&entity.SystemInstance{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestInstanceReporterUsesHostnameAndStopsOnCancellation(t *testing.T) {
	database, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	service := system.New(system.Dependencies{DB: database, InstanceReport: system.InstanceReportConfig{Version: "fallback", StartedAt: 1}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := service.StartSystemInstanceReporter(ctx)
	require.Eventually(t, func() bool {
		rows, err := service.Instances(t.Context())
		return err == nil && len(rows) == 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("instance reporter did not stop")
	}
	hostname, err := os.Hostname()
	require.NoError(t, err)
	var row entity.SystemInstance
	require.NoError(t, database.First(&row, "node_name = ?", hostname).Error)
	var info contract.SystemInstanceInfo
	require.NoError(t, common.UnmarshalJsonStr(row.Info, &info))
	assert.Equal(t, common.NodeNameSourceHostname, info.Node.Source)
	assert.True(t, info.Node.ShouldConfigureManually)
	assert.False(t, info.Role.IsMaster)
}
