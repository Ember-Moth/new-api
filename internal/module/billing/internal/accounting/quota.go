package accounting

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

// PublishUserDelta invalidates a cache projection after a committed wallet
// change. Applying a delta is unsafe here: a concurrent cold-cache fill may
// already contain the new database value, which would make HINCRBY count the
// same mutation twice. The next read hydrates the authoritative balance.
func (s *Store) PublishUserDelta(ctx context.Context, id int, delta int64) error {
	if s.deferCache {
		return nil
	}
	if !s.deps.CacheEnabled() {
		return nil
	}
	return usercache.New(s.db).InvalidateUserCache(id)
}
func (s *Store) PublishTokenDelta(ctx context.Context, id int, key string, delta int64) error {
	if s.deferCache {
		return nil
	}
	if !s.deps.CacheEnabled() {
		return nil
	}
	return tokencache.New(s.db).Invalidate(key)
}

// PublishTokenProjectionByID invalidates the cache under the token's current
// key. Lifecycle settlement may run after key rotation, so callers must not
// reuse an old request key for cache publication.
func (s *Store) PublishTokenProjectionByID(ctx context.Context, id int) error {
	if s.deferCache || !s.deps.CacheEnabled() || id <= 0 {
		return nil
	}
	var token entity.Token
	if err := s.db.Unscoped().Select("id", "key").Where("id = ?", id).First(&token).Error; err != nil {
		return err
	}
	return tokencache.New(s.db).Invalidate(token.Key)
}

func (s *Store) invalidateUserQuotaProjection(id int) {
	if s.deferCache {
		return
	}
	if !s.deps.CacheEnabled() {
		return
	}
	if err := usercache.New(s.db).InvalidateUserCache(id); err != nil {
		common.SysLog("failed to invalidate user quota cache before balance write: " + err.Error())
	}
}

func (s *Store) invalidateTokenQuotaProjection(key string) {
	if s.deferCache {
		return
	}
	if !s.deps.CacheEnabled() {
		return
	}
	if err := tokencache.New(s.db).Invalidate(key); err != nil {
		common.SysLog("failed to invalidate token quota cache before balance write: " + err.Error())
	}
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
	// The direct parameter is retained for the legacy adapter. Wallet balances
	// are always committed to PostgreSQL; only statistics use the batch outbox.
	s.invalidateUserQuotaProjection(id)
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
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
	}
	// A debit is also a durable balance mutation. Do not enqueue it behind a
	// best-effort cache update, because a process exit between those steps would
	// turn a successful reservation into an unrecorded debit.
	s.invalidateUserQuotaProjection(id)
	result := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Update("quota", gorm.Expr("quota - ?", quota))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
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
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
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
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
	}
	return s.adjustTokenQuota(ctx, id, key, -quota)
}
func (s *Store) adjustTokenQuota(ctx context.Context, id int, key string, delta int) error {
	s.invalidateTokenQuotaProjection(key)
	tokenDB := s.db.WithContext(ctx)
	if s.historicalToken {
		tokenDB = tokenDB.Unscoped()
	}
	result := tokenDB.Model(&entity.Token{}).Where("id = ?", id).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota + ?", delta), "used_quota": gorm.Expr("used_quota - ?", delta), "accessed_time": common.GetTimestamp()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	if err := s.PublishTokenDelta(context.WithoutCancel(ctx), id, key, int64(delta)); err != nil {
		common.SysLog("failed to adjust token quota cache: " + err.Error())
	}
	return nil
}

func (s *Store) RecordUsage(ctx context.Context, id, quota, requests int) error {
	return s.RecordUsageTx(ctx, s.db, id, quota, requests)
}

// RecordUsageTx writes usage statistics or their durable batch intents in the
// caller's transaction. This keeps request statistics with a session's final
// funding and token mutation.
func (s *Store) RecordUsageTx(ctx context.Context, tx *gorm.DB, id, quota, requests int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return errors.New("accounting transaction is nil")
	}
	if s.deps.BatchEnabled() {
		deltas := make([]quotaBatchDelta, 0, 2)
		if quota != 0 {
			deltas = append(deltas, quotaBatchDelta{UpdateType: BatchUpdateTypeUsedQuota, EntityID: id, Delta: quota})
		}
		if requests != 0 {
			deltas = append(deltas, quotaBatchDelta{UpdateType: BatchUpdateTypeRequestCount, EntityID: id, Delta: requests})
		}
		return s.enqueueBatchDeltasTx(ctx, tx, deltas)
	}
	return tx.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", quota), "request_count": gorm.Expr("request_count + ?", requests)}).Error
}

// RecordChannelUsage persists channel usage. Batch mode places the statistic
// in the durable outbox; otherwise it is updated directly in PostgreSQL. Both
// branches return write errors so callers never retry through a second path.
func (s *Store) RecordChannelUsage(ctx context.Context, id, quota int) error {
	return s.RecordChannelUsageTx(ctx, s.db, id, quota)
}

// RecordChannelUsageTx is the transaction-bound channel statistic writer.
func (s *Store) RecordChannelUsageTx(ctx context.Context, tx *gorm.DB, id, quota int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return errors.New("accounting transaction is nil")
	}
	if quota == 0 {
		return nil
	}
	if s.deps.BatchEnabled() {
		return s.enqueueBatchDeltasTx(ctx, tx, []quotaBatchDelta{{UpdateType: BatchUpdateTypeChannelUsedQuota, EntityID: id, Delta: quota}})
	}
	result := tx.WithContext(ctx).Table("channels").Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
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
