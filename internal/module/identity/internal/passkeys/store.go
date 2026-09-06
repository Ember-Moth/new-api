package passkeys

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PasskeyCredential = entity.PasskeyCredential

var ErrPasskeyNotFound = entity.ErrPasskeyNotFound
var ErrFriendlyPasskeyNotFound = entity.ErrFriendlyPasskeyNotFound

type Store struct {
	db             *gorm.DB
	advanceVersion func(*gorm.DB, int) (int64, error)
	publishAuth    func(int) error
}

func NewStore(db *gorm.DB, advance func(*gorm.DB, int) (int64, error), publish func(int) error) *Store {
	return &Store{db: db, advanceVersion: advance, publishAuth: publish}
}

func (r *Store) GetPasskeyByUserID(ctx context.Context, userID int) (*PasskeyCredential, error) {
	if userID == 0 {
		common.SysLog("GetPasskeyByUserID: empty user ID")
		return nil, ErrFriendlyPasskeyNotFound
	}
	var credential PasskeyCredential
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 未找到记录是正常情况（用户未绑定），返回 ErrPasskeyNotFound 而不记录日志
			return nil, ErrPasskeyNotFound
		}
		// 只有真正的数据库错误才记录日志
		common.SysLog(fmt.Sprintf("GetPasskeyByUserID: database error for user %d: %v", userID, err))
		return nil, ErrFriendlyPasskeyNotFound
	}
	return &credential, nil
}

func (r *Store) GetPasskeyByCredentialID(ctx context.Context, credentialID []byte) (*PasskeyCredential, error) {
	if len(credentialID) == 0 {
		common.SysLog("GetPasskeyByCredentialID: empty credential ID")
		return nil, ErrFriendlyPasskeyNotFound
	}

	credIDStr := base64.StdEncoding.EncodeToString(credentialID)
	var credential PasskeyCredential
	if err := r.db.WithContext(ctx).Where("credential_id = ?", credIDStr).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.SysLog(fmt.Sprintf("GetPasskeyByCredentialID: passkey not found for credential ID length %d", len(credentialID)))
			return nil, ErrFriendlyPasskeyNotFound
		}
		common.SysLog(fmt.Sprintf("GetPasskeyByCredentialID: database error for credential ID: %v", err))
		return nil, ErrFriendlyPasskeyNotFound
	}

	return &credential, nil
}

// UpdatePasskeyAssertionState persists only fields produced by a successful
// assertion. Registration identity (credential ID, public key, AAGUID,
// transports and attestation metadata) is immutable on this path.
func (r *Store) UpdatePasskeyAssertionState(ctx context.Context, userID int, credential *webauthn.Credential, lastUsedAt time.Time) error {
	if userID <= 0 || credential == nil || len(credential.ID) == 0 || lastUsedAt.IsZero() {
		return fmt.Errorf("Passkey 保存失败，请重试")
	}
	credentialID := base64.StdEncoding.EncodeToString(credential.ID)
	result := r.db.WithContext(ctx).Model(&PasskeyCredential{}).
		Where("user_id = ? AND credential_id = ?", userID, credentialID).
		Updates(map[string]interface{}{
			"sign_count":      credential.Authenticator.SignCount,
			"clone_warning":   credential.Authenticator.CloneWarning,
			"user_present":    credential.Flags.UserPresent,
			"user_verified":   credential.Flags.UserVerified,
			"backup_eligible": credential.Flags.BackupEligible,
			"backup_state":    credential.Flags.BackupState,
			"last_used_at":    lastUsedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPasskeyNotFound
	}
	return nil
}

func upsertPasskeyCredentialWithTx(tx *gorm.DB, credential *PasskeyCredential) error {
	if err := tx.Unscoped().Where("user_id = ?", credential.UserID).Delete(&PasskeyCredential{}).Error; err != nil {
		common.SysLog(fmt.Sprintf("UpsertPasskeyCredential: failed to delete existing credential for user %d: %v", credential.UserID, err))
		return fmt.Errorf("Passkey 保存失败，请重试")
	}
	if err := tx.Create(credential).Error; err != nil {
		common.SysLog(fmt.Sprintf("UpsertPasskeyCredential: failed to create credential for user %d: %v", credential.UserID, err))
		return fmt.Errorf("Passkey 保存失败，请重试")
	}
	return nil
}

// UpsertPasskeyCredentialWithAuthVersion is reserved for enrollment changes;
// assertion sign-count updates must use UpdatePasskeyAssertionState.
func (r *Store) UpsertPasskeyCredentialWithAuthVersion(ctx context.Context, credential *PasskeyCredential, checks ...func(*entity.User) error) error {
	if credential == nil || credential.UserID <= 0 {
		return fmt.Errorf("Passkey 保存失败，请重试")
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPasskeyUser(tx, credential.UserID, checks); err != nil {
			return err
		}
		if _, err := r.advanceVersion(tx, credential.UserID); err != nil {
			return err
		}
		return upsertPasskeyCredentialWithTx(tx, credential)
	}); err != nil {
		return err
	}
	return r.publishAuth(credential.UserID)
}

func (r *Store) DeletePasskeyByUserIDWithAuthVersion(ctx context.Context, userID int, checks ...func(*entity.User) error) error {
	if userID == 0 {
		return fmt.Errorf("删除失败，请重试")
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPasskeyUser(tx, userID, checks); err != nil {
			return err
		}
		var credential PasskeyCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&credential).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPasskeyNotFound
			}
			return err
		}
		if _, err := r.advanceVersion(tx, userID); err != nil {
			return err
		}
		result := tx.Unscoped().Delete(&credential)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPasskeyNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	return r.publishAuth(userID)
}

func lockPasskeyUser(tx *gorm.DB, id int, checks []func(*entity.User) error) error {
	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "role", "status", "auth_version").First(&user, id).Error; err != nil {
		return err
	}
	for _, check := range checks {
		if err := check(&user); err != nil {
			return err
		}
	}
	return nil
}
