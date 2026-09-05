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

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/system"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/model"
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
