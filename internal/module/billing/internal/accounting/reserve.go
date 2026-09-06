package accounting

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-redis/redis/v8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"gorm.io/gorm"
)

type cacheQuotaResult int

const (
	cacheQuotaInsufficient cacheQuotaResult = iota
	cacheQuotaOK
	cacheQuotaMiss
)

var userQuotaReserveScript = redis.NewScript(`
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or tonumber(redis.call('HGET', KEYS[1], 'CacheSchema') or '0') ~= tonumber(ARGV[3])
  or redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  return -1
end
local quota = tonumber(redis.call('HGET', KEYS[1], 'Quota'))
if quota == nil or quota < tonumber(ARGV[1]) then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'Quota', -tonumber(ARGV[1]))
return 1`)

var userQuotaDeltaScript = redis.NewScript(`
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or tonumber(redis.call('HGET', KEYS[1], 'CacheSchema') or '0') ~= tonumber(ARGV[3])
  or redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  return -1
end
redis.call('HINCRBY', KEYS[1], 'Quota', tonumber(ARGV[1]))
return 1`)

var tokenQuotaReserveScript = redis.NewScript(`
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or redis.call('HEXISTS', KEYS[1], 'RemainQuota') == 0
  or redis.call('HEXISTS', KEYS[1], 'UsedQuota') == 0 then
  return -1
end
local remain = tonumber(redis.call('HGET', KEYS[1], 'RemainQuota'))
if remain == nil or remain < tonumber(ARGV[1]) then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'RemainQuota', -tonumber(ARGV[1]))
redis.call('HINCRBY', KEYS[1], 'UsedQuota', tonumber(ARGV[1]))
redis.call('HSET', KEYS[1], 'AccessedTime', ARGV[3])
return 1`)

var tokenQuotaDeltaScript = redis.NewScript(`
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or redis.call('HEXISTS', KEYS[1], 'RemainQuota') == 0
  or redis.call('HEXISTS', KEYS[1], 'UsedQuota') == 0 then
  return -1
end
redis.call('HINCRBY', KEYS[1], 'RemainQuota', tonumber(ARGV[1]))
redis.call('HINCRBY', KEYS[1], 'UsedQuota', -tonumber(ARGV[1]))
redis.call('HSET', KEYS[1], 'AccessedTime', ARGV[3])
return 1`)

func quotaResultFromLua(result int, err error) (cacheQuotaResult, error) {
	if err != nil {
		return cacheQuotaMiss, err
	}
	switch result {
	case 1:
		return cacheQuotaOK, nil
	case 0:
		return cacheQuotaInsufficient, nil
	default:
		return cacheQuotaMiss, nil
	}
}

func (s *Store) cacheTryReserveUserQuota(ctx context.Context, userID int, amount int64) (cacheQuotaResult, error) {
	result, err := userQuotaReserveScript.Run(ctx, s.deps.Redis,
		[]string{usercache.CacheKey(userID)}, amount, userID, usercache.SchemaVersion).Int()
	return quotaResultFromLua(result, err)
}

func (s *Store) cacheApplyUserQuotaDelta(ctx context.Context, userID int, delta int64) (cacheQuotaResult, error) {
	result, err := userQuotaDeltaScript.Run(ctx, s.deps.Redis,
		[]string{usercache.CacheKey(userID)}, delta, userID, usercache.SchemaVersion).Int()
	return quotaResultFromLua(result, err)
}

func (s *Store) cacheTryReserveTokenQuota(ctx context.Context, id int, key string, amount int64) (cacheQuotaResult, error) {
	result, err := tokenQuotaReserveScript.Run(ctx, s.deps.Redis,
		[]string{tokencache.Key(key)}, amount, id, common.GetTimestamp()).Int()
	return quotaResultFromLua(result, err)
}

func (s *Store) cacheApplyTokenQuotaDelta(ctx context.Context, id int, key string, delta int64) (cacheQuotaResult, error) {
	result, err := tokenQuotaDeltaScript.Run(ctx, s.deps.Redis,
		[]string{tokencache.Key(key)}, delta, id, common.GetTimestamp()).Int()
	return quotaResultFromLua(result, err)
}

// persistUserQuotaDelta 把已在缓存侧预扣成功的增量落库；批量模式下入队，
// 直写模式下要求行存在（用户已删除时报错，交由调用方补偿缓存）。
func (s *Store) persistUserQuotaDelta(ctx context.Context, id int, delta int) error {
	if s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeUserQuota, id, delta)
		return nil
	}
	result := s.db.WithContext(ctx).Model(&entity.User{}).Where("id = ?", id).Update("quota", gorm.Expr("quota + ?", delta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) persistTokenQuotaDelta(ctx context.Context, id int, delta int) error {
	if s.deps.BatchEnabled() {
		s.addNewRecord(BatchUpdateTypeTokenQuota, id, delta)
		return nil
	}
	result := s.db.WithContext(ctx).Model(&entity.Token{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota + ?", delta),
			"used_quota":    gorm.Expr("used_quota - ?", delta),
			"accessed_time": common.GetTimestamp(),
		},
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) reserveUserQuotaDB(ctx context.Context, id int, quota int) (bool, error) {
	result := s.db.WithContext(ctx).Model(&entity.User{}).
		Where("id = ? AND quota >= ?", id, quota).
		Update("quota", gorm.Expr("quota - ?", quota))
	return result.RowsAffected == 1, result.Error
}

func (s *Store) reserveTokenQuotaDB(ctx context.Context, id int, quota int) (bool, error) {
	result := s.db.WithContext(ctx).Model(&entity.Token{}).
		Where("id = ? AND remain_quota >= ?", id, quota).
		Updates(map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota - ?", quota),
			"used_quota":    gorm.Expr("used_quota + ?", quota),
			"accessed_time": common.GetTimestamp(),
		})
	return result.RowsAffected == 1, result.Error
}

// TryReserveUserQuota atomically checks and deducts a user's wallet quota.
// 缓存命中时以缓存余额为准（避免批量模式下过期的数据库余额放大并发超扣）；
// Redis 异常或水合失败时降级为数据库条件更新，保证服务可用。
func (s *Store) TryReserveUserQuota(ctx context.Context, id int, quota int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if quota == 0 {
		return true, nil
	}
	if !s.deps.CacheEnabled() {
		return s.reserveUserQuotaDB(ctx, id, quota)
	}

	result, err := s.cacheTryReserveUserQuota(ctx, id, int64(quota))
	if err == nil && result == cacheQuotaMiss {
		if _, hydrateErr := usercache.New(s.db).GetUserCache(id); hydrateErr == nil {
			result, err = s.cacheTryReserveUserQuota(ctx, id, int64(quota))
		}
	}
	if err != nil || result == cacheQuotaMiss {
		if err != nil {
			common.SysLog("user quota cache reserve unavailable, falling back to database: " + err.Error())
		}
		return s.reserveUserQuotaDB(ctx, id, quota)
	}
	if result == cacheQuotaInsufficient {
		return false, nil
	}
	if err = s.persistUserQuotaDelta(ctx, id, -quota); err != nil {
		compensated, compensateErr := s.cacheApplyUserQuotaDelta(context.WithoutCancel(ctx), id, int64(quota))
		if compensateErr != nil || compensated != cacheQuotaOK {
			common.SysError(fmt.Sprintf("failed to compensate reserved user quota: result=%d error=%v", compensated, compensateErr))
		}
		return false, err
	}
	return true, nil
}

// TryReserveTokenQuota atomically checks and deducts a token quota. Unlimited
// tokens skip the balance check but still update remain/used accounting.
func (s *Store) TryReserveTokenQuota(ctx context.Context, id int, key string, quota int, unlimited bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if quota == 0 {
		return true, nil
	}
	if unlimited {
		return true, s.DecreaseTokenQuota(ctx, id, key, quota)
	}
	if !s.deps.CacheEnabled() {
		return s.reserveTokenQuotaDB(ctx, id, quota)
	}

	result, err := s.cacheTryReserveTokenQuota(ctx, id, key, int64(quota))
	if err == nil && result == cacheQuotaMiss {
		if _, hydrateErr := tokencache.New(s.db).GetByKey(key, true); hydrateErr == nil {
			result, err = s.cacheTryReserveTokenQuota(ctx, id, key, int64(quota))
		}
	}
	if err != nil || result == cacheQuotaMiss {
		if err != nil {
			common.SysLog("token quota cache reserve unavailable, falling back to database: " + err.Error())
		}
		return s.reserveTokenQuotaDB(ctx, id, quota)
	}
	if result == cacheQuotaInsufficient {
		return false, nil
	}
	if err = s.persistTokenQuotaDelta(ctx, id, -quota); err != nil {
		compensated, compensateErr := s.cacheApplyTokenQuotaDelta(context.WithoutCancel(ctx), id, key, int64(quota))
		if compensateErr != nil || compensated != cacheQuotaOK {
			common.SysError(fmt.Sprintf("failed to compensate reserved token quota: result=%d error=%v", compensated, compensateErr))
		}
		return false, err
	}
	return true, nil
}
