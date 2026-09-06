package accounting

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

func (s *Store) reserveUserQuotaDB(ctx context.Context, id int, quota int) (bool, error) {
	s.invalidateUserQuotaProjection(id)
	result := s.db.WithContext(ctx).Model(&entity.User{}).
		Where("id = ? AND quota >= ?", id, quota).
		Update("quota", gorm.Expr("quota - ?", quota))
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, gorm.ErrRecordNotFound
	}
	return false, nil
}

func (s *Store) reserveTokenQuotaDB(ctx context.Context, id int, quota int) (bool, error) {
	result := s.db.WithContext(ctx).Model(&entity.Token{}).
		Where("id = ? AND remain_quota >= ?", id, quota).
		Updates(map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota - ?", quota),
			"used_quota":    gorm.Expr("used_quota + ?", quota),
			"accessed_time": common.GetTimestamp(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&entity.Token{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, gorm.ErrRecordNotFound
	}
	return false, nil
}

// TryReserveUserQuota atomically checks and deducts a user's wallet quota in
// PostgreSQL. Redis is invalidated only after the durable reservation
// succeeds; it is a projection and never a source of funds.
func (s *Store) TryReserveUserQuota(ctx context.Context, id int, quota int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return false, err
	}
	if quota == 0 {
		return true, nil
	}
	reserved, err := s.reserveUserQuotaDB(ctx, id, quota)
	if err != nil || !reserved {
		return reserved, err
	}
	if err := s.PublishUserDelta(context.WithoutCancel(ctx), id, -int64(quota)); err != nil {
		common.SysLog("failed to invalidate reserved user quota cache: " + err.Error())
	}
	return true, nil
}

// TryReserveTokenQuota atomically checks and deducts a token quota in
// PostgreSQL. Unlimited tokens skip the balance check but still update the
// durable remain/used accounting.
func (s *Store) TryReserveTokenQuota(ctx context.Context, id int, key string, quota int, unlimited bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return false, err
	}
	if quota == 0 {
		return true, nil
	}
	if unlimited {
		return true, s.DecreaseTokenQuota(ctx, id, key, quota)
	}
	s.invalidateTokenQuotaProjection(key)
	reserved, err := s.reserveTokenQuotaDB(ctx, id, quota)
	if err != nil || !reserved {
		return reserved, err
	}
	if err := s.PublishTokenDelta(context.WithoutCancel(ctx), id, key, -int64(quota)); err != nil {
		common.SysLog("failed to invalidate reserved token quota cache: " + err.Error())
	}
	return true, nil
}
