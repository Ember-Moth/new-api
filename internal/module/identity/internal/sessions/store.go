package sessions

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	identitycontract "github.com/QuantumNous/new-api/internal/module/identity/contract"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type Store struct {
	db    *gorm.DB
	cache *redis.Client
}

func New(db *gorm.DB, cache *redis.Client) *Store { return &Store{db: db, cache: cache} }

type UserSession = identityentity.UserSession
type User = identityentity.User

const (
	UserSessionStatusActive  = "active"
	UserSessionStatusRevoked = "revoked"
	userSessionListLimit     = 100
)

var (
	ErrUserSessionInvalid        = identitycontract.ErrUserSessionInvalid
	ErrUserSessionInactive       = identitycontract.ErrUserSessionInactive
	ErrUserSessionRefreshInvalid = identitycontract.ErrUserSessionRefreshInvalid
	ErrUserSessionRefreshRace    = identitycontract.ErrUserSessionRefreshRace
	ErrUserSessionRefreshReuse   = identitycontract.ErrUserSessionRefreshReuse
	ErrUserSessionLimit          = identitycontract.ErrUserSessionLimit
	ErrUserSessionIssuanceLimit  = identitycontract.ErrUserSessionIssuanceLimit
)

func userSessionCacheKey(sid string) string {
	return "auth:session:" + common.GenerateHMACWithKey([]byte("user-session-cache-v1:"+common.SessionSecret), sid)
}
func userSessionIndex(userID int, kind string) string {
	return "auth:sessions:" + strconv.Itoa(userID) + ":" + kind
}

var createSession = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return -1 end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', tonumber(ARGV[1])-tonumber(ARGV[5]))
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', tonumber(ARGV[1])-tonumber(ARGV[5]))
if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[2]) then return -2 end
if redis.call('ZCOUNT', KEYS[3], '('..ARGV[4], '+inf') >= tonumber(ARGV[3]) then return -3 end
for i=10,#ARGV,2 do redis.call('HSET', KEYS[1], ARGV[i], ARGV[i+1]) end
redis.call('PEXPIRE', KEYS[1], ARGV[6])
redis.call('ZADD', KEYS[2], ARGV[9], ARGV[7])
if redis.call('PTTL', KEYS[2]) < tonumber(ARGV[6]) then redis.call('PEXPIRE', KEYS[2], ARGV[6]) end
redis.call('ZADD', KEYS[3], ARGV[8], ARGV[7])
redis.call('ZADD', KEYS[4], ARGV[8], ARGV[7])
redis.call('EXPIRE', KEYS[3], ARGV[5])
redis.call('EXPIRE', KEYS[4], ARGV[5])
return 1
`)

func (r *Store) CreateUserSession(s *UserSession) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for login sessions")
	}
	now := time.Now().Unix()
	if s == nil || s.SID == "" || s.UserID <= 0 || s.UserAuthVersion <= 0 || s.RefreshHash == "" || s.ExpiresAt <= now || common.UserSessionActiveLimit <= 0 || common.UserSessionIssuanceLimit <= 0 || common.UserSessionIssuanceWindowSeconds <= 0 {
		return ErrUserSessionInvalid
	}
	if s.Version <= 0 {
		s.Version = 1
	}
	if s.Status == "" {
		s.Status = UserSessionStatusActive
	}
	if s.Status != UserSessionStatusActive || s.RevokedAt != 0 {
		return ErrUserSessionInvalid
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = now
	}
	if s.LastActiveAt == 0 {
		s.LastActiveAt = now
	}
	ttl := time.Until(time.Unix(s.ExpiresAt, 0))
	if ttl < time.Millisecond {
		return ErrUserSessionInvalid
	}
	args := []any{now, common.UserSessionActiveLimit, common.UserSessionIssuanceLimit, now - common.UserSessionIssuanceWindowSeconds, max(common.UserSessionIssuanceWindowSeconds, int64(max(common.UserSessionRevokedRetentionDays, 1))*86400, 3600), ttl.Milliseconds(), s.SID, s.CreatedAt, s.ExpiresAt,
		"SID", s.SID, "UserID", s.UserID, "Version", s.Version, "UserAuthVersion", s.UserAuthVersion, "Status", s.Status, "RefreshHash", s.RefreshHash, "PreviousRefreshHash", s.PreviousRefreshHash, "PreviousValidUntil", s.PreviousValidUntil,
		"LoginMethod", s.LoginMethod, "IP", s.IP, "UserAgent", s.UserAgent, "CreatedAt", s.CreatedAt, "LastActiveAt", s.LastActiveAt, "ExpiresAt", s.ExpiresAt, "RevokedAt", s.RevokedAt, "RevokedReason", s.RevokedReason}
	code, err := createSession.Run(context.Background(), r.cache, []string{userSessionCacheKey(s.SID), userSessionIndex(s.UserID, "active"), userSessionIndex(s.UserID, "issued"), userSessionIndex(0, "issued")}, args...).Int()
	if err != nil {
		return err
	}
	switch code {
	case 1:
		return nil
	case -2:
		return ErrUserSessionLimit
	case -3:
		return ErrUserSessionIssuanceLimit
	default:
		return ErrUserSessionInvalid
	}
}

func (r *Store) CountActiveUserSessions(userID int, now int64) (int64, error) {
	if r.cache == nil {
		return 0, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 {
		return 0, ErrUserSessionInvalid
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	return r.cache.ZCount(context.Background(), userSessionIndex(userID, "active"), "("+strconv.FormatInt(now, 10), "+inf").Result()
}
func (r *Store) CountUserSessionsCreatedSince(userID int, createdAfter int64) (int64, error) {
	return r.CountUserSessionsCreatedSinceWithContext(context.Background(), userID, createdAfter)
}

func (r *Store) CountUserSessionsCreatedSinceWithContext(ctx context.Context, userID int, createdAfter int64) (int64, error) {
	if r.cache == nil {
		return 0, errors.New("DragonflyDB is required for login sessions")
	}
	if userID < 0 || createdAfter <= 0 {
		return 0, ErrUserSessionInvalid
	}
	return r.cache.ZCount(ctx, userSessionIndex(userID, "issued"), "("+strconv.FormatInt(createdAfter, 10), "+inf").Result()
}
func (r *Store) GetUserSessionBySID(sid string) (*UserSession, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for login sessions")
	}
	if sid == "" {
		return nil, ErrUserSessionInvalid
	}
	var s UserSession
	if err := r.cache.HGetAll(context.Background(), userSessionCacheKey(sid)).Scan(&s); err != nil {
		return nil, err
	}
	if s.SID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if s.SID != sid || s.UserID <= 0 || s.Version <= 0 || s.UserAuthVersion <= 0 {
		return nil, ErrUserSessionInvalid
	}
	return &s, nil
}
func (r *Store) GetUserSessionCached(sid string) (*UserSession, error) {
	s, err := r.GetUserSessionBySID(sid)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserSessionInactive
	}
	if err != nil {
		return nil, err
	}
	if s.Status != UserSessionStatusActive || s.RevokedAt != 0 || s.ExpiresAt <= time.Now().Unix() {
		return nil, ErrUserSessionInactive
	}
	return s, nil
}
func (r *Store) ListActiveUserSessions(userID int, currentSID string, now int64) ([]UserSession, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 {
		return nil, ErrUserSessionInvalid
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	var user User
	if err := r.db.Select("id", "auth_version").First(&user, userID).Error; err != nil {
		return nil, err
	}
	sids, err := r.cache.ZRangeByScore(context.Background(), userSessionIndex(userID, "active"), &redis.ZRangeBy{Min: "(" + strconv.FormatInt(now, 10), Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	rows := make([]UserSession, 0, len(sids))
	for _, sid := range sids {
		s, err := r.GetUserSessionBySID(sid)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s.Status == UserSessionStatusActive && s.RevokedAt == 0 && s.ExpiresAt > now && s.UserAuthVersion == user.AuthVersion {
			rows = append(rows, *s)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SID == currentSID || rows[j].SID == currentSID {
			return rows[i].SID == currentSID
		}
		if rows[i].LastActiveAt != rows[j].LastActiveAt {
			return rows[i].LastActiveAt > rows[j].LastActiveAt
		}
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt > rows[j].CreatedAt
		}
		return rows[i].SID < rows[j].SID
	})
	if len(rows) > userSessionListLimit {
		rows = rows[:userSessionListLimit]
	}
	return rows, nil
}

var rotateSession = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'UserID') ~= ARGV[1] then return 0 end
if redis.call('HGET', KEYS[1], 'Status') ~= 'active' or tonumber(redis.call('HGET',KEYS[1],'ExpiresAt')) <= tonumber(ARGV[4]) then return 0 end
local current = redis.call('HGET', KEYS[1], 'RefreshHash')
if current == ARGV[2] then
    redis.call('HSET',KEYS[1],'PreviousRefreshHash',current,'PreviousValidUntil',ARGV[5],'RefreshHash',ARGV[3],'LastActiveAt',ARGV[4])
    return 1
end
if redis.call('HGET',KEYS[1],'PreviousRefreshHash') ~= ARGV[2] then return -1 end
if tonumber(ARGV[4]) <= tonumber(redis.call('HGET',KEYS[1],'PreviousValidUntil') or '0') then return -2 end
redis.call('HSET',KEYS[1],'Status','revoked','RevokedAt',ARGV[4],'RevokedReason','refresh_reuse')
redis.call('ZREM',KEYS[2],ARGV[6])
if redis.call('PTTL',KEYS[1]) > tonumber(ARGV[7]) then redis.call('PEXPIRE',KEYS[1],ARGV[7]) end
return -3
`)

func (r *Store) RotateUserSessionRefresh(userID int, sid, presentedHash, nextHash string, now int64, grace time.Duration) (*UserSession, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 || sid == "" || presentedHash == "" || nextHash == "" || presentedHash == nextHash || grace < 0 {
		return nil, ErrUserSessionInvalid
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	result, err := rotateSession.Run(context.Background(), r.cache, []string{userSessionCacheKey(sid), userSessionIndex(userID, "active")}, userID, presentedHash, nextHash, now, now+int64(grace/time.Second), sid, max(common.UserSessionRevokedRetentionDays, 1)*24*60*60*1000).Int()
	if err != nil {
		return nil, err
	}
	switch result {
	case 0:
		return nil, ErrUserSessionInactive
	case -1:
		return nil, ErrUserSessionRefreshInvalid
	case -3:
		return nil, ErrUserSessionRefreshReuse
	}
	s, err := r.GetUserSessionCached(sid)
	if err != nil {
		return nil, err
	}
	if result == -2 {
		return s, ErrUserSessionRefreshRace
	}
	return s, nil
}

var revokeSession = redis.NewScript(`
if redis.call('HGET',KEYS[1],'Status') ~= 'active' or tonumber(redis.call('HGET',KEYS[1],'ExpiresAt') or '0') <= tonumber(ARGV[3]) then return 0 end
if ARGV[1] ~= '' and redis.call('HGET',KEYS[1],'UserID') ~= ARGV[1] then return 0 end
if ARGV[2] ~= '' then
    local valid = redis.call('HGET',KEYS[1],'RefreshHash') == ARGV[2]
    if not valid then valid = redis.call('HGET',KEYS[1],'PreviousRefreshHash') == ARGV[2] and tonumber(ARGV[3]) <= tonumber(redis.call('HGET',KEYS[1],'PreviousValidUntil') or '0') end
    if not valid then return 0 end
end
redis.call('HSET',KEYS[1],'Status','revoked','RevokedAt',ARGV[3],'RevokedReason',ARGV[4])
redis.call('ZREM',KEYS[2],ARGV[5])
if redis.call('PTTL',KEYS[1]) > tonumber(ARGV[6]) then redis.call('PEXPIRE',KEYS[1],ARGV[6]) end
return 1
`)

func (r *Store) RevokeUserSession(userID int, sid, reason string) (bool, error) {
	if r.cache == nil {
		return false, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 || sid == "" {
		return false, ErrUserSessionInvalid
	}
	result, err := revokeSession.Run(context.Background(), r.cache, []string{userSessionCacheKey(sid), userSessionIndex(userID, "active")}, userID, "", time.Now().Unix(), reason, sid, max(common.UserSessionRevokedRetentionDays, 1)*24*60*60*1000).Int()
	return result == 1, err
}
func (r *Store) RevokeUserSessionByRefreshHash(sid, hash, reason string) (bool, error) {
	if hash == "" {
		return false, ErrUserSessionInvalid
	}
	s, err := r.GetUserSessionBySID(sid)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := revokeSession.Run(context.Background(), r.cache, []string{userSessionCacheKey(sid), userSessionIndex(s.UserID, "active")}, s.UserID, hash, time.Now().Unix(), reason, sid, max(common.UserSessionRevokedRetentionDays, 1)*24*60*60*1000).Int()
	return result == 1, err
}

var advanceSession = redis.NewScript(`
if redis.call('HGET',KEYS[1],'UserID') ~= ARGV[1] or redis.call('HGET',KEYS[1],'Status') ~= 'active' or tonumber(redis.call('HGET',KEYS[1],'ExpiresAt') or '0') <= tonumber(ARGV[5]) then return 0 end
if redis.call('HGET',KEYS[1],'Version') ~= ARGV[2] or redis.call('HGET',KEYS[1],'UserAuthVersion') ~= ARGV[3] then return 0 end
redis.call('HINCRBY',KEYS[1],'Version',1)
redis.call('HSET',KEYS[1],'UserAuthVersion',ARGV[4],'LastActiveAt',ARGV[5])
return 1
`)

func (r *Store) AdvanceUserSessionAuthVersion(userID int, sid string, expectedSessionVersion, expectedUserAuthVersion, nextUserAuthVersion int64) (*UserSession, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 || sid == "" || expectedSessionVersion <= 0 || expectedUserAuthVersion <= 0 || nextUserAuthVersion <= expectedUserAuthVersion {
		return nil, ErrUserSessionInvalid
	}
	result, err := advanceSession.Run(context.Background(), r.cache, []string{userSessionCacheKey(sid)}, userID, expectedSessionVersion, expectedUserAuthVersion, nextUserAuthVersion, time.Now().Unix()).Int()
	if err != nil {
		return nil, err
	}
	if result != 1 {
		return nil, ErrUserSessionInactive
	}
	return r.GetUserSessionCached(sid)
}
func (r *Store) RevokeOtherUserSessions(userID int, currentSID, reason string) (int64, error) {
	return r.revokeUserSessions(userID, currentSID, reason)
}
func (r *Store) RevokeAllUserSessions(userID int, reason string) (int64, error) {
	return r.revokeUserSessions(userID, "", reason)
}
func (r *Store) revokeUserSessions(userID int, excludedSID, reason string) (int64, error) {
	if r.cache == nil {
		return 0, errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 {
		return 0, ErrUserSessionInvalid
	}
	sids, err := r.cache.ZRange(context.Background(), userSessionIndex(userID, "active"), 0, -1).Result()
	if err != nil {
		return 0, err
	}
	var count int64
	for _, sid := range sids {
		if sid == excludedSID {
			continue
		}
		revoked, err := r.RevokeUserSession(userID, sid, reason)
		if err != nil {
			return count, err
		}
		if revoked {
			count++
		}
	}
	return count, nil
}

var deleteSessionForUser = redis.NewScript(`
if redis.call('HGET',KEYS[1],'UserID') ~= ARGV[1] then return 0 end
local sid = redis.call('HGET',KEYS[1],'SID')
redis.call('DEL',KEYS[1])
for i=2,4 do redis.call('ZREM',KEYS[i],sid) end
return 1
`)

// DeleteUserSessions erases session metadata after an account is hard-deleted.
func (r *Store) DeleteUserSessions(userID int) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for login sessions")
	}
	if userID <= 0 {
		return ErrUserSessionInvalid
	}
	ctx := context.Background()
	var cursor uint64
	for {
		keys, next, err := r.cache.Scan(ctx, cursor, "auth:session:*", 100).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := deleteSessionForUser.Run(ctx, r.cache, []string{key, userSessionIndex(userID, "active"), userSessionIndex(userID, "issued"), userSessionIndex(0, "issued")}, userID).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return r.cache.Del(ctx, userSessionIndex(userID, "active"), userSessionIndex(userID, "issued")).Err()
		}
	}
}
