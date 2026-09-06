package accounting

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
)

// PublishUserDelta applies a committed wallet change to a live metadata hash.
// A cold cache is left empty so its next read hydrates the committed database row.
func (s *Store) PublishUserDelta(ctx context.Context, id int, delta int64) error {
	if !s.deps.CacheEnabled() {
		return nil
	}
	_, err := s.cacheApplyUserQuotaDelta(ctx, id, delta)
	return err
}
func (s *Store) PublishTokenDelta(ctx context.Context, id int, key string, delta int64) error {
	if !s.deps.CacheEnabled() {
		return nil
	}
	_, err := s.cacheApplyTokenQuotaDelta(ctx, id, key, delta)
	return err
}

func (s *Store) IncreaseUserQuota(ctx context.Context, id, quota int, direct bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
	}
	if !direct && s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeUserQuota, id, quota)
	} else {
		result := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ? AND quota <= ?", id, common.MaxWalletQuota-quota).Update("quota", gorm.Expr("quota + ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
			return contract.ErrWalletQuotaLimitExceeded
		}
	}
	if err := s.PublishUserDelta(context.WithoutCancel(ctx), id, int64(quota)); err != nil {
		common.SysLog("failed to increase user quota cache: " + err.Error())
	}
	return nil
}

func (s *Store) DecreaseUserQuota(ctx context.Context, id, quota int, direct bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if !direct && s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeUserQuota, id, -quota)
	} else {
		if err := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Update("quota", gorm.Expr("quota - ?", quota)).Error; err != nil {
			return err
		}
	}
	if err := s.PublishUserDelta(context.WithoutCancel(ctx), id, -int64(quota)); err != nil {
		common.SysLog("failed to decrease user quota cache: " + err.Error())
	}
	return nil
}

func (s *Store) IncreaseTokenQuota(ctx context.Context, id int, key string, quota int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	return s.adjustTokenQuota(ctx, id, key, quota)
}
func (s *Store) DecreaseTokenQuota(ctx context.Context, id int, key string, quota int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	return s.adjustTokenQuota(ctx, id, key, -quota)
}
func (s *Store) adjustTokenQuota(ctx context.Context, id int, key string, delta int) error {
	if s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeTokenQuota, id, delta)
	} else {
		if err := s.db.WithContext(ctx).Model(&entity.Token{}).Where("id = ?", id).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota + ?", delta), "used_quota": gorm.Expr("used_quota - ?", delta), "accessed_time": common.GetTimestamp()}).Error; err != nil {
			return err
		}
	}
	if err := s.PublishTokenDelta(context.WithoutCancel(ctx), id, key, int64(delta)); err != nil {
		common.SysLog("failed to adjust token quota cache: " + err.Error())
	}
	return nil
}

func (s *Store) RecordUsage(ctx context.Context, id, quota, requests int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeUsedQuota, id, quota)
		if requests != 0 {
			s.addNewRecord(BatchUpdateTypeRequestCount, id, requests)
		}
		return nil
	}
	return s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", quota), "request_count": gorm.Expr("request_count + ?", requests)}).Error
}

// QueueChannelUsage joins channel accounting to the same transaction as wallet,
// token and user usage deltas. False asks the channel module to write directly.
func (s *Store) QueueChannelUsage(id, quota int) bool {
	if !s.deps.BatchEnabled() {
		return false
	}
	s.addNewRecord(BatchUpdateTypeChannelUsedQuota, id, quota)
	return true
}

func (s *Store) DeltaUpdateUserQuota(ctx context.Context, id, delta int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		return s.IncreaseUserQuota(ctx, id, delta, false)
	}
	// Negation of the minimum int is not representable.
	if -delta < 0 {
		return fmt.Errorf("invalid quota delta: %d", delta)
	}
	return s.DecreaseUserQuota(ctx, id, -delta, false)
}
