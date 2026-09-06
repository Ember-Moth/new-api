package twofa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TwoFA = entity.TwoFA
type TwoFABackupCode = entity.TwoFABackupCode

var ErrTwoFANotEnabled = entity.ErrTwoFANotEnabled
var ErrTwoFAAlreadyEnabled = entity.ErrTwoFAAlreadyEnabled

type Store struct {
	db             *gorm.DB
	advanceVersion func(*gorm.DB, int) (int64, error)
	publishAuth    func(int) error
}

func New(db *gorm.DB, advance func(*gorm.DB, int) (int64, error), publish func(int) error) *Store {
	return &Store{db: db, advanceVersion: advance, publishAuth: publish}
}

// GetTwoFAByUserId 根据用户ID获取2FA设置
func (r *Store) GetTwoFAByUserId(ctx context.Context, userId int) (*TwoFA, error) {
	if userId == 0 {
		return nil, errors.New("用户ID不能为空")
	}

	var twoFA TwoFA
	err := r.db.WithContext(ctx).Where("user_id = ?", userId).First(&twoFA).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // 返回nil表示未设置2FA
		}
		return nil, err
	}

	return &twoFA, nil
}

// IsTwoFAEnabled 检查用户是否启用了2FA
func (r *Store) IsTwoFAEnabled(ctx context.Context, userId int) (bool, error) {
	twoFA, err := r.GetTwoFAByUserId(ctx, userId)
	if err != nil {
		return false, err
	}
	return twoFA != nil && twoFA.IsEnabled, nil
}

// CreatePendingTwoFASetup stores a disabled factor while the user completes
// enrollment. Enabling a factor must use EnableWithAuthVersion.
func (r *Store) CreatePendingTwoFASetup(ctx context.Context, t *TwoFA) error {
	if t == nil || t.UserId <= 0 || t.IsEnabled {
		return errors.New("无效的2FA待验证设置")
	}
	// 检查用户是否已存在2FA设置
	existing, err := r.GetTwoFAByUserId(ctx, t.UserId)
	if err != nil {
		return err
	}
	if existing != nil {
		return errors.New("用户已存在2FA设置")
	}

	// 验证用户存在
	var user entity.User
	if err := r.db.WithContext(ctx).First(&user, t.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("用户不存在")
		}
		return err
	}

	return r.db.WithContext(ctx).Create(t).Error
}

func (r *Store) updateUsageState(ctx context.Context, t *TwoFA) error {
	if t.Id == 0 {
		return errors.New("2FA记录ID不能为空")
	}
	return r.db.WithContext(ctx).Model(&TwoFA{}).Where("id = ?", t.Id).Updates(map[string]interface{}{
		"failed_attempts": t.FailedAttempts,
		"locked_until":    t.LockedUntil,
		"last_used_at":    t.LastUsedAt,
	}).Error
}

// DeletePendingTwoFASetup removes only an unverified setup. Enabled factors
// must use DisableTwoFAWithAuthVersion.
func (r *Store) DeletePendingTwoFASetup(ctx context.Context, t *TwoFA) error {
	if t == nil || t.Id == 0 || t.UserId <= 0 {
		return errors.New("2FA记录ID不能为空")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFactorUser(tx, t.UserId, nil); err != nil {
			return err
		}
		var pending TwoFA
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND is_enabled = ?", t.Id, t.UserId, false).
			First(&pending).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("user_id = ?", t.UserId).Delete(&TwoFABackupCode{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&pending).Error
	})
}

// ResetFailedAttempts 重置失败尝试次数
func (r *Store) ResetFailedAttempts(ctx context.Context, t *TwoFA) error {
	t.FailedAttempts = 0
	t.LockedUntil = nil
	return r.updateUsageState(ctx, t)
}

// IncrementFailedAttempts 增加失败尝试次数
func (r *Store) IncrementFailedAttempts(ctx context.Context, t *TwoFA) error {
	if t.Id == 0 {
		return errors.New("2FA记录ID不能为空")
	}

	const maxUpdateRetries = 5
	for range maxUpdateRetries {
		var current TwoFA
		if err := r.db.WithContext(ctx).Select("id", "failed_attempts", "locked_until").First(&current, t.Id).Error; err != nil {
			return err
		}

		now := time.Now()
		if current.LockedUntil != nil && now.Before(*current.LockedUntil) {
			t.FailedAttempts = current.FailedAttempts
			t.LockedUntil = current.LockedUntil
			return nil
		}

		nextFailedAttempts := current.FailedAttempts + 1
		nextLockedUntil := current.LockedUntil
		if nextFailedAttempts >= common.MaxFailAttempts {
			lockUntil := now.Add(time.Duration(common.LockoutDuration) * time.Second)
			nextLockedUntil = &lockUntil
		}

		result := r.db.WithContext(ctx).Model(&TwoFA{}).
			Where("id = ? AND failed_attempts = ? AND (locked_until IS NULL OR locked_until <= ?)", current.Id, current.FailedAttempts, now).
			Updates(map[string]interface{}{
				"failed_attempts": nextFailedAttempts,
				"locked_until":    nextLockedUntil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}

		t.FailedAttempts = nextFailedAttempts
		t.LockedUntil = nextLockedUntil
		return nil
	}

	return errors.New("更新2FA失败次数冲突，请重试")
}

// CreatePendingTwoFASetupBackupCodes stores recovery codes for an unverified
// setup. Regeneration for an enabled factor must advance auth_version.
func (r *Store) CreatePendingTwoFASetupBackupCodes(ctx context.Context, userId int, codes []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFactorUser(tx, userId, nil); err != nil {
			return err
		}
		var pending TwoFA
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND is_enabled = ?", userId, false).First(&pending).Error; err != nil {
			return err
		}
		return replaceBackupCodesWithTx(tx, userId, codes)
	})
}

func replaceBackupCodesWithTx(tx *gorm.DB, userId int, codes []string) error {
	if err := tx.Where("user_id = ?", userId).Delete(&TwoFABackupCode{}).Error; err != nil {
		return err
	}
	for _, code := range codes {
		hashedCode, err := common.HashBackupCode(code)
		if err != nil {
			return err
		}
		if err := tx.Create(&TwoFABackupCode{UserId: userId, CodeHash: hashedCode, IsUsed: false}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReplaceBackupCodesWithAuthVersion atomically replaces the factor's recovery
// credentials and advances the user's authentication version.
func (r *Store) ReplaceBackupCodesWithAuthVersion(ctx context.Context, userId int, codes []string, checks ...func(*entity.User) error) error {
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFactorUser(tx, userId, checks); err != nil {
			return err
		}
		var enabled TwoFA
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND is_enabled = ?", userId, true).First(&enabled).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTwoFANotEnabled
			}
			return err
		}
		if _, err := r.advanceVersion(tx, userId); err != nil {
			return err
		}
		return replaceBackupCodesWithTx(tx, userId, codes)
	}); err != nil {
		return err
	}
	return r.publishAuth(userId)
}

// ValidateBackupCode 验证并使用备用码
func (r *Store) ValidateBackupCode(ctx context.Context, userId int, code string) (bool, error) {
	if !common.ValidateBackupCode(code) {
		return false, errors.New("验证码或备用码不正确")
	}

	normalizedCode := common.NormalizeBackupCode(code)

	// 查找未使用的备用码
	var backupCodes []TwoFABackupCode
	if err := r.db.WithContext(ctx).Where("user_id = ? AND is_used = false", userId).Find(&backupCodes).Error; err != nil {
		return false, err
	}

	// 验证备用码
	for _, bc := range backupCodes {
		if common.ValidatePasswordAndHash(normalizedCode, bc.CodeHash) {
			now := time.Now()
			result := r.db.WithContext(ctx).Model(&TwoFABackupCode{}).
				Where("id = ? AND is_used = ?", bc.Id, false).
				Updates(map[string]interface{}{
					"is_used": true,
					"used_at": now,
				})
			if result.Error != nil {
				return false, result.Error
			}
			return result.RowsAffected == 1, nil
		}
	}

	return false, nil
}

// GetUnusedBackupCodeCount 获取未使用的备用码数量
func (r *Store) GetUnusedBackupCodeCount(ctx context.Context, userId int) (int, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&TwoFABackupCode{}).Where("user_id = ? AND is_used = false", userId).Count(&count).Error
	return int(count), err
}

// DisableTwoFAWithAuthVersion atomically removes the factor and invalidates
// every access token issued against the previous security configuration.
func (r *Store) DisableTwoFAWithAuthVersion(ctx context.Context, userId int, checks ...func(*entity.User) error) error {
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFactorUser(tx, userId, checks); err != nil {
			return err
		}
		var twoFA TwoFA
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND is_enabled = ?", userId, true).First(&twoFA).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTwoFANotEnabled
			}
			return err
		}
		if _, err := r.advanceVersion(tx, userId); err != nil {
			return err
		}
		if err := tx.Unscoped().Where("user_id = ?", userId).Delete(&TwoFABackupCode{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&twoFA).Error
	}); err != nil {
		return err
	}
	return r.publishAuth(userId)
}

// EnableWithAuthVersion atomically enables this factor and advances the user
// authentication version so pre-enrollment sessions cannot remain valid.
func (r *Store) EnableWithAuthVersion(ctx context.Context, t *TwoFA, checks ...func(*entity.User) error) error {
	if t == nil || t.Id == 0 || t.UserId == 0 {
		return errors.New("2FA记录ID不能为空")
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFactorUser(tx, t.UserId, checks); err != nil {
			return err
		}
		var pending TwoFA
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND is_enabled = ?", t.Id, t.UserId, false).First(&pending).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTwoFAAlreadyEnabled
			}
			return err
		}
		if _, err := r.advanceVersion(tx, t.UserId); err != nil {
			return err
		}
		result := tx.Model(&pending).
			Updates(map[string]interface{}{"is_enabled": true, "failed_attempts": 0, "locked_until": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTwoFAAlreadyEnabled
		}
		return nil
	}); err != nil {
		return err
	}
	t.IsEnabled = true
	t.FailedAttempts = 0
	t.LockedUntil = nil
	return r.publishAuth(t.UserId)
}

// ValidateTOTPAndUpdateUsage 验证TOTP并更新使用记录
func (r *Store) ValidateTOTPAndUpdateUsage(ctx context.Context, t *TwoFA, code string) (bool, error) {
	// 检查是否被锁定
	if t.IsLocked() {
		return false, fmt.Errorf("账户已被锁定，请在%v后重试", t.LockedUntil.Format("2006-01-02 15:04:05"))
	}

	// 验证TOTP码
	if !common.ValidateTOTPCode(t.Secret, code) {
		// 增加失败次数
		if err := r.IncrementFailedAttempts(ctx, t); err != nil {
			common.SysLog("更新2FA失败次数失败: " + err.Error())
		}
		return false, nil
	}

	// 验证成功，重置失败次数并更新最后使用时间
	now := time.Now()
	t.FailedAttempts = 0
	t.LockedUntil = nil
	t.LastUsedAt = &now

	if err := r.updateUsageState(ctx, t); err != nil {
		common.SysLog("更新2FA使用记录失败: " + err.Error())
	}

	return true, nil
}

// ValidateBackupCodeAndUpdateUsage 验证备用码并更新使用记录
func (r *Store) ValidateBackupCodeAndUpdateUsage(ctx context.Context, t *TwoFA, code string) (bool, error) {
	// 检查是否被锁定
	if t.IsLocked() {
		return false, fmt.Errorf("账户已被锁定，请在%v后重试", t.LockedUntil.Format("2006-01-02 15:04:05"))
	}

	// 验证备用码
	valid, err := r.ValidateBackupCode(ctx, t.UserId, code)
	if err != nil {
		return false, err
	}

	if !valid {
		// 增加失败次数
		if err := r.IncrementFailedAttempts(ctx, t); err != nil {
			common.SysLog("更新2FA失败次数失败: " + err.Error())
		}
		return false, nil
	}

	// 验证成功，重置失败次数并更新最后使用时间
	now := time.Now()
	t.FailedAttempts = 0
	t.LockedUntil = nil
	t.LastUsedAt = &now

	if err := r.updateUsageState(ctx, t); err != nil {
		common.SysLog("更新2FA使用记录失败: " + err.Error())
	}

	return true, nil
}

// GetTwoFAStats 获取2FA统计信息（管理员使用）
func (r *Store) GetTwoFAStats(ctx context.Context) (map[string]interface{}, error) {
	var totalUsers, enabledUsers int64

	// 总用户数
	if err := r.db.WithContext(ctx).Model(&entity.User{}).Count(&totalUsers).Error; err != nil {
		return nil, err
	}

	// 启用2FA的用户数
	if err := r.db.WithContext(ctx).Model(&TwoFA{}).Where("is_enabled = true").Count(&enabledUsers).Error; err != nil {
		return nil, err
	}

	enabledRate := float64(0)
	if totalUsers > 0 {
		enabledRate = float64(enabledUsers) / float64(totalUsers) * 100
	}

	return map[string]interface{}{
		"total_users":   totalUsers,
		"enabled_users": enabledUsers,
		"enabled_rate":  fmt.Sprintf("%.1f%%", enabledRate),
	}, nil
}

func (r *Store) EnrollPending(ctx context.Context, factor *TwoFA, codes []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, factor.UserId).Error; err != nil {
			return err
		}
		var existing TwoFA
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", factor.UserId).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if existing.IsEnabled {
				return errors.New("用户已启用2FA，请先禁用后重新设置")
			}
			if err := tx.Unscoped().Delete(&existing).Error; err != nil {
				return err
			}
		}
		if err := tx.Unscoped().Where("user_id = ?", factor.UserId).Delete(&TwoFABackupCode{}).Error; err != nil {
			return err
		}
		if err := tx.Create(factor).Error; err != nil {
			return err
		}
		return replaceBackupCodesWithTx(tx, factor.UserId, codes)
	})
}

func lockFactorUser(tx *gorm.DB, userID int, checks []func(*entity.User) error) error {
	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "role", "status", "auth_version").First(&user, userID).Error; err != nil {
		return err
	}
	for _, check := range checks {
		if err := check(&user); err != nil {
			return err
		}
	}
	return nil
}
