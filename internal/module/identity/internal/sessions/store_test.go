package sessions

import (
	"fmt"
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

func setupUserSessionTest(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	require.NoError(t, schema.UpPostgres(pool))
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	active, issued, window := common.UserSessionActiveLimit, common.UserSessionIssuanceLimit, common.UserSessionIssuanceWindowSeconds
	common.UserSessionActiveLimit, common.UserSessionIssuanceLimit, common.UserSessionIssuanceWindowSeconds = 200, 400, 86400
	t.Cleanup(func() {
		_ = client.Close()
		common.UserSessionActiveLimit, common.UserSessionIssuanceLimit, common.UserSessionIssuanceWindowSeconds = active, issued, window
	})
	return New(db, client), server
}
func newTestUserSession(sid string, userID int, now int64) *UserSession {
	return &UserSession{SID: sid, UserID: userID, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive, RefreshHash: "digest-" + sid, LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600}
}
func TestSessionAuthorityExpiresWithoutDatabaseFallback(t *testing.T) {
	store, server := setupUserSessionTest(t)
	s := newTestUserSession("expires", 1, time.Now().Unix())
	require.NoError(t, store.CreateUserSession(s))
	assert.False(t, store.db.Migrator().HasTable("user_sessions"))
	got, err := store.GetUserSessionCached(s.SID)
	require.NoError(t, err)
	assert.Equal(t, s, got)
	ttl, err := store.cache.PTTL(t.Context(), userSessionCacheKey(s.SID)).Result()
	require.NoError(t, err)
	assert.InDelta(t, 3600, ttl.Seconds(), 2)
	server.FastForward(time.Hour + time.Second)
	issued, err := store.CountUserSessionsCreatedSince(s.UserID, s.CreatedAt-1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, issued, "session expiry must not erase issuance history")
	_, err = store.GetUserSessionCached(s.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	require.NoError(t, store.cache.Close())
	_, err = store.GetUserSessionCached(s.SID)
	require.Error(t, err)
	require.Error(t, store.CreateUserSession(newTestUserSession("offline", 1, time.Now().Unix())))
}
func TestRefreshRotationRaceReuseAndSecretAuthenticatedLogout(t *testing.T) {
	store, _ := setupUserSessionTest(t)
	now := time.Now().Unix()
	s := newTestUserSession("rotate", 1, now)
	require.NoError(t, store.CreateUserSession(s))
	_, err := store.RotateUserSessionRefresh(1, s.SID, "unknown", "next", now, time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshInvalid)
	revoked, err := store.RevokeUserSessionByRefreshHash(s.SID, "unknown", "logout")
	require.NoError(t, err)
	assert.False(t, revoked)
	_, err = store.RotateUserSessionRefresh(1, s.SID, s.RefreshHash, "next", now, 5*time.Second)
	require.NoError(t, err)
	_, err = store.RotateUserSessionRefresh(1, s.SID, s.RefreshHash, "other", now+1, 5*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshRace)
	got, err := store.GetUserSessionBySID(s.SID)
	require.NoError(t, err)
	assert.Equal(t, "next", got.RefreshHash)
	_, err = store.RotateUserSessionRefresh(1, s.SID, s.RefreshHash, "other", now+6, 5*time.Second)
	assert.ErrorIs(t, err, ErrUserSessionRefreshReuse)
	_, err = store.GetUserSessionCached(s.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	count, err := store.CountActiveUserSessions(1, now)
	require.NoError(t, err)
	assert.Zero(t, count)
	count, err = store.CountUserSessionsCreatedSince(1, now-1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	good := newTestUserSession("logout", 1, now)
	require.NoError(t, store.CreateUserSession(good))
	revoked, err = store.RevokeUserSession(2, good.SID, "foreign")
	require.NoError(t, err)
	assert.False(t, revoked)
	revoked, err = store.RevokeUserSessionByRefreshHash(good.SID, good.RefreshHash, "logout")
	require.NoError(t, err)
	assert.True(t, revoked)
}
func TestConcurrentSessionIssuanceEnforcesBothLimits(t *testing.T) {
	store, _ := setupUserSessionTest(t)
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 2
	start := make(chan struct{})
	results := make(chan error, 2)
	now := time.Now().Unix()
	for _, sid := range []string{"a", "b"} {
		go func() { <-start; results <- store.CreateUserSession(newTestUserSession(sid, 1, now)) }()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else {
			assert.ErrorIs(t, err, ErrUserSessionLimit)
		}
	}
	assert.Equal(t, 1, winners)
	count, err := store.RevokeAllUserSessions(1, "logout")
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.NoError(t, store.CreateUserSession(newTestUserSession("c", 1, now)))
	_, err = store.RevokeAllUserSessions(1, "logout")
	require.NoError(t, err)
	assert.ErrorIs(t, store.CreateUserSession(newTestUserSession("d", 1, now)), ErrUserSessionIssuanceLimit)
	count, err = store.CountUserSessionsCreatedSince(1, now)
	require.NoError(t, err)
	assert.Zero(t, count, "strict issuance boundary")
	count, err = store.CountUserSessionsCreatedSince(0, now-1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
}
func TestSessionManagementAndVersionFencing(t *testing.T) {
	store, _ := setupUserSessionTest(t)
	now := time.Now().Unix()
	user := User{Id: 1, Username: "sessions", AffCode: "sessions", AuthVersion: 1}
	require.NoError(t, store.db.Create(&user).Error)
	for _, sid := range []string{"current", "other"} {
		require.NoError(t, store.CreateUserSession(newTestUserSession(sid, 1, now)))
	}
	current, err := store.AdvanceUserSessionAuthVersion(1, "current", 1, 1, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, current.Version)
	_, err = store.AdvanceUserSessionAuthVersion(1, "current", 1, 1, 2)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	require.NoError(t, store.db.Model(&user).Update("auth_version", 2).Error)
	rows, err := store.ListActiveUserSessions(1, "current", now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "current", rows[0].SID)
	count, err := store.RevokeOtherUserSessions(1, "current", "others")
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	_, err = store.GetUserSessionCached("current")
	require.NoError(t, err)
	assert.ErrorIs(t, store.CreateUserSession(newTestUserSession("current", 2, now)), ErrUserSessionInvalid, "existing SID cannot be overwritten")
	raw, err := common.Marshal(current)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), current.RefreshHash)
	exact := newTestUserSession("exact-version", 1, now)
	exact.Version = 9007199254740993
	require.NoError(t, store.CreateUserSession(exact))
	next, err := store.AdvanceUserSessionAuthVersion(1, exact.SID, exact.Version, 1, 2)
	require.NoError(t, err)
	assert.Equal(t, exact.Version+1, next.Version)
}
func TestSessionListKeepsCurrentAndBoundsResults(t *testing.T) {
	store, _ := setupUserSessionTest(t)
	now := time.Now().Unix()
	require.NoError(t, store.db.Create(&User{Id: 1, Username: "list", AffCode: "list", AuthVersion: 1}).Error)
	for i := 0; i < 101; i++ {
		s := newTestUserSession(fmt.Sprint(i), 1, now)
		s.LastActiveAt = now - int64(i)
		require.NoError(t, store.CreateUserSession(s))
	}
	rows, err := store.ListActiveUserSessions(1, "100", now)
	require.NoError(t, err)
	require.Len(t, rows, 100)
	assert.Equal(t, "100", rows[0].SID)
	assert.Equal(t, "0", rows[1].SID)

}
