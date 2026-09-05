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
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/gin-gonic/gin"
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
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}))

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
