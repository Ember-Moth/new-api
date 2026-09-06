package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAuthSessionTestDB(t *testing.T) *model.User {
	t.Helper()
	testdb.UseCache(t)
	previousDB, previousRedis := model.DB, common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	previousRevokedRetention := common.UserSessionRevokedRetentionDays
	previousAlertThreshold := common.UserSessionHourlyAlertThreshold
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, schema.UpPostgres(sqlDB))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = common.DefaultUserSessionActiveLimit
	common.UserSessionIssuanceLimit = common.DefaultUserSessionIssuanceLimit
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	common.UserSessionRevokedRetentionDays = common.DefaultUserSessionRevokedRetentionDays
	common.UserSessionHourlyAlertThreshold = common.DefaultUserSessionHourlyAlertThreshold
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
		common.UserSessionRevokedRetentionDays = previousRevokedRetention
		common.UserSessionHourlyAlertThreshold = previousAlertThreshold
		_ = sqlDB.Close()
	})
	user := &model.User{
		Username:    "session-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func useSharedAuthSessionRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousSyncFrequency := common.SyncFrequency
	serverA := miniredis.RunT(t)
	serverB := serverA
	clientA := redis.NewClient(&redis.Options{Addr: serverA.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: serverB.Addr()})
	common.RedisEnabled = true
	common.SyncFrequency = 2
	common.RDB = clientA
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		common.SyncFrequency = previousSyncFrequency
	})
	return serverA, clientA, serverB, clientB
}

func cachedLoginSessionKey(t *testing.T, server *miniredis.Miniredis) string {
	t.Helper()
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, "auth:session:") {
			return key
		}
	}
	require.FailNow(t, "login session was not cached")
	return ""
}

func TestCreateLoginSessionEnforcesActiveAndIssuedLimits(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 2
	one, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	assert.ErrorIs(t, err, model.ErrUserSessionLimit)
	_, err = model.RevokeAllUserSessions(user.Id, "logout")
	require.NoError(t, err)
	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	_, err = model.RevokeAllUserSessions(user.Id, "password_reset")
	require.NoError(t, err)
	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	assert.ErrorIs(t, err, model.ErrUserSessionIssuanceLimit)
	identity, err := ParseAccessToken(one.AccessToken)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}
func TestLoginSessionCacheLossRequiresLoginAgain(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	login, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	identity, err := ParseAccessToken(login.AccessToken)
	require.NoError(t, err)
	require.NoError(t, common.RDB.FlushDB(t.Context()).Err())
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	_, _, err = RefreshLoginSession(login.RefreshToken, login.Session.SID, "127.0.0.1", "browser")
	require.Error(t, err)
}
func TestCreateLoginSessionFailsClosedWhenCacheIsUnavailable(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, common.RDB.Close())
	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.Error(t, err)
}

func TestLoginSessionCreateRefreshAndRevoke(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)

	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	assert.NotEmpty(t, bundle.RefreshToken)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, cachedUser, err := ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.Equal(t, user.Id, cachedUser.Id)
	require.NoError(t, RevokeByRefreshToken(bundle.Session.SID+".wrong-refresh-secret", "", "logout"))
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err, "a caller that only knows sid must not be able to revoke the session")

	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.NotEqual(t, bundle.RefreshToken, refreshed.RefreshToken)
	recovered, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken, "a concurrent refresh must recover the winner's rotated token")

	_, _, err = RefreshLoginSession(refreshed.RefreshToken, "different-session", "127.0.0.2", "test-agent-2")
	assert.ErrorIs(t, err, ErrLoginSessionMismatch)

	require.NoError(t, RevokeByRefreshToken(refreshed.RefreshToken, refreshed.Session.SID, "logout"))
	_, _, err = ValidateLoginSession(identity)
	assert.True(t, errors.Is(err, ErrLoginSessionRevoked))
}

func TestSharedDragonflySessionRevokeIsImmediate(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useSharedAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.NotEmpty(t, cachedLoginSessionKey(t, serverB), "node B reads the same authoritative session")

	common.RDB = clientA
	require.NoError(t, RevokeByRefreshToken(bundle.RefreshToken, bundle.Session.SID, "logout"))

	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestSharedDragonflyAuthVersionAdvanceIsImmediate(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useSharedAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	oldIdentity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(oldIdentity)
	require.NoError(t, err)
	cacheKey := cachedLoginSessionKey(t, serverB)
	version := serverB.HGet(cacheKey, "Version")
	assert.Equal(t, "1", version, "node B must hold the pre-rotation session version")

	common.RDB = clientA
	rotated, err := AdvanceCurrentSessionSecurity(oldIdentity, "security_update")
	require.NoError(t, err)
	newIdentity, err := ParseAccessToken(rotated.AccessToken)
	require.NoError(t, err)
	assert.Greater(t, newIdentity.SessionVersion, oldIdentity.SessionVersion)
	assert.Greater(t, newIdentity.UserAuthVersion, oldIdentity.UserAuthVersion)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(oldIdentity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestUserAuthVersionInvalidatesExistingSession(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	_, err = model.BumpUserAuthVersion(user.Id)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	_, err = CreateLoginSessionAtAuthVersion(user.Id, identity.UserAuthVersion, "2fa", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, ErrLoginSessionRevoked, "a pending 2FA flow must not survive an auth-version change")
}

func useTestSessionSecret(t *testing.T) {
	t.Helper()
	previous := common.SessionSecret
	common.SessionSecret = "test-session-secret-with-sufficient-entropy"
	t.Cleanup(func() { common.SessionSecret = previous })
}
