package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	postgresRuntimeEventChannel = "new_api_runtime_events"

	postgresRuntimeEventOptions  = "options"
	postgresRuntimeEventChannels = "channels"
)

type RuntimeRateLimit struct {
	Key         string `gorm:"column:key;primaryKey"`
	Count       int    `gorm:"column:count"`
	WindowStart int64  `gorm:"column:window_start"`
	ExpiresAt   int64  `gorm:"column:expires_at"`
	UpdatedAt   int64  `gorm:"column:updated_at"`
}

func (RuntimeRateLimit) TableName() string {
	return "runtime_rate_limits"
}

type VerificationCode struct {
	Purpose   string `gorm:"column:purpose;primaryKey"`
	Key       string `gorm:"column:key;primaryKey"`
	Code      string `gorm:"column:code"`
	CreatedAt int64  `gorm:"column:created_at"`
	ExpiresAt int64  `gorm:"column:expires_at"`
}

func (VerificationCode) TableName() string {
	return "verification_codes"
}

type PostgresVerificationCodeStore struct{}

func InitPostgresRuntimeState() {
	common.SetVerificationCodeStore(PostgresVerificationCodeStore{})
}

func (PostgresVerificationCodeStore) RegisterVerificationCodeWithKey(key string, code string, purpose string) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	now := common.GetTimestamp()
	row := VerificationCode{
		Purpose:   purpose,
		Key:       key,
		Code:      code,
		CreatedAt: now,
		ExpiresAt: now + int64(common.VerificationValidMinutes*60),
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "purpose"}, {Name: "key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"code":       row.Code,
			"created_at": row.CreatedAt,
			"expires_at": row.ExpiresAt,
		}),
	}).Create(&row).Error
}

func (PostgresVerificationCodeStore) VerifyCodeWithKey(key string, code string, purpose string) (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var count int64
	err := DB.Model(&VerificationCode{}).
		Where("purpose = ? AND "+commonKeyCol+" = ? AND code = ? AND expires_at > ?", purpose, key, code, common.GetTimestamp()).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	cleanupExpiredRuntimeStateAsync()
	return count > 0, nil
}

func (PostgresVerificationCodeStore) DeleteKey(key string, purpose string) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Where("purpose = ? AND "+commonKeyCol+" = ?", purpose, key).Delete(&VerificationCode{}).Error
}

func AllowPostgresFixedWindowRateLimit(key string, maxCount int, durationSeconds int64) (bool, int64, error) {
	return applyPostgresFixedWindowRateLimit(key, maxCount, durationSeconds, true)
}

func CheckPostgresFixedWindowRateLimit(key string, maxCount int, durationSeconds int64) (bool, int64, error) {
	return applyPostgresFixedWindowRateLimit(key, maxCount, durationSeconds, false)
}

func applyPostgresFixedWindowRateLimit(key string, maxCount int, durationSeconds int64, increment bool) (bool, int64, error) {
	if maxCount <= 0 {
		return true, 0, nil
	}
	if DB == nil {
		return false, 0, errors.New("database is not initialized")
	}
	if durationSeconds <= 0 {
		durationSeconds = 1
	}
	now := common.GetTimestamp()
	expiresAt := now + durationSeconds
	allowed := true
	var retryAfter int64

	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = DB.Transaction(func(tx *gorm.DB) error {
			var row RuntimeRateLimit
			err := lockForUpdate(tx).Where(commonKeyCol+" = ?", key).First(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if !increment {
					return nil
				}
				row = RuntimeRateLimit{
					Key:         key,
					Count:       1,
					WindowStart: now,
					ExpiresAt:   expiresAt,
					UpdatedAt:   now,
				}
				return tx.Create(&row).Error
			}
			if err != nil {
				return err
			}
			if row.ExpiresAt <= now {
				if !increment {
					return nil
				}
				return tx.Model(&RuntimeRateLimit{}).Where(commonKeyCol+" = ?", key).Updates(map[string]interface{}{
					"count":        1,
					"window_start": now,
					"expires_at":   expiresAt,
					"updated_at":   now,
				}).Error
			}
			if row.Count >= maxCount {
				allowed = false
				retryAfter = row.ExpiresAt - now
				if retryAfter < 1 {
					retryAfter = 1
				}
				return nil
			}
			if !increment {
				return nil
			}
			return tx.Model(&RuntimeRateLimit{}).Where(commonKeyCol+" = ?", key).Updates(map[string]interface{}{
				"count":      gorm.Expr(`"count" + 1`),
				"updated_at": now,
			}).Error
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate key") {
			break
		}
	}
	if err != nil {
		return false, 0, err
	}
	cleanupExpiredRuntimeStateAsync()
	return allowed, retryAfter, nil
}

var runtimeStateCleanupUnix atomic.Int64

func cleanupExpiredRuntimeStateAsync() {
	now := common.GetTimestamp()
	last := runtimeStateCleanupUnix.Load()
	if now-last < 300 || !runtimeStateCleanupUnix.CompareAndSwap(last, now) {
		return
	}
	gopool.Go(func() {
		cutoff := common.GetTimestamp()
		if err := DB.Where("expires_at < ?", cutoff).Delete(&RuntimeRateLimit{}).Error; err != nil {
			common.SysError("failed to cleanup runtime rate limits: " + err.Error())
		}
		if err := DB.Where("expires_at < ?", cutoff).Delete(&VerificationCode{}).Error; err != nil {
			common.SysError("failed to cleanup verification codes: " + err.Error())
		}
	})
}

type PostgresAdvisoryLock struct {
	conn     *sql.Conn
	name     string
	released atomic.Bool
}

func TryPostgresAdvisoryLock(ctx context.Context, name string) (*PostgresAdvisoryLock, bool, error) {
	if DB == nil {
		return nil, false, errors.New("database is not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, errors.New("advisory lock name is empty")
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return nil, false, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", name).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !locked {
		_ = conn.Close()
		return nil, false, nil
	}
	return &PostgresAdvisoryLock{conn: conn, name: name}, true, nil
}

func AcquirePostgresAdvisoryLock(ctx context.Context, name string) (*PostgresAdvisoryLock, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("advisory lock name is empty")
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return nil, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", name); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &PostgresAdvisoryLock{conn: conn, name: name}, nil
}

func (l *PostgresAdvisoryLock) Unlock() error {
	if l == nil || l.conn == nil {
		return nil
	}
	if !l.released.CompareAndSwap(false, true) {
		return nil
	}
	var unlocked bool
	err := l.conn.QueryRowContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", l.name).Scan(&unlocked)
	closeErr := l.conn.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !unlocked {
		return fmt.Errorf("advisory lock %q was not held", l.name)
	}
	return nil
}

func WithPostgresAdvisoryLock(ctx context.Context, name string, fn func(context.Context) error) error {
	lock, ok, err := TryPostgresAdvisoryLock(ctx, name)
	if err != nil || !ok {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			common.SysError("failed to release PostgreSQL advisory lock: " + unlockErr.Error())
		}
	}()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func WithBlockingPostgresAdvisoryLock(ctx context.Context, name string, fn func(context.Context) error) error {
	lock, err := AcquirePostgresAdvisoryLock(ctx, name)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			common.SysError("failed to release PostgreSQL advisory lock: " + unlockErr.Error())
		}
	}()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

var runtimeEventListenerOnce sync.Once

func StartPostgresRuntimeEventListener() {
	runtimeEventListenerOnce.Do(func() {
		dsn := strings.TrimSpace(os.Getenv("SQL_DSN"))
		if dsn == "" {
			common.SysError("SQL_DSN is empty, PostgreSQL runtime event listener disabled")
			return
		}
		gopool.Go(func() {
			for {
				if err := runPostgresRuntimeEventListener(context.Background(), dsn); err != nil {
					common.SysError("PostgreSQL runtime event listener stopped: " + err.Error())
				}
				time.Sleep(5 * time.Second)
			}
		})
	})
}

func runPostgresRuntimeEventListener(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "LISTEN "+postgresRuntimeEventChannel); err != nil {
		return err
	}
	common.SysLog("PostgreSQL runtime event listener started")
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		handlePostgresRuntimeEvent(notification.Payload)
	}
}

func handlePostgresRuntimeEvent(payload string) {
	switch payload {
	case postgresRuntimeEventOptions:
		loadOptionsFromDatabase()
	case postgresRuntimeEventChannels:
		if common.MemoryCacheEnabled {
			InitChannelCache()
		}
	default:
		if payload != "" {
			common.SysLog("ignored unknown PostgreSQL runtime event: " + payload)
		}
	}
}
