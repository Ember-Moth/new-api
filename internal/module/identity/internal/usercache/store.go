package usercache

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type User = entity.User
type UserBase = entity.UserBase

const userCacheSchemaVersion = entity.UserCacheSchemaVersion

type Store struct{ db *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{db: db} }
func (r *Store) user(id int) (*User, error) {
	var user User
	if err := r.db.Omit("password", "access_token").First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// getUserCacheKey returns the key for user cache
func getUserCacheKey(userId int) string {
	return fmt.Sprintf("user:%d", userId)
}

func userCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// InvalidateUserCache clears user cache
func (r *Store) InvalidateUserCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	return common.RedisDelKey(getUserCacheKey(userId))
}

func (r *Store) populateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return r.writeUserCache(user.ToBaseUser(), true)
}

// updateUserCache refreshes non-quota user cache fields.
// Quota is maintained by atomic quota delta paths and must not be overwritten
// by stale user snapshots from profile/settings updates.
func (r *Store) updateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return r.writeUserCache(user.ToBaseUser(), false)
}

// GetUserCache gets complete user cache from hash
func (r *Store) GetUserCache(userId int) (*UserBase, error) {
	// Try getting from Redis first
	userCache, err := r.cacheGetUserBase(userId)
	if err == nil {
		return userCache, nil
	}

	// Redis misses and read failures both fall back to the shared database. A
	// version fence newer than the database is the one exception: allowing that
	// snapshot would re-authorize a user while a restrictive update is pending.
	user, err := r.user(userId)
	if err != nil {
		return nil, err
	}
	if common.RedisEnabled {
		floor, floorErr := r.getUserAuthVersionFloor(userId)
		if floorErr == nil && floor > user.AuthVersion {
			return nil, ErrUserAuthCachePending
		}
		if err := r.populateUserCache(*user); err != nil {
			if errors.Is(err, ErrUserAuthCachePending) {
				return nil, err
			}
			common.SysLog("failed to synchronously populate user cache: " + err.Error())
		}
	}
	return user.ToBaseUser(), nil
}

func (r *Store) cacheGetUserBase(userId int) (*UserBase, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var userCache UserBase
	// Try getting from Redis first
	err := common.RedisHGetObj(getUserCacheKey(userId), &userCache)
	if err != nil {
		return nil, err
	}
	if userCache.Id != userId || userCache.CacheSchema != userCacheSchemaVersion || userCache.AuthVersion <= 0 {
		return nil, fmt.Errorf("user cache schema is stale")
	}
	floor, err := r.getUserAuthVersionFloor(userId)
	if err != nil {
		return nil, err
	}
	if floor > userCache.AuthVersion {
		return nil, ErrUserAuthCachePending
	}
	return &userCache, nil
}

// RefreshUserGroupCache writes the database-authoritative group into an
// existing user hash without changing the user's authentication version.
func (r *Store) RefreshUserGroupCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 {
		return fmt.Errorf("invalid user id")
	}
	var authoritative User
	if err := r.db.Select("id", "auth_version", `"group"`).Where("id = ?", userId).First(&authoritative).Error; err != nil {
		return err
	}
	// Group transitions intentionally keep the same authentication version. A
	// refresh that read the previous group can therefore arrive after a newer
	// refresh and still pass the auth-version fence. Re-read after every write
	// and repair the cache when the authoritative group changed in between.
	for range 3 {
		if err := r.updateUserCacheFieldAtVersion(userId, "Group", authoritative.Group, authoritative.AuthVersion); err != nil {
			return err
		}

		var verified User
		if err := r.db.Select("id", "auth_version", `"group"`).Where("id = ?", userId).First(&verified).Error; err != nil {
			return err
		}
		if verified.AuthVersion == authoritative.AuthVersion && verified.Group == authoritative.Group {
			return nil
		}
		authoritative = verified
	}

	// Preserve the freshest snapshot observed even when the row was too busy to
	// stabilize within the bounded retries. Returning an error lets best-effort
	// callers emit an operation-specific warning.
	if err := r.updateUserCacheFieldAtVersion(userId, "Group", authoritative.Group, authoritative.AuthVersion); err != nil {
		return err
	}
	return fmt.Errorf("user group changed repeatedly during cache refresh")
}

// updateUserCacheField prevents individual cache refreshes from bypassing the
// auth-version fence. It intentionally does nothing when the complete hash is
// absent; the next GetUserCache call will repopulate it from the database.
func (r *Store) updateUserCacheField(userId int, field string, value interface{}) error {
	if !common.RedisEnabled {
		return nil
	}
	var user User
	if err := r.db.Select("id", "auth_version").Where("id = ?", userId).First(&user).Error; err != nil {
		return err
	}
	if user.AuthVersion <= 0 {
		return fmt.Errorf("invalid user auth version")
	}
	return r.updateUserCacheFieldAtVersion(userId, field, value, user.AuthVersion)
}

var ErrUserAuthCachePending = errors.New("user authentication state update is pending")

var ErrUserAuthVersionConflict = errors.New("user authentication version update conflicted")

func getUserAuthFenceKey(userId int) string {
	return fmt.Sprintf("auth:user:fence:%d", userId)
}

func getUserAuthVersionKey(userId int) string {
	return fmt.Sprintf("auth:user:version:%d", userId)
}

// A pending fence only covers the interval between publishing the next
// version and the surrounding database transaction reaching a decision. Its
// TTL must outlive every user hash that could have been populated before the
// fence, while still allowing an automatically rolled-back transaction to
// recover without an operator repairing Redis.
func userAuthFenceTTLSeconds() int {
	cacheTTL := userCacheTTLSeconds()
	extra := cacheTTL
	if extra < 60 {
		extra = 60
	}
	return cacheTTL + extra
}

func (r *Store) writeUserCache(user *UserBase, includeQuota bool) error {
	if user == nil || user.Id <= 0 || !common.RedisEnabled {
		return nil
	}
	user.CacheSchema = userCacheSchemaVersion
	if user.AuthVersion <= 0 {
		return fmt.Errorf("invalid user auth version")
	}
	includeQuotaArg := "0"
	if includeQuota {
		includeQuotaArg = "1"
	}
	ttl := userCacheTTLSeconds()

	result, err := writeUserCacheScript.Run(context.Background(), common.RDB,
		[]string{getUserCacheKey(user.Id), getUserAuthFenceKey(user.Id), getUserAuthVersionKey(user.Id)},
		user.AuthVersion, user.Id, user.Group, user.Email, user.Status, user.Role,
		user.Username, user.Setting, user.CacheSchema, includeQuotaArg, user.Quota, ttl,
	).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrUserAuthCachePending
	}
	return nil
}

func (r *Store) getUserAuthVersionFloor(userId int) (int64, error) {
	if !common.RedisEnabled {
		return 0, nil
	}
	values, err := common.RDB.MGet(context.Background(), getUserAuthFenceKey(userId), getUserAuthVersionKey(userId)).Result()
	if err != nil {
		return 0, err
	}
	var floor int64
	for _, value := range values {
		if value == nil {
			continue
		}
		parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return 0, err
		}
		if parsed > floor {
			floor = parsed
		}
	}
	return floor, nil
}

// SetUserAuthVersionFence publishes a fail-closed version before a restrictive
// database update. Pending fences expire only after every pre-existing user
// hash must have expired; a committed update is promoted separately to a
// permanent monotonic version floor.
func (r *Store) SetUserAuthVersionFence(userId int, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid user auth fence")
	}

	return setUserAuthVersionFenceScript.Run(context.Background(), common.RDB, []string{getUserAuthFenceKey(userId)}, authVersion, userAuthFenceTTLSeconds()).Err()
}

// PublishCommittedUserAuthVersion records the durable lower bound used to
// reject an arbitrarily delayed cache fill after a committed security change.
// It also removes this transaction's now-obsolete pending fence.
func (r *Store) PublishCommittedUserAuthVersion(userId int, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid committed user auth version")
	}

	return publishCommittedUserAuthVersionScript.Run(context.Background(), common.RDB,
		[]string{getUserAuthVersionKey(userId), getUserAuthFenceKey(userId)}, authVersion,
	).Err()
}

// IncrementUserAuthVersionWithTx locks the user, publishes the next deny
// fence, then persists the version in the caller's transaction. Unscoped is
// intentional so the same fail-closed path also covers hard deletion of an
// already soft-deleted user.
func (r *Store) IncrementUserAuthVersionWithTx(tx *gorm.DB, userId int) (int64, error) {
	if tx == nil || userId <= 0 {
		return 0, fmt.Errorf("invalid user auth version update")
	}
	for range 3 {
		var user User
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "auth_version").Where("id = ?", userId).First(&user).Error; err != nil {
			return 0, err
		}
		current := user.AuthVersion
		if current < 1 {
			current = 1
		}
		next := current + 1
		if err := r.SetUserAuthVersionFence(userId, next); err != nil {
			return 0, err
		}
		result := tx.Unscoped().Model(&User{}).
			Where("id = ? AND auth_version = ?", userId, user.AuthVersion).
			Update("auth_version", next)
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 1 {
			return next, nil
		}
	}
	return 0, ErrUserAuthVersionConflict
}

// BumpUserAuthVersion is the transaction-owning variant used by password,
// role, status and security-factor changes outside another transaction.
func (r *Store) BumpUserAuthVersion(userId int) (int64, error) {
	var next int64
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		next, err = r.IncrementUserAuthVersionWithTx(tx, userId)
		return err
	}); err != nil {
		return 0, err
	}
	if err := r.PublishUserAuthCache(userId); err != nil {
		return next, err
	}
	return next, nil
}

// PublishUserAuthCache refreshes the current database state after a successful
// auth-sensitive transaction without touching the cached quota field.
func (r *Store) PublishUserAuthCache(userId int) error {
	user, err := r.user(userId)
	if err != nil {
		return err
	}
	return r.updateUserCache(*user)
}

func (r *Store) updateUserCacheFieldAtVersion(userId int, field string, value interface{}, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid user auth version")
	}

	result, err := updateUserCacheFieldAtVersionScript.Run(context.Background(), common.RDB,
		[]string{getUserCacheKey(userId), getUserAuthFenceKey(userId), getUserAuthVersionKey(userId)},
		authVersion, field, value, userCacheSchemaVersion,
	).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrUserAuthCachePending
	}
	return nil
}

var writeUserCacheScript = redis.NewScript(`
local incoming = tonumber(ARGV[1])
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
local committed = tonumber(redis.call('GET', KEYS[3]) or '0')
local current = tonumber(redis.call('HGET', KEYS[1], 'AuthVersion') or '0')
if pending > incoming or committed > incoming or current > incoming then
  return 0
end
if committed < incoming then
  redis.call('SET', KEYS[3], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
if ARGV[10] == '0' and redis.call('EXISTS', KEYS[1]) == 0 then
  return 1
end
redis.call('HSET', KEYS[1],
  'Id', ARGV[2], 'Group', ARGV[3], 'Email', ARGV[4],
  'Status', ARGV[5], 'Role', ARGV[6], 'Username', ARGV[7],
  'Setting', ARGV[8], 'AuthVersion', ARGV[1], 'CacheSchema', ARGV[9])
if ARGV[10] == '1' and redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  redis.call('HSET', KEYS[1], 'Quota', ARGV[11])
end
redis.call('EXPIRE', KEYS[1], ARGV[12])
return 1`)

var setUserAuthVersionFenceScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local incoming = tonumber(ARGV[1])
if current < incoming then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
elseif current == incoming then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
elseif redis.call('TTL', KEYS[1]) < 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return 1`)

var publishCommittedUserAuthVersionScript = redis.NewScript(`
local incoming = tonumber(ARGV[1])
local committed = tonumber(redis.call('GET', KEYS[1]) or '0')
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
if committed < incoming then
  redis.call('SET', KEYS[1], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
return 1`)

var updateUserCacheFieldAtVersionScript = redis.NewScript(`
local incoming = tonumber(ARGV[1])
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
local committed = tonumber(redis.call('GET', KEYS[3]) or '0')
local current = tonumber(redis.call('HGET', KEYS[1], 'AuthVersion') or '0')
if pending > incoming or committed > incoming or current > incoming then
  return 0
end
if committed < incoming then
  redis.call('SET', KEYS[3], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 1
end
if current ~= incoming then
  return 1
end
redis.call('HSET', KEYS[1], ARGV[2], ARGV[3], 'CacheSchema', ARGV[4])
return 1`)

func (r *Store) Populate(user User) error             { return r.populateUserCache(user) }
func (r *Store) Publish(user User) error              { return r.updateUserCache(user) }
func (r *Store) Cached(userID int) (*UserBase, error) { return r.cacheGetUserBase(userID) }
func (r *Store) UpdateField(userID int, field string, value any) error {
	return r.updateUserCacheField(userID, field, value)
}
func CacheKey(userID int) string   { return getUserCacheKey(userID) }
func FenceKey(userID int) string   { return getUserAuthFenceKey(userID) }
func VersionKey(userID int) string { return getUserAuthVersionKey(userID) }
func CacheTTLSeconds() int         { return userCacheTTLSeconds() }
