package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

// Token configuration is owned by identity; authentication and quota accounting
// continue using the shared entity until their runtime flows are migrated.
type Token = entity.Token

// InsertToken provisions the default registration token for the remaining user controller.
func InsertToken(token *Token) error {
	return identity.New(identity.Dependencies{DB: DB}).ProvisionToken(context.Background(), token)
}
func ValidateUserToken(key string) (token *Token, err error) {
	if key == "" {
		return nil, ErrTokenNotProvided
	}
	token, err = GetTokenByKey(key, false)
	if err == nil {
		if token.Status == common.TokenStatusExhausted ||
			token.Status == common.TokenStatusExpired ||
			token.Status != common.TokenStatusEnabled {
			return token, ErrTokenInvalid
		}
		if token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
			if !common.RedisEnabled {
				token.Status = common.TokenStatusExpired
				err := updateTokenStatus(token)
				if err != nil {
					common.SysLog("failed to update token status" + err.Error())
				}
			}
			return token, ErrTokenInvalid
		}
		if !token.UnlimitedQuota && token.RemainQuota <= 0 {
			if !common.RedisEnabled {
				token.Status = common.TokenStatusExhausted
				err := updateTokenStatus(token)
				if err != nil {
					common.SysLog("failed to update token status" + err.Error())
				}
			}
			return token, ErrTokenInvalid
		}
		return token, nil
	}
	common.SysLog("ValidateUserToken: failed to get token: " + err.Error())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTokenInvalid
	}
	return nil, fmt.Errorf("%w: %v", ErrDatabase, err)
}

func GetTokenById(id int) (*Token, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	token := Token{Id: id}
	var err error = nil
	err = DB.First(&token, "id = ?", id).Error
	return &token, err
}

// GetHistoricalTokenForBilling loads the immutable token identity needed by a
// task's terminal accounting event. It includes soft-deleted tokens because a
// task may finish after the credential is revoked; the persisted task still
// owns the already-authorized token ledger mutation. The lookup intentionally
// does not lock the row: ApplyAdjustmentTx acquires the token lock after the
// funding-source lock, matching the reservation lock order.
func GetHistoricalTokenForBilling(tx *gorm.DB, userID, tokenID int) (*Token, error) {
	if tx == nil {
		return nil, errors.New("token billing transaction is nil")
	}
	if userID <= 0 || tokenID <= 0 {
		return nil, errors.New("token billing identity is invalid")
	}
	var token Token
	if err := tx.Unscoped().Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func GetTokenByKey(key string, fromDB bool) (*Token, error) {
	return tokencache.New(DB).GetByKey(key, fromDB)
}
func InvalidateTokenCacheForMutation(key string) error { return tokencache.New(DB).Invalidate(key) }

func updateTokenStatus(token *Token) (err error) {
	if cacheErr := InvalidateTokenCacheForMutation(token.Key); cacheErr != nil {
		common.SysLog("failed to invalidate token cache before status update: " + cacheErr.Error())
	}
	// This can update zero values
	return DB.Model(token).Select("accessed_time", "status").Updates(token).Error
}

// InvalidateUserTokensCache 清理指定用户所有令牌在 Redis 中的缓存，
// 配合 InvalidateUserCache 使用，可在用户被禁用/删除时立即阻断其令牌的请求。
// 下一次请求将从数据库重新加载令牌及用户状态，从而立即识别出被禁用的用户。
func InvalidateUserTokensCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 {
		return errors.New("userId 无效")
	}
	var tokens []Token
	if err := DB.Unscoped().
		Select("id", commonKeyCol).
		Where("user_id = ?", userId).
		Find(&tokens).Error; err != nil {
		return err
	}
	return invalidateTokensCache(tokens)
}

func invalidateTokensCache(tokens []Token) error {
	if !common.RedisEnabled {
		return nil
	}
	var firstErr error
	for _, t := range tokens {
		if t.Key == "" {
			continue
		}
		if err := InvalidateTokenCacheForMutation(t.Key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
