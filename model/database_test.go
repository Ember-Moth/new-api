package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabaseRejectsMissingAndUnsupportedDSNs(t *testing.T) {
	for _, test := range []struct {
		name string
		dsn  string
	}{
		{name: "missing"},
		{name: "whitespace", dsn: "   "},
		{name: "local alias", dsn: "local"},
		{name: "sqlite file", dsn: "file:data.db"},
		{name: "sqlite URL", dsn: "sqlite://data.db"},
		{name: "mysql DSN", dsn: "root:private-password@tcp(localhost:3306)/new-api"},
		{name: "mysql URL", dsn: "mysql://root:private-password@localhost/new-api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, isLog := range []bool{false, true} {
				envName := "SQL_DSN"
				if isLog {
					envName = "LOG_SQL_DSN"
				}
				t.Setenv(envName, test.dsn)
				db, _, err := chooseDB(envName, isLog)
				require.Error(t, err)
				assert.Nil(t, db)
				assert.Contains(t, err.Error(), envName)
				assert.Contains(t, err.Error(), "PostgreSQL")
				assert.NotContains(t, err.Error(), "private-password")
			}
		})
	}
	t.Run("ClickHouse cannot be a primary database", func(t *testing.T) {
		t.Setenv("SQL_DSN", "clickhouse://default:private-password@localhost:9000/logs")
		_, _, err := chooseDB("SQL_DSN", false)
		require.ErrorContains(t, err, "ClickHouse is supported only through LOG_SQL_DSN")
		assert.NotContains(t, err.Error(), "private-password")
	})
}

func TestPostgreSQLStartupPreservesDataAndIndexesAcrossRestarts(t *testing.T) {
	for _, separateLog := range []bool{false, true} {
		name := "shared log database"
		if separateLog {
			name = "separate log database"
		}
		t.Run(name, func(t *testing.T) {
			previousDB, previousLogDB := DB, LOG_DB
			previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			previousMaster := common.IsMasterNode
			t.Cleanup(func() {
				DB, LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
				common.IsMasterNode = previousMaster
				initCol()
			})
			common.IsMasterNode = true
			t.Setenv("SQL_DSN", testdb.DSN(t))
			t.Setenv("LOG_SQL_DSN", "")
			if separateLog {
				t.Setenv("LOG_SQL_DSN", testdb.DSN(t))
			}
			require.NoError(t, InitDB())
			require.NoError(t, InitLogDB())
			t.Cleanup(func() { require.NoError(t, CloseDB()) })

			user := User{Username: "postgres-restart", DisplayName: "数据库验证", Quota: 1 << 40}
			require.NoError(t, DB.Create(&user).Error)
			token := Token{UserId: user.Id, Key: "preserved-api-key", Group: "default"}
			require.NoError(t, DB.Create(&token).Error)
			plan := SubscriptionPlan{Title: "preserved plan", PriceAmount: 1.234567, Enabled: true}
			require.NoError(t, DB.Create(&plan).Error)
			entry := Log{UserId: user.Id, Content: "preserved log", RequestId: "restart-request"}
			require.NoError(t, LOG_DB.Create(&entry).Error)

			var originalIndexes []string
			require.NoError(t, DB.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() ORDER BY indexname`).Scan(&originalIndexes).Error)
			var originalLogIndexes []string
			require.NoError(t, LOG_DB.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() ORDER BY indexname`).Scan(&originalLogIndexes).Error)
			for range 2 {
				require.NoError(t, CloseDB())
				require.NoError(t, InitDB())
				require.NoError(t, InitLogDB())
			}

			var preservedUser User
			require.NoError(t, DB.First(&preservedUser, user.Id).Error)
			assert.Equal(t, user.Quota, preservedUser.Quota)
			assert.Equal(t, user.DisplayName, preservedUser.DisplayName)
			var preservedPlan SubscriptionPlan
			require.NoError(t, DB.First(&preservedPlan, plan.Id).Error)
			assert.Equal(t, plan.PriceAmount, preservedPlan.PriceAmount)
			assert.True(t, preservedPlan.Enabled)
			var preservedLog Log
			require.NoError(t, LOG_DB.First(&preservedLog, entry.Id).Error)
			assert.Equal(t, entry.Content, preservedLog.Content)
			var indexes, logIndexes []string
			require.NoError(t, DB.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() ORDER BY indexname`).Scan(&indexes).Error)
			require.NoError(t, LOG_DB.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() ORDER BY indexname`).Scan(&logIndexes).Error)
			assert.Equal(t, originalIndexes, indexes)
			assert.Equal(t, originalLogIndexes, logIndexes)
			assert.Error(t, DB.Create(&Token{UserId: user.Id, Key: token.Key}).Error)
			assert.Equal(t, common.DatabaseTypePostgreSQL, common.MainDatabaseType())
			assert.Equal(t, common.DatabaseTypePostgreSQL, common.LogDatabaseType())
		})
	}
}
