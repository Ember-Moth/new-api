package model

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"

	"gorm.io/driver/clickhouse"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const commonGroupCol = `"group"`
const commonKeyCol = `"key"`

// jsonScanBytes 归一化 json 列的驱动返回值:不同驱动/协议模式下同一列可能
// 以 []byte 或 string 返回,静默丢弃 string 会导致字段被清零而不报错。
func jsonScanBytes(value interface{}) []byte {
	switch v := value.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		return nil
	}
}

var DB *gorm.DB

var LOG_DB *gorm.DB

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

func isClickHouseDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "clickhouse://") ||
		strings.HasPrefix(dsn, "tcp://") ||
		strings.HasPrefix(dsn, "http://") ||
		strings.HasPrefix(dsn, "https://")
}

func normalizeClickHouseDSN(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "https" {
		return dsn
	}
	query := parsed.Query()
	if _, ok := query["secure"]; !ok {
		query.Set("secure", "true")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func chooseDB(envName string, isLog bool) (*gorm.DB, common.DatabaseType, error) {
	dsn := strings.TrimSpace(os.Getenv(envName))
	if isLog {
		if dsn == "" {
			return nil, "", fmt.Errorf("%s is required; configure a ClickHouse URL", envName)
		}
		if !isClickHouseDSN(dsn) {
			return nil, "", fmt.Errorf("%s requires ClickHouse; PostgreSQL log databases are not supported", envName)
		}
		common.SysLog("using ClickHouse as log database")
		db, err := gorm.Open(clickhouse.Open(normalizeClickHouseDSN(dsn)), newGormConfig(false))
		return db, common.DatabaseTypeClickHouse, err
	}
	if dsn == "" {
		return nil, "", fmt.Errorf("%s is required; configure a PostgreSQL URL (postgres:// or postgresql://)", envName)
	}
	if isClickHouseDSN(dsn) {
		return nil, "", fmt.Errorf("%s requires PostgreSQL; ClickHouse is supported only through LOG_SQL_DSN", envName)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil, "", fmt.Errorf("%s requires a PostgreSQL URL (postgres:// or postgresql://); unsupported database configuration", envName)
	}
	common.SysLog("using PostgreSQL as database")
	// Disable both pgx implicit and GORM explicit prepared statements for
	// compatibility with transaction-pooling proxies such as PgBouncer.
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), newGormConfig(false))
	if err != nil {
		return nil, common.DatabaseTypePostgreSQL, err
	}
	var serverVersion int
	err = db.Raw("SELECT current_setting('server_version_num')::integer").Scan(&serverVersion).Error
	if err == nil && serverVersion < 180000 {
		err = fmt.Errorf("%s requires PostgreSQL 18 or newer; connected server is PostgreSQL %d", envName, serverVersion/10000)
	}
	if err != nil {
		if connection, closeErr := db.DB(); closeErr == nil {
			_ = connection.Close()
		}
		return nil, common.DatabaseTypePostgreSQL, err
	}
	return db, common.DatabaseTypePostgreSQL, nil
}

func InitDB() error {
	db, dbType, err := chooseDB("SQL_DSN", false)
	if err != nil {
		return err
	}
	common.SetMainDatabaseType(dbType)

	if common.DebugEnabled {
		db = db.Debug()
	}
	DB = db
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
	sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
	sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))
	if !common.IsControlPlane {
		return nil
	}
	common.SysLog("applying PostgreSQL schema migrations")
	return schema.UpPostgres(sqlDB)
}

func InitLogDB() error {
	db, _, err := chooseDB("LOG_SQL_DSN", true)
	if err != nil {
		return err
	}
	if common.DebugEnabled {
		db = db.Debug()
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
	sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
	sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))
	if common.IsControlPlane {
		coordinator, err := DB.DB()
		if err != nil {
			_ = sqlDB.Close()
			return err
		}
		common.SysLog("applying ClickHouse log schema migrations")
		if err := schema.UpClickHouse(normalizeClickHouseDSN(os.Getenv("LOG_SQL_DSN")), coordinator); err != nil {
			_ = sqlDB.Close()
			return err
		}
	}
	LOG_DB = db
	if common.IsControlPlane {
		return syncClickHouseLogTTL(clickHouseLogTTLDays())
	}
	return nil
}

func clickHouseLogTTLDays() int {
	ttlDays := common.GetEnvOrDefault("LOG_SQL_CLICKHOUSE_TTL_DAYS", 0)
	if ttlDays < 0 {
		return 0
	}
	return ttlDays
}

func clickHouseLogTTLExpression(ttlDays int) string {
	if ttlDays <= 0 {
		return ""
	}
	return fmt.Sprintf("toDateTime(created_at) + INTERVAL %d DAY DELETE", ttlDays)
}

func syncClickHouseLogTTL(ttlDays int) error {
	expression := clickHouseLogTTLExpression(ttlDays)
	if expression != "" {
		return LOG_DB.Exec("ALTER TABLE logs MODIFY TTL " + expression).Error
	}

	hasTTL, err := clickHouseLogTableHasTTL()
	if err != nil {
		return err
	}
	if !hasTTL {
		return nil
	}
	return LOG_DB.Exec("ALTER TABLE logs REMOVE TTL").Error
}

func clickHouseLogTableHasTTL() (bool, error) {
	var createTableSQL string
	if err := LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&createTableSQL).Error; err != nil {
		return false, err
	}
	return clickHouseCreateTableHasTTL(createTableSQL), nil
}

func clickHouseCreateTableHasTTL(createTableSQL string) bool {
	upperSQL := strings.ToUpper(createTableSQL)
	return strings.Contains(upperSQL, "\nTTL ") || strings.Contains(upperSQL, " TTL ")
}

func closeDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	return err
}

func CloseDB() error { return errors.Join(closeDB(LOG_DB), closeDB(DB)) }

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
