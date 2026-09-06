package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type setMiniRedisTimeOnEvalHook struct {
	server *miniredis.Miniredis
	at     time.Time
}

func (hook setMiniRedisTimeOnEvalHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() == "eval" || cmd.Name() == "evalsha" {
		hook.server.SetTime(hook.at)
	}
	return ctx, nil
}

func (setMiniRedisTimeOnEvalHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (setMiniRedisTimeOnEvalHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (setMiniRedisTimeOnEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func setupUserSessionTest(t *testing.T) *Store {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	require.NoError(t, schema.UpPostgres(pool))
	oldRedisEnabled := common.RedisEnabled
	oldActiveLimit := common.UserSessionActiveLimit
	oldIssuanceLimit := common.UserSessionIssuanceLimit
	oldIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	oldRevokedRetention := common.UserSessionRevokedRetentionDays
	common.RedisEnabled = false
	common.UserSessionActiveLimit = common.DefaultUserSessionActiveLimit
	common.UserSessionIssuanceLimit = common.DefaultUserSessionIssuanceLimit
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	common.UserSessionRevokedRetentionDays = common.DefaultUserSessionRevokedRetentionDays
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.UserSessionActiveLimit = oldActiveLimit
		common.UserSessionIssuanceLimit = oldIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = oldIssuanceWindow
		common.UserSessionRevokedRetentionDays = oldRevokedRetention
	})
	return New(db)
}

func createUserSessionTestUser(t *testing.T, store *Store, userID int, authVersion int64) {
	t.Helper()
	user := User{
		Id:          userID,
		Username:    fmt.Sprintf("user-session-%d", userID),
		Password:    "unused",
		Status:      common.UserStatusEnabled,
		Role:        common.RoleCommonUser,
		Group:       "default",
		AffCode:     fmt.Sprintf("session-aff-%d", userID),
		AuthVersion: authVersion,
	}
	require.NoError(t, store.db.Create(&user).Error)
	t.Cleanup(func() { _ = store.db.Unscoped().Delete(&User{}, userID).Error })
}

func newTestUserSession(sid string, userID int, now int64) *UserSession {
	return &UserSession{
		SID:             sid,
		UserID:          userID,
		Version:         1,
		UserAuthVersion: 1,
		Status:          UserSessionStatusActive,
		RefreshHash:     fmt.Sprintf("%x", sha256.Sum256([]byte(sid))),
		LoginMethod:     "password",
		IP:              "127.0.0.1",
		UserAgent:       "model-test",
		CreatedAt:       now,
		LastActiveAt:    now,
		ExpiresAt:       now + int64((30*24*time.Hour)/time.Second),
	}
}

func TestUserSessionCacheTTLUsesShortCacheWindow(t *testing.T) {
	store := setupUserSessionTest(t)
	server := useSessionMiniRedis(t)
	now := time.Now().Unix()
	tests := []struct {
		name       string
		status     string
		expiresAt  int64
		wantMaxTTL time.Duration
	}{
		{name: "active", status: UserSessionStatusActive, expiresAt: now + 300, wantMaxTTL: 2 * time.Second},
		{name: "revoking", status: UserSessionStatusRevoking, expiresAt: now + 300, wantMaxTTL: 2 * time.Second},
		{name: "revoked", status: UserSessionStatusRevoked, expiresAt: now + 300, wantMaxTTL: 2 * time.Second},
		{name: "already expired", status: UserSessionStatusRevoked, expiresAt: now - 1, wantMaxTTL: time.Second},
	}

	for index, test := range tests {
		sid := fmt.Sprintf("short-cache-ttl-%d", index)
		entry := sessionCacheEntry(newTestUserSession(sid, 1100+index, now))
		entry.Status = test.status
		entry.ExpiresAt = test.expiresAt
		if test.status != UserSessionStatusActive {
			entry.RevokedAt = now
		}

		cacheDeadline := time.Time{}
		if test.status == UserSessionStatusActive {
			cacheDeadline = userSessionCacheDeadline()
		}
		require.NoError(t, store.writeUserSessionCache(entry, cacheDeadline), test.name)
		ttl := server.TTL(userSessionCacheKey(sid))
		assert.Positive(t, ttl, test.name)
		assert.LessOrEqual(t, ttl, test.wantMaxTTL, test.name)
	}

	initialTTL := server.TTL(userSessionCacheKey("short-cache-ttl-0"))
	server.FastForward(time.Second)
	_, err := store.getUserSessionCache("short-cache-ttl-0")
	require.NoError(t, err)
	remainingTTL := server.TTL(userSessionCacheKey("short-cache-ttl-0"))
	assert.Positive(t, remainingTTL)
	assert.LessOrEqual(t, remainingTTL, initialTTL-time.Second, "cache reads must not renew the bounded TTL")

	common.SyncFrequency = 10
	nearExpiry := sessionCacheEntry(newTestUserSession("short-cache-ttl-near-expiry", 1199, now))
	nearExpiry.ExpiresAt = time.Now().Add(2 * time.Second).Unix()
	nearExpiryDeadline := userSessionCacheDeadline()
	remainingLifetime := time.Until(time.Unix(nearExpiry.ExpiresAt, 0))
	require.NoError(t, store.writeUserSessionCache(nearExpiry, nearExpiryDeadline))
	nearExpiryTTL := server.TTL(userSessionCacheKey(nearExpiry.SID))
	assert.Positive(t, nearExpiryTTL)
	assert.LessOrEqual(t, nearExpiryTTL, remainingLifetime, "cache TTL must not exceed the Session remaining lifetime")

	common.SyncFrequency = 0
	fallback := sessionCacheEntry(newTestUserSession("short-cache-ttl-fallback", 1200, now))
	fallback.ExpiresAt = now + 300
	require.NoError(t, store.writeUserSessionCache(fallback, userSessionCacheDeadline()))
	fallbackTTL := server.TTL(userSessionCacheKey(fallback.SID))
	assert.Greater(t, fallbackTTL, 59*time.Second)
	assert.LessOrEqual(t, fallbackTTL, 60*time.Second, "non-positive cache frequency must use the existing 60-second fallback")
}

func TestStaleActiveSessionCacheFillCannotRestartWindowAfterDenyExpires(t *testing.T) {
	store := setupUserSessionTest(t)
	server := useSessionMiniRedis(t)
	now := time.Now().Unix()
	active := sessionCacheEntry(newTestUserSession("stale-active-cache-fill", 1201, now))
	denied := *active
	denied.Status = UserSessionStatusRevoked
	denied.RevokedAt = now
	denied.RevokedReason = "test-revoke"

	require.NoError(t, store.writeUserSessionCache(&denied, time.Time{}))
	cacheKey := userSessionCacheKey(active.SID)
	assert.True(t, server.Exists(cacheKey))
	server.FastForward(3 * time.Second)
	assert.False(t, server.Exists(cacheKey), "the short deny tombstone must have expired in this race setup")

	err := store.writeUserSessionCache(active, time.Now().Add(-time.Millisecond))
	assert.ErrorIs(t, err, errUserSessionCacheObservationStale)
	assert.False(t, server.Exists(cacheKey), "a delayed pre-revoke active snapshot must not restart a fresh cache window")
}

func TestActiveSessionCacheFillUsesRemainingObservationWindow(t *testing.T) {
	store := setupUserSessionTest(t)
	server := useSessionMiniRedis(t)
	now := time.Now().Unix()
	entry := sessionCacheEntry(newTestUserSession("bounded-active-cache-fill", 1202, now))
	deadline := time.Now().Add(1500 * time.Millisecond)

	require.NoError(t, store.writeUserSessionCache(entry, deadline))
	ttl := server.TTL(userSessionCacheKey(entry.SID))
	assert.Positive(t, ttl)
	assert.LessOrEqual(t, ttl, 1500*time.Millisecond, "a delayed fill must inherit only the unused observation window")
}

func TestSessionCacheLuaUsesAbsoluteActiveAndRelativeDenyExpiry(t *testing.T) {
	store := setupUserSessionTest(t)
	server := useSessionMiniRedis(t)
	now := time.Now().Unix()
	deadline := time.Now().Add(10 * time.Second)
	common.RDB.AddHook(setMiniRedisTimeOnEvalHook{server: server, at: deadline.Add(time.Second)})

	active := sessionCacheEntry(newTestUserSession("delayed-active-cache-eval", 1203, now))
	require.NoError(t, store.writeUserSessionCache(active, deadline))
	assert.False(t, server.Exists(userSessionCacheKey(active.SID)), "an active fill executed after its absolute deadline must not recreate the cache")

	denied := sessionCacheEntry(newTestUserSession("delayed-deny-cache-eval", 1204, now))
	denied.Status = UserSessionStatusRevoked
	denied.RevokedAt = now
	denied.RevokedReason = "test-revoke"
	require.NoError(t, store.writeUserSessionCache(denied, time.Time{}))
	denyTTL := server.TTL(userSessionCacheKey(denied.SID))
	assert.Positive(t, denyTTL)
	assert.LessOrEqual(t, denyTTL, 2*time.Second, "a delayed deny publication must receive a full relative short TTL at Redis execution")
}

func TestUserSessionCreateListAndRevokeOne(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	user := User{Id: 1001, Username: "session-list-user", Password: "password", AuthVersion: 1}
	require.NoError(t, store.db.Create(&user).Error)
	t.Cleanup(func() { _ = store.db.Unscoped().Delete(&User{}, user.Id).Error })
	first := newTestUserSession("session-one", 1001, now)
	second := newTestUserSession("session-two", 1001, now+1)
	require.NoError(t, store.CreateUserSession(first))
	require.NoError(t, store.CreateUserSession(second))

	sessions, err := store.ListActiveUserSessions(1001, first.SID, now)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, first.SID, sessions[0].SID)

	revoked, err := store.RevokeUserSession(1001, first.SID, "user_revoked")
	require.NoError(t, err)
	assert.True(t, revoked)
	revoked, err = store.RevokeUserSession(1001, first.SID, "duplicate")
	require.NoError(t, err)
	assert.False(t, revoked)

	_, err = store.GetUserSessionCached(first.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	active, err := store.GetUserSessionCached(second.SID)
	require.NoError(t, err)
	assert.Equal(t, second.SID, active.SID)
}

func TestRotateUserSessionRefreshRaceAndReuse(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1002, 1)
	session := newTestUserSession("rotate-session", 1002, now)
	require.NoError(t, store.CreateUserSession(session))

	rotated, err := store.RotateUserSessionRefresh(1002, session.SID, session.RefreshHash, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now+10, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rotated.RefreshHash)
	assert.Equal(t, session.RefreshHash, rotated.PreviousRefreshHash)
	assert.Equal(t, now+40, rotated.PreviousValidUntil)

	_, err = store.RotateUserSessionRefresh(1002, session.SID, session.RefreshHash, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", now+20, 30*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshRace)
	_, err = store.RotateUserSessionRefresh(1002, session.SID, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", now+20, 30*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshInvalid)
	stored, getErr := store.GetUserSessionBySID(session.SID)
	require.NoError(t, getErr)
	assert.Equal(t, UserSessionStatusActive, stored.Status)

	_, err = store.RotateUserSessionRefresh(1002, session.SID, session.RefreshHash, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", now+41, 30*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshReuse)
	stored, getErr = store.GetUserSessionBySID(session.SID)
	require.NoError(t, getErr)
	assert.Equal(t, UserSessionStatusRevoked, stored.Status)
	assert.Equal(t, "refresh_reuse", stored.RevokedReason)
}

func TestUserSessionPreviousRefreshHashNormalizesLegacyPadding(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1010, 1)
	digest := strings.Repeat("a", 64)

	blank := newTestUserSession("legacy-blank-previous-hash", 1010, now)
	blank.PreviousRefreshHash = strings.Repeat(" ", 64)
	blank.PreviousValidUntil = now + 60
	require.NoError(t, store.db.Create(blank).Error)
	loadedBlank, err := store.GetUserSessionBySID(blank.SID)
	require.NoError(t, err)
	assert.Empty(t, loadedBlank.PreviousRefreshHash)

	valid := newTestUserSession("legacy-valid-previous-hash", 1010, now)
	valid.RefreshHash = strings.Repeat("b", 64)
	valid.PreviousRefreshHash = digest
	valid.PreviousValidUntil = now + 60
	require.NoError(t, store.db.Create(valid).Error)
	loadedValid, err := store.GetUserSessionBySID(valid.SID)
	require.NoError(t, err)
	assert.Equal(t, digest, loadedValid.PreviousRefreshHash)

	require.NoError(t, store.db.Model(&UserSession{}).Where("sid = ?", valid.SID).
		Updates(map[string]any{
			"previous_refresh_hash": digest + "   ",
			"previous_valid_until":  now + 60,
		}).Error)
	_, err = store.RotateUserSessionRefresh(valid.UserID, valid.SID, digest, strings.Repeat("c", 64), now+1, 30*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshRace)

	revoked, err := store.RevokeUserSessionByRefreshHash(valid.SID, digest, "legacy-padded-refresh-logout")
	require.NoError(t, err)
	assert.True(t, revoked, "refresh-cookie logout must accept a legacy CHAR-padded previous digest inside its grace window")
}

func TestUserSessionCacheExcludesRefreshDigests(t *testing.T) {
	store := setupUserSessionTest(t)
	useSessionMiniRedis(t)
	now := time.Now().Unix()
	session := newTestUserSession("cache-without-refresh-digests", 1011, now)
	session.PreviousRefreshHash = strings.Repeat("a", 64)
	session.PreviousValidUntil = now + 30
	require.NoError(t, store.writeUserSessionCache(sessionCacheEntry(session), userSessionCacheDeadline()))

	cacheKey := userSessionCacheKey(session.SID)
	fields, err := common.RDB.HGetAll(context.Background(), cacheKey).Result()
	require.NoError(t, err)
	assert.NotContains(t, fields, "RefreshHash")
	assert.NotContains(t, fields, "PreviousRefreshHash")
	assert.NotContains(t, fields, "PreviousValidUntil")

	require.NoError(t, common.RDB.HSet(context.Background(), cacheKey,
		"RefreshHash", strings.Repeat("b", 64),
		"PreviousRefreshHash", strings.Repeat("c", 64)+"   ",
		"PreviousValidUntil", now+30,
	).Err())
	entry, err := store.getUserSessionCache(session.SID)
	require.NoError(t, err)
	cachedSession := entry.session()
	assert.Empty(t, cachedSession.RefreshHash)
	assert.Empty(t, cachedSession.PreviousRefreshHash)
	assert.Zero(t, cachedSession.PreviousValidUntil)
}

func TestRevokeOtherUserSessionsKeepsCurrent(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1003, 1)
	createUserSessionTestUser(t, store, 1004, 1)
	for _, sid := range []string{"current-session", "other-one", "other-two"} {
		session := newTestUserSession(sid, 1003, now)
		if sid == "other-one" {
			session.UserAuthVersion = 99
		}
		require.NoError(t, store.CreateUserSession(session))
	}
	require.NoError(t, store.CreateUserSession(newTestUserSession("different-user", 1004, now)))

	count, err := store.RevokeOtherUserSessions(1003, "current-session", "revoke_others")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	current, err := store.GetUserSessionCached("current-session")
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusActive, current.Status)
	_, err = store.GetUserSessionCached("other-one")
	assert.True(t, errors.Is(err, ErrUserSessionInactive))
	stale, err := store.GetUserSessionBySID("other-one")
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusRevoked, stale.Status, "revocation must include active sessions from stale auth versions")
	different, err := store.GetUserSessionCached("different-user")
	require.NoError(t, err)
	assert.Equal(t, 1004, different.UserID)
}

func TestRevokeUserSessionByRefreshHashRequiresSecret(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1005, 1)
	session := newTestUserSession("refresh-logout-session", 1005, now)
	require.NoError(t, store.CreateUserSession(session))

	revoked, err := store.RevokeUserSessionByRefreshHash(session.SID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "logout")
	require.NoError(t, err)
	assert.False(t, revoked)
	active, err := store.GetUserSessionCached(session.SID)
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusActive, active.Status)

	revoked, err = store.RevokeUserSessionByRefreshHash(session.SID, session.RefreshHash, "logout")
	require.NoError(t, err)
	assert.True(t, revoked)
	_, err = store.GetUserSessionCached(session.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
}

func TestUserSessionGrowthCountsUseBroadActiveAndStrictIssuancePredicates(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1006, 7)
	rows := []UserSession{
		*newTestUserSession("count-current-version", 1006, now-10),
		*newTestUserSession("count-stale-version", 1006, now-9),
		*newTestUserSession("count-expired", 1006, now-8),
		*newTestUserSession("count-revoked", 1006, now-7),
		*newTestUserSession("count-cutoff", 1006, now-3600),
	}
	rows[0].UserAuthVersion = 7
	rows[1].UserAuthVersion = 2
	rows[2].UserAuthVersion = 7
	rows[2].ExpiresAt = now
	rows[3].UserAuthVersion = 7
	rows[3].Status = UserSessionStatusRevoked
	rows[3].RevokedAt = now - 1
	rows[4].UserAuthVersion = 7
	rows[4].CreatedAt = now - 3600
	rows[4].ExpiresAt = now
	require.NoError(t, store.db.Create(&rows).Error)

	activeCount, err := store.CountActiveUserSessions(1006, now)
	require.NoError(t, err)
	assert.Equal(t, int64(2), activeCount, "active count includes stale auth versions but excludes expired and revoked rows")

	issuedCount, err := store.CountUserSessionsCreatedSince(1006, now-3600)
	require.NoError(t, err)
	assert.Equal(t, int64(4), issuedCount, "issuance count includes every status and uses a strict cutoff")
	globalCount, err := store.CountUserSessionsCreatedSince(0, now-3600)
	require.NoError(t, err)
	assert.Equal(t, issuedCount, globalCount)
}

func TestListActiveUserSessionsKeepsCurrentAndBoundsOtherSessions(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1007, 7)
	current := newTestUserSession("list-current", 1007, now-1000)
	current.UserAuthVersion = 7
	rows := make([]UserSession, 0, 107)
	rows = append(rows, *current)
	for i := 0; i < 105; i++ {
		session := newTestUserSession(fmt.Sprintf("list-other-%03d", i), 1007, now-int64(i))
		session.UserAuthVersion = 7
		rows = append(rows, *session)
	}
	stale := newTestUserSession("list-stale-auth-version", 1007, now+1)
	stale.UserAuthVersion = 6
	rows = append(rows, *stale)
	require.NoError(t, store.db.CreateInBatches(rows, 100).Error)

	sessions, err := store.ListActiveUserSessions(1007, current.SID, now)
	require.NoError(t, err)
	require.Len(t, sessions, 100)
	assert.Equal(t, current.SID, sessions[0].SID)
	for _, session := range sessions {
		assert.Equal(t, int64(7), session.UserAuthVersion)
		assert.NotEqual(t, stale.SID, session.SID)
	}

	sessionsWithoutCurrent, err := store.ListActiveUserSessions(1007, "missing-current", now)
	require.NoError(t, err)
	assert.Len(t, sessionsWithoutCurrent, userSessionListLimit, "a missing current SID must not reduce the total list limit")
}

func TestRevokeUserSessionsReturnsCumulativeProgressAndSupportsRetry(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	createUserSessionTestUser(t, store, 1008, 1)
	rows := make([]UserSession, 0, userSessionRevokeBatchSize+1)
	for i := 0; i < userSessionRevokeBatchSize+1; i++ {
		rows = append(rows, *newTestUserSession(fmt.Sprintf("batch-revoke-%03d", i), 1008, now))
	}
	require.NoError(t, store.db.CreateInBatches(rows, 100).Error)

	forcedErr := errors.New("forced second revoke batch failure")
	callbackName := "test:fail_second_user_session_revoke_batch"
	updateCalls := 0
	callbackRegistered := true
	require.NoError(t, store.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			updateCalls++
			if updateCalls == 2 {
				tx.AddError(forcedErr)
			}
		}
	}))
	t.Cleanup(func() {
		if callbackRegistered {
			_ = store.db.Callback().Update().Remove(callbackName)
		}
	})

	affected, err := store.RevokeAllUserSessions(1008, "batch-test")
	assert.ErrorIs(t, err, forcedErr)
	assert.Equal(t, int64(userSessionRevokeBatchSize), affected)
	require.NoError(t, store.db.Callback().Update().Remove(callbackName))
	callbackRegistered = false

	retried, err := store.RevokeAllUserSessions(1008, "batch-test-retry")
	require.NoError(t, err)
	assert.Equal(t, int64(1), retried)
	var activeCount int64
	require.NoError(t, store.db.Model(&UserSession{}).Where("user_id = ? AND status = ?", 1008, UserSessionStatusActive).Count(&activeCount).Error)
	assert.Zero(t, activeCount)
}

func TestDeleteExpiredUserSessionsLoopsInChunksAndRechecksPredicate(t *testing.T) {
	store := setupUserSessionTest(t)
	now := time.Now().Unix()
	common.UserSessionRevokedRetentionDays = 7
	common.UserSessionIssuanceWindowSeconds = 3600
	oldCreatedAt := now - 7200
	rows := make([]UserSession, 0, userSessionCleanupScanLimit+5)
	race := newTestUserSession("cleanup-race", 1009, now-1000)
	race.CreatedAt = oldCreatedAt
	race.ExpiresAt = now - 1000
	rows = append(rows, *race)
	for i := 0; i < userSessionCleanupScanLimit+1; i++ {
		session := newTestUserSession(fmt.Sprintf("cleanup-expired-%04d", i), 1009, now-100)
		session.CreatedAt = oldCreatedAt - int64(i)
		session.ExpiresAt = now - 100
		rows = append(rows, *session)
	}
	oldRevoked := newTestUserSession("cleanup-old-revoked", 1009, now-10)
	oldRevoked.CreatedAt = oldCreatedAt
	oldRevoked.Status = UserSessionStatusRevoked
	oldRevoked.RevokedAt = now - int64(8*24*time.Hour/time.Second)
	rows = append(rows, *oldRevoked)
	recentRevoked := newTestUserSession("cleanup-recent-revoked", 1009, now-9)
	recentRevoked.CreatedAt = oldCreatedAt
	recentRevoked.Status = UserSessionStatusRevoked
	recentRevoked.RevokedAt = now - int64(6*24*time.Hour/time.Second)
	recentRevoked.ExpiresAt = now - 100
	rows = append(rows, *recentRevoked)
	recentIssuedExpired := newTestUserSession("cleanup-recent-issued-expired", 1009, now-1800)
	recentIssuedExpired.ExpiresAt = now - 100
	rows = append(rows, *recentIssuedExpired)
	expiryBoundary := newTestUserSession("cleanup-expiry-boundary", 1009, now-7)
	expiryBoundary.CreatedAt = oldCreatedAt
	expiryBoundary.ExpiresAt = now
	rows = append(rows, *expiryBoundary)
	revokedBoundary := newTestUserSession("cleanup-revoked-boundary", 1009, now-6)
	revokedBoundary.CreatedAt = oldCreatedAt
	revokedBoundary.Status = UserSessionStatusRevoked
	revokedBoundary.RevokedAt = now - int64(7*24*time.Hour/time.Second)
	rows = append(rows, *revokedBoundary)
	live := newTestUserSession("cleanup-live", 1009, now-8)
	rows = append(rows, *live)
	require.NoError(t, store.db.CreateInBatches(rows, 100).Error)

	callbackName := "test:recheck_user_session_cleanup_predicate"
	deleteCalls := 0
	mutated := false
	require.NoError(t, store.db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "user_sessions" {
			return
		}
		deleteCalls++
		if !mutated {
			mutated = true
			tx.Exec("UPDATE user_sessions SET expires_at = ? WHERE sid = ?", now+3600, race.SID)
		}
	}))
	t.Cleanup(func() { _ = store.db.Callback().Delete().Remove(callbackName) })

	require.NoError(t, store.DeleteExpiredUserSessions(now))
	require.NoError(t, store.DeleteOldRevokedUserSessions(now))
	assert.Equal(t, 4, deleteCalls, "expired and retained-revoked scans each delete in bounded chunks")
	var remaining []UserSession
	require.NoError(t, store.db.Order("sid").Find(&remaining).Error)
	require.Len(t, remaining, 6)
	remainingSIDs := make([]string, 0, len(remaining))
	for _, session := range remaining {
		remainingSIDs = append(remainingSIDs, session.SID)
	}
	assert.ElementsMatch(t, []string{
		race.SID,
		recentRevoked.SID,
		recentIssuedExpired.SID,
		expiryBoundary.SID,
		revokedBoundary.SID,
		live.SID,
	}, remainingSIDs)
}

func TestUserSessionGrowthQueryIndexesExist(t *testing.T) {
	store := setupUserSessionTest(t)
	migrator := store.db.Migrator()
	assert.True(t, migrator.HasIndex(&UserSession{}, "idx_user_sessions_expires_at"))
	assert.True(t, migrator.HasIndex(&UserSession{}, "idx_user_sessions_user_created"))
	assert.True(t, migrator.HasIndex(&UserSession{}, "idx_user_sessions_status_revoked"))
}

func useSessionMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	previous, enabled, syncFrequency := common.RDB, common.RedisEnabled, common.SyncFrequency
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RDB, common.RedisEnabled, common.SyncFrequency = client, true, 2
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		common.RDB, common.RedisEnabled, common.SyncFrequency = previous, enabled, syncFrequency
	})
	return server
}
