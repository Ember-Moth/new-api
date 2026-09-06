package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	billingentity "github.com/QuantumNous/new-api/internal/module/billing/entity"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
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

	t.Run("ledger persistence failures leave caches unchanged and reservations compensated", func(t *testing.T) {
		user := model.User{Username: "df-ledger-failure", AffCode: "df-ledger-failure", Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		token := model.Token{UserId: user.Id, Key: "df-ledger-failure-token", Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		_, err = model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		ledger := model.AccountingStore()
		require.NoError(t, database.Exec(`CREATE FUNCTION fail_ledger_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected ledger write failure'; END; $$; CREATE TRIGGER fail_ledger_write BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION fail_ledger_write(); CREATE TRIGGER fail_ledger_write BEFORE UPDATE ON tokens FOR EACH ROW EXECUTE FUNCTION fail_ledger_write();`).Error)
		t.Cleanup(func() { require.NoError(t, database.Exec("DROP FUNCTION IF EXISTS fail_ledger_write() CASCADE").Error) })
		require.Error(t, ledger.DecreaseUserQuota(t.Context(), user.Id, 10, true))
		require.Error(t, ledger.IncreaseUserQuota(t.Context(), user.Id, 10, true))
		require.Error(t, ledger.DecreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		require.Error(t, ledger.IncreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		reserved, err := ledger.TryReserveUserQuota(t.Context(), user.Id, 10)
		require.Error(t, err)
		assert.False(t, reserved)
		reserved, err = ledger.TryReserveTokenQuota(t.Context(), token.Id, token.Key, 10, false)
		require.Error(t, err)
		assert.False(t, reserved)
		cachedUser, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		cachedToken, err := model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 100, cachedUser.Quota)
		assert.Equal(t, 100, cachedToken.RemainQuota)
		assert.Zero(t, cachedToken.UsedQuota)
		require.NoError(t, database.Exec("DROP FUNCTION fail_ledger_write() CASCADE").Error)
		require.NoError(t, ledger.DecreaseUserQuota(t.Context(), user.Id, 10, true))
		require.NoError(t, ledger.DecreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		cachedUser, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		cachedToken, err = model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 90, cachedUser.Quota)
		assert.Equal(t, 90, cachedToken.RemainQuota)
		assert.Equal(t, 10, cachedToken.UsedQuota)
		require.NoError(t, database.First(&user, user.Id).Error)
		require.NoError(t, database.First(&token, token.Id).Error)
		assert.Equal(t, user.Quota, cachedUser.Quota)
		assert.Equal(t, token.RemainQuota, cachedToken.RemainQuota)
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

	t.Run("authentication runtime refresh recovery and revoke", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-auth-runtime-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-auth-runtime", AffCode: "dragonfly-auth-runtime", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		authentication := service.AuthenticationRuntime()
		login, err := authentication.CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
		require.NoError(t, err)
		identity, err := service.ParseAccessToken(login.AccessToken)
		require.NoError(t, err)
		_, _, err = authentication.ValidateLoginSession(identity)
		require.NoError(t, err)
		refreshed, _, err := authentication.RefreshLoginSession(login.RefreshToken, login.Session.SID, "127.0.0.1", "browser")
		require.NoError(t, err)
		assert.NotEqual(t, login.RefreshToken, refreshed.RefreshToken)
		recovered, _, err := authentication.RefreshLoginSession(login.RefreshToken, login.Session.SID, "127.0.0.1", "browser")
		require.NoError(t, err)
		assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken)
		require.NoError(t, authentication.RevokeByRefreshToken(recovered.RefreshToken, login.Session.SID, "logout"))
		_, _, err = authentication.ValidateLoginSession(identity)
		require.ErrorIs(t, err, service.ErrLoginSessionRevoked)
	})

	t.Run("user metadata publication preserves quota and rejects stale security", func(t *testing.T) {
		user := model.User{Username: "dragonfly-usercache", AffCode: "dragonfly-usercache", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		cache := usercache.New(database)
		_, err := cache.GetUserCache(user.Id)
		require.NoError(t, err)
		stale := entity.User(user)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		assert.True(t, reserved)
		stale.Username = "metadata-updated"
		require.NoError(t, cache.Publish(stale))
		current, err := cache.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 93, current.Quota)
		assert.Equal(t, "metadata-updated", current.Username)
		require.NoError(t, database.Transaction(func(tx *gorm.DB) error {
			if _, err := cache.IncrementUserAuthVersionWithTx(tx, user.Id); err != nil {
				return err
			}
			return tx.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error
		}))
		require.NoError(t, cache.PublishUserAuthCache(user.Id))
		require.ErrorIs(t, cache.Publish(stale), usercache.ErrUserAuthCachePending)
		current, err = cache.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 2, current.AuthVersion)
		assert.Equal(t, common.UserStatusDisabled, current.Status)
		assert.Equal(t, 93, current.Quota)
	})

	t.Run("subscription group transitions preserve cached wallet and login session", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-subscription-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-subscription", AffCode: "dragonfly-subscription", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "subscription-browser")
		require.NoError(t, err)
		auth, err := service.ParseAccessToken(login.AccessToken)
		require.NoError(t, err)
		_, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		require.True(t, reserved)
		plan := model.SubscriptionPlan{Title: "Dragonfly subscription", Enabled: true, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, UpgradeGroup: "pro", DowngradeGroup: "default"}
		require.NoError(t, database.Create(&plan).Error)
		_, err = model.SubscriptionMemberships().AdminBindSubscription(t.Context(), user.Id, plan.Id, "")
		require.NoError(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "pro", cached.Group)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		_, _, err = service.ValidateLoginSession(auth)
		require.NoError(t, err)
		active, err := model.SubscriptionMemberships().GetAllActiveUserSubscriptions(t.Context(), user.Id)
		require.NoError(t, err)
		require.Len(t, active, 1)
		_, err = model.SubscriptionMemberships().AdminInvalidateUserSubscription(t.Context(), active[0].Subscription.Id)
		require.NoError(t, err)
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "default", cached.Group)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		_, _, err = service.ValidateLoginSession(auth)
		require.NoError(t, err)
	})

	t.Run("subscription catalog transactions do not publish uncommitted plans", func(t *testing.T) {
		user := model.User{Username: "dragonfly-catalog", AffCode: "dragonfly-catalog"}
		require.NoError(t, database.Create(&user).Error)
		plan := model.SubscriptionPlan{Title: "cached title", Enabled: true, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1}
		require.NoError(t, database.Create(&plan).Error)
		sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
		require.NoError(t, database.Create(&sub).Error)
		catalog := model.SubscriptionCatalog()
		cached, err := catalog.Plan(t.Context(), nil, plan.Id)
		require.NoError(t, err)
		assert.True(t, cached.Enabled)
		info, err := catalog.PlanInfo(t.Context(), sub.Id)
		require.NoError(t, err)
		assert.Equal(t, "cached title", info.PlanTitle)
		ttl, err := client.TTL(t.Context(), cachex.Namespace("new-api:subscription_plan:v1").FullKey(strconv.Itoa(plan.Id))).Result()
		require.NoError(t, err)
		assert.Greater(t, ttl, time.Duration(0))
		assert.LessOrEqual(t, ttl, 5*time.Minute)
		rollback := errors.New("catalog rollback")
		err = database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{"title": "private transaction", "enabled": false}).Error; err != nil {
				return err
			}
			transactional, err := catalog.Plan(t.Context(), tx, plan.Id)
			require.NoError(t, err)
			assert.False(t, transactional.Enabled)
			assert.Equal(t, "private transaction", transactional.Title)
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		cached, err = catalog.Plan(t.Context(), nil, plan.Id)
		require.NoError(t, err)
		assert.True(t, cached.Enabled)
		assert.Equal(t, "cached title", cached.Title)
		require.NoError(t, database.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Update("title", "committed title").Error)
		require.NoError(t, catalog.Invalidate(plan.Id))
		info, err = catalog.PlanInfo(t.Context(), sub.Id)
		require.NoError(t, err)
		assert.Equal(t, "committed title", info.PlanTitle)
	})

	t.Run("subscription balance purchase preserves other cached reservations", func(t *testing.T) {
		previousUnit := common.QuotaPerUnit
		common.QuotaPerUnit = 10
		t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
		require.NoError(t, schema.UpPostgres(pool, schema.Logs))
		user := model.User{Username: "dragonfly-subpay", AffCode: "dragonfly-subpay", Quota: 50, Group: "default", AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 2)
		require.NoError(t, err)
		require.True(t, reserved)
		plan := model.SubscriptionPlan{Title: "Balance plan", Enabled: true, PriceAmount: 1.25, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, MaxPurchasePerUser: 1, UpgradeGroup: "pro"}
		require.NoError(t, database.Create(&plan).Error)
		require.NoError(t, model.SubscriptionPayments().PurchaseWithBalance(t.Context(), user.Id, plan.Id))
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 35, user.Quota)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 35, cached.Quota)
		assert.Equal(t, "pro", cached.Group)
		assert.EqualValues(t, 1, cached.AuthVersion)
		require.Error(t, model.SubscriptionPayments().PurchaseWithBalance(t.Context(), user.Id, plan.Id))
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 35, cached.Quota)
		var count int64
		require.NoError(t, database.Model(&model.SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})

	t.Run("billing preference publication preserves pending wallet reservations", func(t *testing.T) {
		previousBatch := common.BatchUpdateEnabled
		common.BatchUpdateEnabled = true
		t.Cleanup(func() { require.NoError(t, model.FlushQuotaUpdates()); common.BatchUpdateEnabled = previousBatch })
		user := model.User{Username: "dragonfly-preference", AffCode: "dragonfly-preference", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1, Setting: `{"language":"zh","billing_preference":"subscription_first"}`}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		require.True(t, reserved)
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 100, user.Quota)
		accounts := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{PublishAuth: usercache.New(database).PublishUserAuthCache}})
		preference, err := accounts.UpdateBillingPreference(t.Context(), user.Id, "wallet_only")
		require.NoError(t, err)
		assert.Equal(t, "wallet_only", preference)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		assert.Equal(t, "wallet_only", cached.GetSetting().BillingPreference)
		assert.Equal(t, "zh", cached.GetSetting().Language)
		require.NoError(t, model.FlushQuotaUpdates())
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 93, user.Quota)
		assert.Equal(t, "wallet_only", user.GetSetting().BillingPreference)
	})

	t.Run("all topup providers preserve pending reservations and credit once", func(t *testing.T) {
		oldUnit, oldBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
		common.QuotaPerUnit, common.BatchUpdateEnabled = 10, true
		t.Cleanup(func() {
			require.NoError(t, model.FlushQuotaUpdates())
			common.QuotaPerUnit, common.BatchUpdateEnabled = oldUnit, oldBatch
		})
		require.NoError(t, schema.UpPostgres(pool, schema.Logs))
		for _, test := range []struct {
			provider string
			credit   int
		}{{"epay", 20}, {"stripe", 25}, {"creem", 2}, {"waffo", 20}, {"waffo_pancake", 20}} {
			user := model.User{Username: "df-topup-" + test.provider, AffCode: "df-topup-" + test.provider, Quota: 100, AuthVersion: 1}
			require.NoError(t, database.Create(&user).Error)
			_, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			reserved, err := model.TryReserveUserQuota(user.Id, 7)
			require.NoError(t, err)
			require.True(t, reserved)
			row := billingentity.TopUp{UserId: user.Id, Amount: 2, Money: 2.5, TradeNo: "cache-" + test.provider, PaymentProvider: test.provider, PaymentMethod: test.provider, Status: common.TopUpStatusPending}
			require.NoError(t, model.TopUpStore().Create(t.Context(), &row))
			customer := "cus_cache"
			input := billingcontract.TopUpCompletion{TradeNo: row.TradeNo, Provider: test.provider, StripeCustomerID: &customer, CustomerEmail: "payer@example.test"}
			store := model.TopUpStore()
			done, err := store.Complete(t.Context(), input)
			require.NoError(t, err)
			assert.False(t, done)
			done, err = store.Complete(t.Context(), input)
			require.NoError(t, err)
			assert.True(t, done)
			cached, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 93+test.credit, cached.Quota)
			assert.EqualValues(t, 1, cached.AuthVersion)
			require.NoError(t, database.First(&user, user.Id).Error)
			assert.Equal(t, 100+test.credit, user.Quota)
			if test.provider == "creem" {
				assert.Equal(t, "payer@example.test", cached.Email)
			}
			if test.provider == "stripe" {
				assert.Equal(t, "cus_cache", user.StripeCustomer)
			}
			require.NoError(t, model.FlushQuotaUpdates())
			require.NoError(t, database.First(&user, user.Id).Error)
			assert.Equal(t, 93+test.credit, user.Quota)
		}
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
