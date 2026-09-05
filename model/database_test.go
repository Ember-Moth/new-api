package model

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostgreSQLBelow18IsRejectedForMainAndLogDatabases(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_OLD_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_OLD_DSN is required to exercise an unsupported PostgreSQL server")
	}
	for _, isLog := range []bool{false, true} {
		envName := "SQL_DSN"
		if isLog {
			envName = "LOG_SQL_DSN"
		}
		t.Run(envName, func(t *testing.T) {
			t.Setenv(envName, dsn)
			db, _, err := chooseDB(envName, isLog)
			require.ErrorContains(t, err, "requires PostgreSQL 18 or newer")
			assert.Nil(t, db)
			assert.Contains(t, err.Error(), envName)
		})
	}
}

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
			token := Token{UserId: user.Id, Key: strings.Repeat("k", 64), Group: "default"}
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
			group := PrefillGroup{Name: "reusable-name", Type: "model", Items: JSONValue(`[]`)}
			require.NoError(t, DB.Create(&group).Error)
			assert.Error(t, DB.Create(&PrefillGroup{Name: group.Name, Type: group.Type}).Error)
			require.NoError(t, DB.Delete(&group).Error)
			require.NoError(t, DB.Create(&PrefillGroup{Name: group.Name, Type: group.Type}).Error)
			assert.Equal(t, common.DatabaseTypePostgreSQL, common.MainDatabaseType())
			assert.Equal(t, common.DatabaseTypePostgreSQL, common.LogDatabaseType())
		})
	}
}

func TestPostgreSQLMigrationsSerializeAndLeaveApplicationPoolUsable(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			errors <- schema.UpPostgres(pool, schema.Main)
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errors)
	}
	require.NoError(t, schema.UpPostgres(pool, schema.Logs))
	user := User{Username: "migration-user", AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	assert.Positive(t, user.Id, "the SQL identity must generate keys for GORM inserts")
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	var preserved User
	require.NoError(t, db.First(&preserved, user.Id).Error)
	assert.Equal(t, user.Username, preserved.Username)
	for _, table := range []string{"schema_migrations", "schema_migrations_logs"} {
		var state struct {
			Version int
			Dirty   bool
		}
		require.NoError(t, db.Table(table).Take(&state).Error)
		assert.Equal(t, 1, state.Version)
		assert.False(t, state.Dirty)
	}
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, pool.PingContext(ctx), "migration Close must return its connection without closing the pool")
}

func TestPostgreSQLMigrationDownPreservesUnrelatedTablesAndAllowsReinitialization(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE unrelated_records (id bigint PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO unrelated_records VALUES (1)").Error)
	for _, scope := range []schema.Scope{schema.Main, schema.Logs} {
		require.NoError(t, schema.UpPostgres(pool, scope))
	}
	for _, scope := range []schema.Scope{schema.Logs, schema.Main} {
		client, err := schema.NewPostgres(pool, scope)
		require.NoError(t, err)
		downErr := client.Down()
		sourceErr, databaseErr := client.Close()
		require.NoError(t, downErr)
		require.NoError(t, sourceErr)
		require.NoError(t, databaseErr)
	}
	assert.False(t, db.Migrator().HasTable(&User{}))
	assert.False(t, db.Migrator().HasTable(&Log{}))
	var retained int
	require.NoError(t, db.Raw("SELECT id FROM unrelated_records").Scan(&retained).Error)
	assert.Equal(t, 1, retained)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Logs))
	require.NoError(t, db.Create(&User{Username: "reinitialized"}).Error)
}

func TestPostgreSQLFailedMigrationRollsBackDDLAndRefusesAutomaticDirtyRecovery(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	// A conflicting table causes a failure after earlier tables in the file.
	require.NoError(t, db.Exec("CREATE TABLE tokens (id bigint PRIMARY KEY)").Error)
	require.Error(t, schema.UpPostgres(pool, schema.Main))
	assert.False(t, db.Migrator().HasTable(&Ability{}), "failed migration must not leave partial tables behind")
	var dirty migrate.ErrDirty
	require.ErrorAs(t, schema.UpPostgres(pool, schema.Main), &dirty)
	assert.Equal(t, 1, dirty.Version)
	require.NoError(t, pool.PingContext(t.Context()))
}

func TestClickHouseLogMigrationsPreserveRowsAndApplyRetention(t *testing.T) {
	dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("TEST_CLICKHOUSE_DSN is required for real ClickHouse migrations")
	}
	admin, err := sql.Open("clickhouse", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	databaseName := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec("CREATE DATABASE " + databaseName)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP DATABASE " + databaseName)
		require.NoError(t, err)
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	logDSN := parsed.String()
	coordinator, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := coordinator.DB()
	require.NoError(t, err)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			results <- schema.UpClickHouse(logDSN, pool)
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-results)
	}
	previousDB, previousLogDB, previousMaster := DB, LOG_DB, common.IsMasterNode
	previousType := common.LogDatabaseType()
	t.Cleanup(func() {
		DB, LOG_DB, common.IsMasterNode = previousDB, previousLogDB, previousMaster
		common.SetLogDatabaseType(previousType)
		initCol()
	})
	DB, common.IsMasterNode = coordinator, true
	t.Setenv("LOG_SQL_DSN", logDSN)
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "7")
	require.NoError(t, InitLogDB())
	t.Cleanup(func() { require.NoError(t, closeDB(LOG_DB)) })
	entry := Log{Id: 42, CreatedAt: common.GetTimestamp(), Content: "retained log", RequestId: "migration-request", Quota: 17}
	require.NoError(t, LOG_DB.Create(&entry).Error)
	for range 2 {
		require.NoError(t, closeDB(LOG_DB))
		require.NoError(t, InitLogDB())
	}
	var retained Log
	require.NoError(t, LOG_DB.Where("request_id = ?", entry.RequestId).Take(&retained).Error)
	assert.Equal(t, entry.Content, retained.Content)
	assert.Equal(t, entry.Quota, retained.Quota)
	var definition string
	require.NoError(t, LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&definition).Error)
	assert.Contains(t, definition, "toIntervalDay(7)")
	var migrationState string
	require.NoError(t, LOG_DB.Raw("SELECT concat(toString(version), ':', toString(dirty)) FROM schema_migrations_logs ORDER BY sequence DESC LIMIT 1").Scan(&migrationState).Error)
	assert.Equal(t, "1:0", migrationState)
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "0")
	require.NoError(t, closeDB(LOG_DB))
	require.NoError(t, InitLogDB())
	require.NoError(t, LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&definition).Error)
	assert.NotContains(t, definition, "TTL ")
	assert.NoError(t, pool.PingContext(t.Context()), "coordinator remains usable")
}
