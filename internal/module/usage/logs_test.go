package usage_test

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"net/http"
	"net/http/httptest"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/usage"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

func TestLogCursorPaginationPreservesTiesAndViewerIsolation(t *testing.T) {
	for _, kind := range []common.DatabaseType{common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse} {
		t.Run(string(kind), func(t *testing.T) {
			primary, err := testdb.Open(t, &gorm.Config{})
			require.NoError(t, err)
			pool, err := primary.DB()
			require.NoError(t, err)
			logsDB := primary
			if kind == common.DatabaseTypePostgreSQL {
				require.NoError(t, schema.UpPostgres(pool, schema.Logs))
			} else {
				dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
				if dsn == "" {
					t.Skip("TEST_CLICKHOUSE_DSN is required")
				}
				admin, err := sql.Open("clickhouse", dsn)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, admin.Close()) })
				name := "cursor_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				_, err = admin.Exec("CREATE DATABASE " + name)
				require.NoError(t, err)
				t.Cleanup(func() { _, err := admin.Exec("DROP DATABASE " + name); require.NoError(t, err) })
				parsed, err := url.Parse(dsn)
				require.NoError(t, err)
				parsed.Path = "/" + name
				require.NoError(t, schema.UpClickHouse(parsed.String(), pool))
				logsDB, err = gorm.Open(clickhouse.Open(parsed.String()), &gorm.Config{})
				require.NoError(t, err)
				t.Cleanup(func() { pool, err := logsDB.DB(); require.NoError(t, err); require.NoError(t, pool.Close()) })
			}
			store := usage.New(usage.Dependencies{DB: logsDB, Kind: kind})
			for _, content := range []string{"first", "second", "third", "fourth", "fifth"} {
				require.NoError(t, logsDB.Create(&usage.Log{UserId: 7, CreatedAt: 100, RequestId: "same-request", Content: content, Type: 2, Other: `{"admin_info":{"secret":"hidden"}}`}).Error)
			}
			require.NoError(t, logsDB.Create(&usage.Log{UserId: 8, CreatedAt: 100, RequestId: "same-request", Content: "other-user", Type: 2}).Error)
			var contents []string
			cursor := ""
			for pageNumber := 0; pageNumber < 3; pageNumber++ {
				page, err := store.NewLogCursorPage(cursor, "self:7")
				require.NoError(t, err)
				rows, total, err := store.GetUserLogs(t.Context(), 7, 0, 0, 0, "", "", pageNumber*2, 2, "", "", "", page)
				require.NoError(t, err)
				assert.Zero(t, total, "cursor requests do not compute a total count")
				for _, row := range rows {
					contents = append(contents, row.Content)
					assert.NotContains(t, row.Other, "hidden")
				}
				if pageNumber == 0 {
					require.True(t, page.HasMore)
					_, err := store.NewLogCursorPage(page.NextCursor, "self:8")
					assert.ErrorIs(t, err, usage.ErrInvalidLogCursor)
					_, err = store.NewLogCursorPage(page.NextCursor+"tampered", "self:7")
					assert.ErrorIs(t, err, usage.ErrInvalidLogCursor)
					require.NoError(t, logsDB.Create(&usage.Log{UserId: 7, CreatedAt: 101, Content: "newer", Type: 2}).Error)
				}
				cursor = page.NextCursor
				assert.Equal(t, pageNumber < 2, page.HasMore)
			}
			assert.ElementsMatch(t, []string{"first", "second", "third", "fourth", "fifth"}, contents)
			cutoff := common.GetTimestamp()
			own := &usage.Log{UserId: 70, Username: "shared%", CreatedAt: cutoff, Type: 2, Quota: 50, PromptTokens: 3, CompletionTokens: 2, ModelName: `gpt_4\mini`, RequestId: "preserved-request", Other: `{"admin_info":{"secret":"private"}}`}
			foreign := &usage.Log{UserId: 80, Username: "shared-other", CreatedAt: cutoff, Type: 2, Quota: 99999, PromptTokens: 100, ModelName: `gptX4\mini`}
			require.NoError(t, store.Create(t.Context(), own))
			require.NoError(t, store.Create(t.Context(), foreign))
			assert.Equal(t, "preserved-request", own.RequestId)
			assert.NotEmpty(t, foreign.RequestId)
			filtered, total, err := store.GetAllLogs(t.Context(), 0, cutoff, 0, `gpt_4\mini%`, "", "", 0, 10, 0, "", "", "")
			require.NoError(t, err)
			assert.Equal(t, int64(1), total)
			require.Len(t, filtered, 1)
			assert.Equal(t, own.RequestId, filtered[0].RequestId)
			handler := usagehttp.New(store)
			router := gin.New()
			router.GET("/self-stat", func(c *gin.Context) { c.Set("id", 70); c.Set("username", "shared%"); handler.GetLogsSelfStat(c) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/self-stat", nil))
			require.Equal(t, http.StatusOK, response.Code)
			var statistic struct {
				Success bool       `json:"success"`
				Data    usage.Stat `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &statistic))
			require.True(t, statistic.Success, response.Body.String())
			assert.Equal(t, 50, statistic.Data.Quota)
			assert.Equal(t, 1, statistic.Data.Rpm)
			assert.Equal(t, 5, statistic.Data.Tpm)
			userLogs, _, err := store.GetUserLogs(t.Context(), 70, 0, 0, 0, "", "", 20, 10, "", "", "")
			require.NoError(t, err)
			assert.Empty(t, userLogs)
			userLogs, _, err = store.GetUserLogs(t.Context(), 70, 0, 0, 0, "", "", 0, 10, "", "", "")
			require.NoError(t, err)
			require.Len(t, userLogs, 1)
			assert.Equal(t, 1, userLogs[0].Id)
			assert.NotContains(t, userLogs[0].Other, "private")
			before, err := store.CountOldLog(t.Context(), cutoff)
			require.NoError(t, err)
			require.Positive(t, before)
			deleted, err := store.DeleteOldLogBatch(t.Context(), cutoff, 1)
			require.NoError(t, err)
			if kind == common.DatabaseTypePostgreSQL {
				assert.Equal(t, int64(1), deleted)
			} else {
				assert.Equal(t, before, deleted)
			}
			after, err := store.CountOldLog(t.Context(), cutoff)
			require.NoError(t, err)
			assert.Equal(t, before-deleted, after)

		})
	}
}
