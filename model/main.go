package model

import (
	"context"
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

func lockForUpdateSkipLocked(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
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
		InitPostgresRuntimeState()
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))

		common.SysLog("database migration started")
		err = WithBlockingPostgresAdvisoryLock(context.Background(), "new-api:migration:main", func(context.Context) error {
			return migrateDB()
		})
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

		common.SysLog("database migration started")
		err = WithBlockingPostgresAdvisoryLock(context.Background(), "new-api:migration:log", func(context.Context) error {
			return migrateLOGDB()
		})
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

func migrateDB() error {
	autoMigrateLog := false
	if !hasSeparateLogDB() {
		var err error
		autoMigrateLog, err = preparePostgresLogTable(DB)
		if err != nil {
			return err
		}
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
	if err := DB.AutoMigrate(models...); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&SubscriptionPlan{}); err != nil {
		return err
	}
	if err := runPostgresMainMigrations(DB); err != nil {
		return err
	}
	if !hasSeparateLogDB() {
		if err := runPostgresLogMigrations(DB); err != nil {
			return err
		}
	}
	return nil
}

func migrateDBFast() error {
	autoMigrateLog := false
	if !hasSeparateLogDB() {
		var err error
		autoMigrateLog, err = preparePostgresLogTable(DB)
		if err != nil {
			return err
		}
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
	if err := runPostgresMainMigrations(DB); err != nil {
		return err
	}
	if !hasSeparateLogDB() {
		if err := runPostgresLogMigrations(DB); err != nil {
			return err
		}
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
	return runPostgresLogMigrations(LOG_DB)
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
