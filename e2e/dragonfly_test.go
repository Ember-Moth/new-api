package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDragonflyCacheContracts(t *testing.T) {
	dsn := os.Getenv("TEST_DRAGONFLY_DSN")
	if dsn == "" {
		t.Skip("TEST_DRAGONFLY_DSN is required for the real DragonflyDB integration test")
	}
	previousClient, previousEnabled := common.RDB, common.RedisEnabled
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousBatch, previousSync := common.BatchUpdateEnabled, common.SyncFrequency
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousClient, previousEnabled
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.BatchUpdateEnabled, common.SyncFrequency = previousBatch, previousSync
	})
	t.Setenv("REDIS_CONN_STRING", dsn)
	t.Setenv("SYNC_FREQUENCY", "60")
	common.RedisEnabled = false
	require.NoError(t, common.InitRedisClient())
	client := common.RDB
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	assert.True(t, common.RedisEnabled)
	// This test owns logical database 15 exclusively, never the application's DB 0.
	require.Equal(t, 15, client.Options().DB, "use a disposable DragonflyDB database 15")
	info, err := client.Info(t.Context(), "server").Result()
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(info), "dragonfly_version")
	count, err := client.DBSize(t.Context()).Result()
	require.NoError(t, err)
	require.Zero(t, count, "refusing to clear a non-empty cache database")
	t.Cleanup(func() { require.NoError(t, client.FlushDB(context.Background()).Err()) })

	database, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = database, database
	common.BatchUpdateEnabled, common.SyncFrequency = false, 60
	pool, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))

	t.Run("concurrent reservations cannot spend the same cached balance twice", func(t *testing.T) {
		user := model.User{Username: "dragonfly-reserve", AffCode: "dragonfly-reserve", Quota: 10, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		type outcome struct {
			reserved bool
			err      error
		}
		start := make(chan struct{})
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				reserved, err := model.TryReserveUserQuota(user.Id, 8)
				results <- outcome{reserved: reserved, err: err}
			}()
		}
		close(start)
		winners := 0
		for range 2 {
			result := <-results
			require.NoError(t, result.err)
			if result.reserved {
				winners++
			}
		}
		assert.Equal(t, 1, winners)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 2, cached.Quota)
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 2, user.Quota)
	})

	t.Run("wallet maximum and token usage retain exact integer values", func(t *testing.T) {
		user := model.User{Username: "dragonfly-large-wallet", AffCode: "dragonfly-large-wallet", Quota: common.MaxWalletQuota, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 2)
		require.NoError(t, err)
		assert.True(t, reserved)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, common.MaxWalletQuota-2, cached.Quota)
		token := model.Token{UserId: user.Id, Key: strings.Repeat("a", 32), Status: common.TokenStatusEnabled, RemainQuota: 20, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		reserved, err = model.TryReserveTokenQuota(token.Id, token.Key, 7, false)
		require.NoError(t, err)
		assert.True(t, reserved)
		reserved, err = model.TryReserveTokenQuota(token.Id, token.Key, 14, false)
		require.NoError(t, err)
		assert.False(t, reserved)
		stored, err := model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 13, stored.RemainQuota)
		assert.Equal(t, 7, stored.UsedQuota)
		require.NoError(t, database.First(&token, token.Id).Error)
		assert.Equal(t, stored.RemainQuota, token.RemainQuota)
		assert.Equal(t, stored.UsedQuota, token.UsedQuota)
	})

	t.Run("token management invalidates hydrated authorization snapshots", func(t *testing.T) {
		management := identity.New(identity.Dependencies{
			DB: database, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
			TokenPolicy: identity.TokenPolicy{
				MaxAutoGroups:     func() int { return 5 },
				IsSelectableGroup: func(userGroup, group string) bool { return group == "vip" },
			},
		})
		tokens := []model.Token{
			{UserId: 71, Name: "dragonfly-edit-token", Key: "dragonfly-edit-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100, Group: "auto", CrossGroupRetry: true, AutoGroups: model.StringList{"default", "vip"}},
			{UserId: 71, Name: "dragonfly-batch-token", Key: "dragonfly-batch-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
			{UserId: 72, Name: "dragonfly-foreign-token", Key: "dragonfly-foreign-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
		}
		require.NoError(t, database.Create(&tokens).Error)
		for _, token := range tokens {
			_, err := model.GetTokenByKey(token.Key, false)
			require.NoError(t, err)
		}
		updated, err := management.UpdateToken(t.Context(), contract.TokenActor{ID: 71, Group: "default"}, contract.TokenRequest{
			TokenSettings: contract.TokenSettings{Id: tokens[0].Id, Name: tokens[0].Name, ExpiredTime: -1, RemainQuota: 100, Group: "auto", CrossGroupRetry: true},
			AutoGroups:    contract.TokenAutoGroupsInput{Set: true, Groups: []string{"vip"}},
		}, false)
		require.NoError(t, err)
		assert.Equal(t, []string{"vip"}, updated.AutoGroups)
		reloaded, err := model.GetTokenByKey(tokens[0].Key, false)
		require.NoError(t, err)
		assert.Equal(t, model.StringList{"vip"}, reloaded.AutoGroups)
		// A status-only update must preserve a quota change already committed by accounting.
		require.NoError(t, database.Model(&tokens[0]).Updates(map[string]any{"remain_quota": 73, "used_quota": 27}).Error)
		_, err = management.UpdateToken(t.Context(), contract.TokenActor{ID: 71}, contract.TokenRequest{
			TokenSettings: contract.TokenSettings{Id: tokens[0].Id, Status: common.TokenStatusDisabled},
		}, true)
		require.NoError(t, err)
		reloaded, err = model.GetTokenByKey(tokens[0].Key, false)
		require.NoError(t, err)
		assert.Equal(t, 73, reloaded.RemainQuota)
		assert.Equal(t, 27, reloaded.UsedQuota)
		_, err = model.ValidateUserToken(tokens[0].Key)
		require.ErrorIs(t, err, model.ErrTokenInvalid)
		require.NoError(t, management.DeleteToken(t.Context(), tokens[0].Id, 71))
		_, err = model.GetTokenByKey(tokens[0].Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		deleted, err := management.DeleteTokens(t.Context(), []int{tokens[1].Id, tokens[2].Id}, 71)
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
		_, err = model.GetTokenByKey(tokens[1].Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		retained, err := model.ValidateUserToken(tokens[2].Key)
		require.NoError(t, err)
		assert.Equal(t, 72, retained.UserId)
	})

	t.Run("user management invalidates cached sessions and API access", func(t *testing.T) {
		management := identity.New(identity.Dependencies{
			DB: database, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
			UserSecurity: identity.UserSecurity{
				AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
				PublishDeletedVersion: model.PublishCommittedUserAuthVersion,
				RevokeSessions:        func(id int, reason string) error { _, err := model.RevokeAllUserSessions(id, reason); return err },
				InvalidateUser:        model.InvalidateUserCache, InvalidateTokens: model.InvalidateUserTokensCache,
				DeleteCredentials: model.DeleteUserAuthenticationData,
			},
		})
		user := model.User{Username: "dragonfly-managed-user", AffCode: "dragonfly-managed-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		token := model.Token{UserId: user.Id, Key: strings.Repeat("c", 32), Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		now := time.Now().Unix()
		session := model.UserSession{SID: "dragonfly-managed-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: strings.Repeat("d", 64), LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600}
		require.NoError(t, model.CreateUserSession(&session))
		_, err := model.GetUserSessionCached(session.SID)
		require.NoError(t, err)
		router := gin.New()
		router.GET("/managed-access", middleware.TokenAuthReadOnly(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		request := httptest.NewRequest(http.MethodGet, "/managed-access", nil)
		request.Header.Set("Authorization", "Bearer sk-"+token.Key)
		allowed := httptest.NewRecorder()
		router.ServeHTTP(allowed, request)
		require.Equal(t, http.StatusNoContent, allowed.Code)
		_, err = management.ManageUser(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, contract.ManageUserRequest{Id: user.Id, Action: "disable"})
		require.NoError(t, err)
		blocked := httptest.NewRecorder()
		router.ServeHTTP(blocked, request.Clone(t.Context()))
		assert.Equal(t, http.StatusForbidden, blocked.Code)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, common.UserStatusDisabled, cached.Status)
		assert.EqualValues(t, 2, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
		_, err = model.GetUserSessionCached(session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		_, err = management.DeleteUser(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, user.Id)
		require.NoError(t, err)
		_, err = model.GetTokenByKey(token.Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		_, err = model.GetUserCache(user.Id)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(session.SID)
		require.Error(t, err)
	})

	t.Run("self password rotation preserves the current cached session", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-self-password-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		password, err := common.Password2Hash("CurrentPassword123")
		require.NoError(t, err)
		user := model.User{Username: "dragonfly-self", AffCode: "dragonfly-self", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100, Setting: `{"language":"zh","billing_preference":"subscription_first","sidebar_modules":"saved"}`}
		require.NoError(t, database.Create(&user).Error)
		first, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "first-browser")
		require.NoError(t, err)
		second, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "second-browser")
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(first.Session.SID)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(second.Session.SID)
		require.NoError(t, err)
		identityBefore, err := service.ParseAccessToken(first.AccessToken)
		require.NoError(t, err)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		bundle, err := management.UpdateSelf(t.Context(), user.Id, contract.SelfUpdateRequest{ProfileInput: contract.ProfileInput{Password: "NewPassword123", OriginalPassword: "CurrentPassword123"}}, &identityBefore)
		require.NoError(t, err)
		assert.Equal(t, first.Session.SID, bundle.Session.SID)
		_, _, err = service.ValidateLoginSession(identityBefore)
		require.Error(t, err)
		identityAfter, err := service.ParseAccessToken(bundle.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(identityAfter)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(second.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		require.NoError(t, management.UpdateNotificationSettings(t.Context(), user.Id, contract.NotificationSettingsRequest{QuotaWarningType: "email", QuotaWarningThreshold: 2, NotificationEmail: "alerts@example.test"}))
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 100, cached.Quota)
		assert.EqualValues(t, 2, cached.AuthVersion)
		assert.Contains(t, cached.Setting, `"language":"zh"`)
		assert.Contains(t, cached.Setting, `"billing_preference":"subscription_first"`)
		assert.Contains(t, cached.Setting, `"notification_email":"alerts@example.test"`)
	})

	t.Run("two factor changes rotate cached authentication state", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-twofa-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-twofa", AffCode: "dragonfly-twofa", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		browser, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "twofa-browser")
		require.NoError(t, err)
		other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-twofa-browser")
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(browser.Session.SID)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.NoError(t, err)
		before, err := service.ParseAccessToken(browser.AccessToken)
		require.NoError(t, err)
		setup, err := management.SetupTwoFA(t.Context(), user.Id)
		require.NoError(t, err)
		code, err := totp.GenerateCode(setup.Secret, time.Now())
		require.NoError(t, err)
		active, err := management.EnableTwoFA(t.Context(), user.Id, code, &before)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(before)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		enabledIdentity, err := service.ParseAccessToken(active.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enabledIdentity)
		require.NoError(t, err)
		disabled, err := management.DisableTwoFA(t.Context(), user.Id, setup.BackupCodes[0], &enabledIdentity)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enabledIdentity)
		require.Error(t, err)
		finalIdentity, err := service.ParseAccessToken(disabled.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(finalIdentity)
		require.NoError(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 3, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
		status, err := management.TwoFAStatus(t.Context(), user.Id)
		require.NoError(t, err)
		assert.False(t, status.Enabled)
	})

	t.Run("passkey enrollment and removal refresh cached sessions", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-passkey-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-passkey", AffCode: "dragonfly-passkey", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		browser, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "passkey-browser")
		require.NoError(t, err)
		other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-passkey-browser")
		require.NoError(t, err)
		before, err := service.ParseAccessToken(browser.AccessToken)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.NoError(t, err)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		saved, err := management.SaveRegisteredPasskey(t.Context(), &entity.PasskeyCredential{UserID: user.Id, CredentialID: "dragonfly-credential", PublicKey: "validated-public-key"}, before)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(before)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		enrolled, err := service.ParseAccessToken(saved.AccessToken)
		require.NoError(t, err)
		removed, err := management.DeletePasskey(t.Context(), user.Id, enrolled)
		require.NoError(t, err)
		final, err := service.ParseAccessToken(removed.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(final)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enrolled)
		require.Error(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 3, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
	})

	t.Run("session revocation and auth version publication invalidate cached access", func(t *testing.T) {
		user := model.User{Username: "dragonfly-session", AffCode: "dragonfly-session", AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		now := time.Now().Unix()
		session := model.UserSession{SID: "dragonfly-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: strings.Repeat("b", 64), LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600}
		require.NoError(t, model.CreateUserSession(&session))
		_, err = model.GetUserSessionCached(session.SID)
		require.NoError(t, err)
		revoked, err := model.RevokeUserSession(user.Id, session.SID, "integration-test")
		require.NoError(t, err)
		assert.True(t, revoked)
		_, err = model.GetUserSessionCached(session.SID)
		assert.ErrorIs(t, err, model.ErrUserSessionInactive)
		require.NoError(t, model.SetUserAuthVersionFence(user.Id, 2))
		_, err = model.GetUserCache(user.Id)
		assert.ErrorIs(t, err, model.ErrUserAuthCachePending)
		require.NoError(t, database.Model(&user).Update("auth_version", 2).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 2, cached.AuthVersion)
	})

	t.Run("hash transactions and namespaced scan deletion preserve TTL and unrelated keys", func(t *testing.T) {
		value := struct {
			Name  string
			Count int
		}{Name: "cache-value", Count: 7}
		require.NoError(t, common.RedisHSetObj("dragonfly-integration:hash", &value, time.Minute))
		var loaded struct {
			Name  string
			Count int
		}
		require.NoError(t, common.RedisHGetObj("dragonfly-integration:hash", &loaded))
		assert.Equal(t, value, loaded)
		ttl, err := client.TTL(t.Context(), "dragonfly-integration:hash").Result()
		require.NoError(t, err)
		assert.Positive(t, ttl)
		assert.LessOrEqual(t, ttl, time.Minute)
		expiresAt, err := client.Do(t.Context(), "PEXPIRETIME", "dragonfly-integration:hash").Int64()
		require.NoError(t, err)
		require.NoError(t, common.RedisHIncrBy("dragonfly-integration:hash", "Count", 3))
		require.NoError(t, common.RedisHSetField("dragonfly-integration:hash", "Name", "updated"))
		require.NoError(t, common.RedisHGetObj("dragonfly-integration:hash", &loaded))
		assert.Equal(t, 10, loaded.Count)
		assert.Equal(t, "updated", loaded.Name)
		afterMutation, err := client.Do(t.Context(), "PEXPIRETIME", "dragonfly-integration:hash").Int64()
		require.NoError(t, err)
		assert.Equal(t, expiresAt, afterMutation, "atomic field updates must not extend cache life")
		require.NoError(t, common.RedisHIncrBy("dragonfly-integration:missing", "Count", 1))
		exists, err := client.Exists(t.Context(), "dragonfly-integration:missing").Result()
		require.NoError(t, err)
		assert.Zero(t, exists, "mutation must not create a partial cache record")
		cache := cachex.NewHybridCache(cachex.HybridCacheConfig[string]{Namespace: "dragonfly-integration", Redis: client, RedisCodec: cachex.StringCodec{}})
		require.NoError(t, cache.SetWithTTL("target:one", "value", time.Minute))
		require.NoError(t, cache.SetWithTTL("keep", "untouched", time.Minute))
		deleted, err := cache.DeleteByPrefix("target:")
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
		kept, found, err := cache.Get("keep")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "untouched", kept)
	})

	t.Run("fixed window rate limit returns 429 with retry information", func(t *testing.T) {
		previousEnabled, previousNum, previousDuration := common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
		t.Cleanup(func() {
			common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = previousEnabled, previousNum, previousDuration
		})
		common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = true, 1, 60
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.GET("/limited", middleware.GlobalAPIRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		for _, expected := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
			request := httptest.NewRequest(http.MethodGet, "/limited", nil)
			request.RemoteAddr = "192.0.2.18:5000"
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, expected, response.Code)
			if expected == http.StatusTooManyRequests {
				assert.NotEmpty(t, response.Header().Get("Retry-After"))
			}
		}
	})
}
