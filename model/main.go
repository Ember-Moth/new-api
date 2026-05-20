package model

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var commonGroupCol string
var commonKeyCol string

var logKeyCol string
var logGroupCol string

func init() {
	initCol()
}

func initCol() {
	commonGroupCol = `"group"`
	commonKeyCol = `"key"`
	if os.Getenv("LOG_SQL_DSN") != "" {
		logGroupCol = `"group"`
		logKeyCol = `"key"`
	} else {
		logGroupCol = commonGroupCol
		logKeyCol = commonKeyCol
	}
}

func lockForUpdate(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

var DB *gorm.DB

var LOG_DB *gorm.DB

const postgresAutoCreatePerformanceIndexesEnv = "POSTGRES_AUTO_CREATE_PERFORMANCE_INDEXES"

func createRootAccountIfNeed() error {
	var user User
	//if user.Status != common.UserStatusEnabled {
	if err := DB.First(&user).Error; err != nil {
		common.SysLog("no user exists, create a root user for you: username is root, password is 123456")
		hashedPassword, err := common.Password2Hash("123456")
		if err != nil {
			return err
		}
		rootUser := User{
			Username:    "root",
			Password:    hashedPassword,
			Role:        common.RoleRootUser,
			Status:      common.UserStatusEnabled,
			DisplayName: "Root User",
			AccessToken: nil,
			Quota:       100000000,
		}
		DB.Create(&rootUser)
	}
	return nil
}

func CheckSetup() {
	setup := GetSetup()
	if setup == nil {
		// No setup record exists, check if we have a root user
		if RootUserExists() {
			common.SysLog("system is not initialized, but root user exists")
			// Create setup record
			newSetup := Setup{
				Version:       common.Version,
				InitializedAt: time.Now().Unix(),
			}
			err := DB.Create(&newSetup).Error
			if err != nil {
				common.SysLog("failed to create setup record: " + err.Error())
			}
			constant.Setup = true
		} else {
			common.SysLog("system is not initialized and no root user exists")
			constant.Setup = false
		}
	} else {
		// Setup record exists, system is initialized
		common.SysLog("system is already initialized at: " + time.Unix(setup.InitializedAt, 0).String())
		constant.Setup = true
	}
}

func chooseDB(envName string) (*gorm.DB, error) {
	defer func() {
		initCol()
	}()
	dsn := strings.TrimSpace(os.Getenv(envName))
	if dsn == "" {
		return nil, fmt.Errorf("%s is required; only PostgreSQL is supported", envName)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil, fmt.Errorf("%s must be a PostgreSQL DSN starting with postgres:// or postgresql://", envName)
	}
	common.SysLog("using PostgreSQL as database")
	return gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true, // disables implicit prepared statement usage
	}), &gorm.Config{
		PrepareStmt: true, // precompile SQL
	})
}

func InitDB() (err error) {
	db, err := chooseDB("SQL_DSN")
	if err == nil {
		if common.DebugEnabled {
			db = db.Debug()
		}
		DB = db
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))

		if !common.IsMasterNode {
			return nil
		}
		common.SysLog("database migration started")
		err = migrateDB()
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

func InitLogDB() (err error) {
	if os.Getenv("LOG_SQL_DSN") == "" {
		LOG_DB = DB
		return
	}
	db, err := chooseDB("LOG_SQL_DSN")
	if err == nil {
		if common.DebugEnabled {
			db = db.Debug()
		}
		LOG_DB = db
		sqlDB, err := LOG_DB.DB()
		if err != nil {
			return err
		}
		sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))

		if !common.IsMasterNode {
			return nil
		}
		common.SysLog("database migration started")
		err = migrateLOGDB()
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

func migrateDB() error {
	// Migrate price_amount column from float/double to decimal for existing tables
	migrateSubscriptionPlanPriceAmount()
	// Migrate model_limits column from varchar to text for existing tables
	if err := migrateTokenModelLimitsToText(); err != nil {
		return err
	}
	autoMigrateLog, err := preparePostgresLogTable(DB)
	if err != nil {
		return err
	}

	models := []interface{}{
		&Channel{},
		&Token{},
		&User{},
		&PasskeyCredential{},
		&Option{},
		&Redemption{},
		&Ability{},
		&Midjourney{},
		&TopUp{},
		&QuotaData{},
		&QuotaDataDaily{},
		&QuotaDataMonthly{},
		&Task{},
		&Model{},
		&Vendor{},
		&PrefillGroup{},
		&Setup{},
		&TwoFA{},
		&TwoFABackupCode{},
		&Checkin{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&SubscriptionPreConsumeRecord{},
		&CustomOAuthProvider{},
		&UserOAuthBinding{},
		&PerfMetric{},
	}
	if autoMigrateLog {
		models = append(models, &Log{})
	}
	err = DB.AutoMigrate(models...)
	if err != nil {
		return err
	}
	if err := DB.AutoMigrate(&SubscriptionPlan{}); err != nil {
		return err
	}
	if err := ensurePostgresQuotaDataRollups(DB); err != nil {
		return err
	}
	return ensurePostgresPerformanceIndexesIfEnabled(DB)
}

func migrateDBFast() error {
	autoMigrateLog, err := preparePostgresLogTable(DB)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup

	migrations := []struct {
		model interface{}
		name  string
	}{
		{&Channel{}, "Channel"},
		{&Token{}, "Token"},
		{&User{}, "User"},
		{&PasskeyCredential{}, "PasskeyCredential"},
		{&Option{}, "Option"},
		{&Redemption{}, "Redemption"},
		{&Ability{}, "Ability"},
		{&Midjourney{}, "Midjourney"},
		{&TopUp{}, "TopUp"},
		{&QuotaData{}, "QuotaData"},
		{&QuotaDataDaily{}, "QuotaDataDaily"},
		{&QuotaDataMonthly{}, "QuotaDataMonthly"},
		{&Task{}, "Task"},
		{&Model{}, "Model"},
		{&Vendor{}, "Vendor"},
		{&PrefillGroup{}, "PrefillGroup"},
		{&Setup{}, "Setup"},
		{&TwoFA{}, "TwoFA"},
		{&TwoFABackupCode{}, "TwoFABackupCode"},
		{&Checkin{}, "Checkin"},
		{&SubscriptionOrder{}, "SubscriptionOrder"},
		{&UserSubscription{}, "UserSubscription"},
		{&SubscriptionPreConsumeRecord{}, "SubscriptionPreConsumeRecord"},
		{&CustomOAuthProvider{}, "CustomOAuthProvider"},
		{&UserOAuthBinding{}, "UserOAuthBinding"},
		{&PerfMetric{}, "PerfMetric"},
	}
	if autoMigrateLog {
		migrations = append(migrations, struct {
			model interface{}
			name  string
		}{&Log{}, "Log"})
	}
	// 动态计算migration数量，确保errChan缓冲区足够大
	errChan := make(chan error, len(migrations))

	for _, m := range migrations {
		wg.Add(1)
		go func(model interface{}, name string) {
			defer wg.Done()
			if err := DB.AutoMigrate(model); err != nil {
				errChan <- fmt.Errorf("failed to migrate %s: %v", name, err)
			}
		}(m.model, m.name)
	}

	// Wait for all migrations to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}
	if err := DB.AutoMigrate(&SubscriptionPlan{}); err != nil {
		return err
	}
	if err := ensurePostgresQuotaDataRollups(DB); err != nil {
		return err
	}
	if err := ensurePostgresPerformanceIndexesIfEnabled(DB); err != nil {
		return err
	}
	common.SysLog("database migrated")
	return nil
}

func migrateLOGDB() error {
	autoMigrateLog, err := preparePostgresLogTable(LOG_DB)
	if err != nil {
		return err
	}
	if autoMigrateLog {
		if err = LOG_DB.AutoMigrate(&Log{}); err != nil {
			return err
		}
	}
	return ensurePostgresLogPerformanceIndexesIfEnabled(LOG_DB)
}

func ensurePostgresPerformanceIndexesIfEnabled(db *gorm.DB) error {
	if !common.GetEnvOrDefaultBool(postgresAutoCreatePerformanceIndexesEnv, true) {
		common.SysLog("skipping PostgreSQL performance index auto-creation; run docs/installation/postgresql-performance-indexes.sql in production")
		return nil
	}
	common.SysLog("creating PostgreSQL performance indexes with regular CREATE INDEX")
	return ensurePostgresPerformanceIndexes(db)
}

func ensurePostgresLogPerformanceIndexesIfEnabled(db *gorm.DB) error {
	if !common.GetEnvOrDefaultBool(postgresAutoCreatePerformanceIndexesEnv, true) {
		common.SysLog("skipping PostgreSQL log performance index auto-creation; run docs/installation/postgresql-performance-indexes.sql in production")
		return nil
	}
	common.SysLog("creating PostgreSQL log performance indexes with regular CREATE INDEX")
	return ensurePostgresLogPerformanceIndexes(db)
}

func ensurePostgresPerformanceIndexes(db *gorm.DB) error {
	if err := ensurePostgresLogPerformanceIndexes(db); err != nil {
		return err
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_abilities_lookup ON abilities ("group", model, enabled, priority DESC, weight DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_user_id_id ON tasks (user_id, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_submit_time_id ON tasks (submit_time, id)`,
		`CREATE INDEX IF NOT EXISTS idx_top_ups_user_create_id ON top_ups (user_id, create_time DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_top_ups_user_id_id_desc ON top_ups (user_id, id DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_quota_data_hour ON quota_data (user_id, username, model_name, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_user_id_created ON quota_data (user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_username_created ON quota_data (username, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_created_model ON quota_data (created_at, model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_daily_user_id_created ON quota_data_daily (user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_daily_username_created ON quota_data_daily (username, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_daily_created_model ON quota_data_daily (created_at, model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_monthly_user_id_created ON quota_data_monthly (user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_monthly_username_created ON quota_data_monthly (username, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_quota_data_monthly_created_model ON quota_data_monthly (created_at, model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_user_subscriptions_status_end_id ON user_subscriptions (status, end_time, id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_subscriptions_status_next_reset ON user_subscriptions (status, next_reset_time, id)`,
	}
	return execIndexStatements(db, statements)
}

func ensurePostgresLogPerformanceIndexes(db *gorm.DB) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_created_at_id ON logs (id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_created_at_type ON logs (created_at, type)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_channel_id ON logs (channel_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_group ON logs ("group")`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ip ON logs (ip)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_model_name ON logs (model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_request_id ON logs (request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_token_id ON logs (token_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_token_name ON logs (token_name)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_upstream_request_id ON logs (upstream_request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_user_id ON logs (user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_username ON logs (username)`,
		`CREATE INDEX IF NOT EXISTS idx_user_id_id ON logs (user_id, id)`,
		`CREATE INDEX IF NOT EXISTS index_username_model_name ON logs (model_name, username)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_type_created_at ON logs (type, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_user_id_id_desc ON logs (user_id, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_user_type_id ON logs (user_id, type, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_created_at_id_desc ON logs (created_at DESC, id DESC)`,
	}
	return execIndexStatements(db, statements)
}

func execIndexStatements(db *gorm.DB, statements []string) error {
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("failed to create PostgreSQL performance index: %w", err)
		}
	}
	return nil
}

// migrateTokenModelLimitsToText migrates model_limits column from varchar(1024) to text
// This is safe to run multiple times - it checks the column type first
func migrateTokenModelLimitsToText() error {
	tableName := "tokens"
	columnName := "model_limits"

	if !DB.Migrator().HasTable(tableName) {
		return nil
	}

	if !DB.Migrator().HasColumn(&Token{}, columnName) {
		return nil
	}

	var dataType string
	if err := DB.Raw(`SELECT data_type FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		tableName, columnName).Scan(&dataType).Error; err != nil {
		common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
	} else if strings.ToLower(dataType) == "text" {
		return nil
	}

	alterSQL := fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE text`, tableName, columnName)
	if err := DB.Exec(alterSQL).Error; err != nil {
		return fmt.Errorf("failed to migrate %s.%s to text: %w", tableName, columnName, err)
	}
	common.SysLog(fmt.Sprintf("Successfully migrated %s.%s to text", tableName, columnName))
	return nil
}

// migrateSubscriptionPlanPriceAmount migrates price_amount column from float/double to decimal(10,6)
// This is safe to run multiple times - it checks the column type first
func migrateSubscriptionPlanPriceAmount() {
	tableName := "subscription_plans"
	columnName := "price_amount"

	// Check if table exists first
	if !DB.Migrator().HasTable(tableName) {
		return
	}

	// Check if column exists
	if !DB.Migrator().HasColumn(&SubscriptionPlan{}, columnName) {
		return
	}

	var dataType string
	if err := DB.Raw(`SELECT data_type FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		tableName, columnName).Scan(&dataType).Error; err != nil {
		common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
	} else if strings.ToLower(dataType) == "numeric" {
		return
	}

	alterSQL := fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE decimal(10,6) USING %s::decimal(10,6)`,
		tableName, columnName, columnName)
	if err := DB.Exec(alterSQL).Error; err != nil {
		common.SysLog(fmt.Sprintf("Warning: failed to migrate %s.%s to decimal: %v", tableName, columnName, err))
	} else {
		common.SysLog(fmt.Sprintf("Successfully migrated %s.%s to decimal(10,6)", tableName, columnName))
	}
}

func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	return err
}

func CloseDB() error {
	if LOG_DB != nil && LOG_DB != DB {
		err := closeDB(LOG_DB)
		if err != nil {
			return err
		}
	}
	return closeDB(DB)
}

var (
	lastPingTime time.Time
	pingMutex    sync.Mutex
)

func PingDB() error {
	pingMutex.Lock()
	defer pingMutex.Unlock()

	if time.Since(lastPingTime) < time.Second*10 {
		return nil
	}

	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("Error getting sql.DB from GORM: %v", err)
		return err
	}

	err = sqlDB.Ping()
	if err != nil {
		log.Printf("Error pinging DB: %v", err)
		return err
	}

	lastPingTime = time.Now()
	common.SysLog("Database pinged successfully")
	return nil
}
