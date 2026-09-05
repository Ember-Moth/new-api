package model

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"
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
				t.Cleanup(func() { require.NoError(t, closeDB(logsDB)) })
			}
			previousDB, previousLog, previousType := DB, LOG_DB, common.LogDatabaseType()
			DB, LOG_DB = primary, logsDB
			common.SetLogDatabaseType(kind)
			initCol()
			t.Cleanup(func() { DB, LOG_DB = previousDB, previousLog; common.SetLogDatabaseType(previousType); initCol() })
			for _, content := range []string{"first", "second", "third", "fourth", "fifth"} {
				require.NoError(t, logsDB.Create(&Log{UserId: 7, CreatedAt: 100, RequestId: "same-request", Content: content, Type: LogTypeConsume, Other: `{"admin_info":{"secret":"hidden"}}`}).Error)
			}
			require.NoError(t, logsDB.Create(&Log{UserId: 8, CreatedAt: 100, RequestId: "same-request", Content: "other-user", Type: LogTypeConsume}).Error)
			var contents []string
			cursor := ""
			for pageNumber := 0; pageNumber < 3; pageNumber++ {
				page, err := NewLogCursorPage(cursor, "self:7")
				require.NoError(t, err)
				rows, total, err := GetUserLogs(7, LogTypeUnknown, 0, 0, "", "", pageNumber*2, 2, "", "", "", page)
				require.NoError(t, err)
				assert.Zero(t, total, "cursor requests do not compute a total count")
				for _, row := range rows {
					contents = append(contents, row.Content)
					assert.NotContains(t, row.Other, "hidden")
				}
				if pageNumber == 0 {
					require.True(t, page.HasMore)
					_, err := NewLogCursorPage(page.NextCursor, "self:8")
					assert.ErrorIs(t, err, ErrInvalidLogCursor)
					_, err = NewLogCursorPage(page.NextCursor+"tampered", "self:7")
					assert.ErrorIs(t, err, ErrInvalidLogCursor)
					require.NoError(t, logsDB.Create(&Log{UserId: 7, CreatedAt: 101, Content: "newer", Type: LogTypeConsume}).Error)
				}
				cursor = page.NextCursor
				assert.Equal(t, pageNumber < 2, page.HasMore)
			}
			assert.ElementsMatch(t, []string{"first", "second", "third", "fourth", "fifth"}, contents)
		})
	}
}
