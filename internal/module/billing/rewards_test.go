package billing_test

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/usage"
	usageentity "github.com/QuantumNous/new-api/internal/module/usage/entity"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

func TestCheckinConcurrencyMonthStatisticsAndRewardBounds(t *testing.T) {
	f := newTopupFixture(t, 10)
	pool, err := f.db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	today := time.Date(2028, 2, 29, 8, 0, 0, 0, time.FixedZone("SG", 8*3600))
	cfg := contract.RewardConfig{CheckinEnabled: true, MinQuota: 3, MaxQuota: 3, QuotaPerUnit: 10}
	var logs atomic.Int32
	svc := billing.New(billing.Dependencies{DB: f.db, RewardConfig: func() contract.RewardConfig { return cfg }, Now: func() time.Time { return today }, RewardLog: func(context.Context, int, string) { logs.Add(1) }})
	require.NoError(t, f.db.Create(&[]entity.Checkin{{UserId: f.user.Id, CheckinDate: "2028-01-31", QuotaAwarded: 1}, {UserId: f.user.Id, CheckinDate: "2028-02-28", QuotaAwarded: 4}, {UserId: f.user.Id, CheckinDate: "2028-03-01", QuotaAwarded: 8}}).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, err := svc.Checkin(t.Context(), f.user.Id); results <- err }()
	}
	close(start)
	one, two := <-results, <-results
	if one != nil {
		one, two = two, one
	}
	require.NoError(t, one)
	require.EqualError(t, two, "今日已签到")
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 13, f.user.Quota)
	assert.EqualValues(t, 1, logs.Load())
	status, err := svc.GetCheckinStatus(t.Context(), f.user.Id, "2028-02")
	require.NoError(t, err)
	assert.EqualValues(t, 4, status.Stats.TotalCheckins)
	assert.Equal(t, "16", status.Stats.TotalQuota.String())
	assert.True(t, status.Stats.CheckedInToday)
	assert.Equal(t, []contract.CheckinRecord{{CheckinDate: "2028-02-29", QuotaAwarded: 3}, {CheckinDate: "2028-02-28", QuotaAwarded: 4}}, status.Stats.Records)
	_, err = svc.GetCheckinStatus(t.Context(), f.user.Id, "2028-13")
	require.Error(t, err)
	today = today.AddDate(0, 0, 1)
	cfg.MinQuota, cfg.MaxQuota = -1, 3
	_, err = svc.Checkin(t.Context(), f.user.Id)
	require.EqualError(t, err, "签到额度配置错误")
	cfg.MinQuota, cfg.MaxQuota = 1, 1
	require.NoError(t, f.db.Model(&f.user).Update("quota", common.MaxWalletQuota).Error)
	today = today.AddDate(0, 0, 1)
	_, err = svc.Checkin(t.Context(), f.user.Id)
	require.ErrorIs(t, err, contract.ErrTopUpQuotaLimitExceeded)
	var count int64
	require.NoError(t, f.db.Model(&entity.Checkin{}).Where("checkin_date = ?", today.Format("2006-01-02")).Count(&count).Error)
	assert.Zero(t, count)
	_, err = svc.Checkin(t.Context(), 99999)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cfg.MinQuota, cfg.MaxQuota = 0, 0
	result, err := svc.Checkin(t.Context(), f.user.Id)
	require.NoError(t, err)
	assert.Zero(t, result.QuotaAwarded)
	cfg.CheckinEnabled = false
	_, err = svc.Checkin(t.Context(), f.user.Id)
	require.EqualError(t, err, "签到功能未启用")
}

func TestAffiliateTransferBoundsConcurrencyAndFieldIsolation(t *testing.T) {
	f := newTopupFixture(t, 10)
	require.NoError(t, f.db.Model(&f.user).Updates(map[string]any{"aff_quota": 60, "auth_version": 13, "setting": `{"language":"zh"}`, "remark": "keep"}).Error)
	allowed := true
	svc := billing.New(billing.Dependencies{DB: f.db, PaymentAllowed: func() bool { return allowed }, RewardConfig: func() contract.RewardConfig { return contract.RewardConfig{QuotaPerUnit: 10} }})
	require.Error(t, svc.TransferAffiliate(t.Context(), f.user.Id, -1))
	require.Error(t, svc.TransferAffiliate(t.Context(), f.user.Id, common.MaxWalletQuota+1))
	require.Error(t, svc.TransferAffiliate(t.Context(), f.user.Id, 9))
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- svc.TransferAffiliate(t.Context(), f.user.Id, 50) }()
	}
	close(start)
	one, two := <-results, <-results
	if one != nil {
		one, two = two, one
	}
	require.NoError(t, one)
	require.EqualError(t, two, "邀请额度不足！")
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 60, f.user.Quota)
	assert.Equal(t, 10, f.user.AffQuota)
	assert.EqualValues(t, 13, f.user.AuthVersion)
	assert.Equal(t, "keep", f.user.Remark)
	assert.Equal(t, `{"language":"zh"}`, f.user.Setting)
	require.NoError(t, f.db.Model(&f.user).Update("quota", common.MaxWalletQuota).Error)
	require.ErrorIs(t, svc.TransferAffiliate(t.Context(), f.user.Id, 10), contract.ErrTopUpQuotaLimitExceeded)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 10, f.user.AffQuota)
	allowed = false
	require.ErrorIs(t, svc.TransferAffiliate(t.Context(), f.user.Id, 10), billing.ErrPaymentComplianceRequired)
}

func TestBillingStatementsPreserveUnitsOwnershipAndLargeTotals(t *testing.T) {
	f := newTopupFixture(t, 10)
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	require.NoError(t, f.db.Model(&f.user).Updates(map[string]any{"quota": 100, "used_quota": 20}).Error)
	token := identityentity.Token{UserId: f.user.Id, Key: "statement-key", Name: "statement", RemainQuota: 100, UsedQuota: 20, ExpiredTime: -1}
	require.NoError(t, f.db.Create(&token).Error)
	cfg := contract.StatementConfig{QuotaPerUnit: 10, ExchangeRate: 7}
	svc := billing.New(billing.Dependencies{DB: f.db, StatementConfig: func() contract.StatementConfig { return cfg }})
	for _, mode := range []bool{false, true} {
		cfg.TokenStats = mode
		for _, tc := range []struct {
			display      string
			limit, usage float64
		}{{"USD", 12, 200}, {"CNY", 84, 1400}, {"TOKENS", 120, 2000}} {
			cfg.DisplayType = tc.display
			limits, err := svc.DashboardSubscription(t.Context(), f.user.Id, token.Id)
			require.NoError(t, err)
			used, err := svc.DashboardUsage(t.Context(), f.user.Id, token.Id)
			require.NoError(t, err)
			assert.Equal(t, tc.limit, limits.HardLimitUSD)
			assert.Equal(t, tc.usage, used.TotalUsage)
			assert.Zero(t, limits.AccessUntil)
		}
	}
	_, err := svc.DashboardSubscription(t.Context(), 99999, token.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, f.db.Model(&token).Update("unlimited_quota", true).Error)
	limit, err := svc.DashboardSubscription(t.Context(), f.user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, 100000000.0, limit.HardLimitUSD)
	require.NoError(t, f.db.Model(&token).Updates(map[string]any{"remain_quota": int64(math.MaxInt64), "used_quota": int64(math.MaxInt64), "unlimited_quota": false}).Error)
	report, err := svc.TokenUsage(t.Context(), token.Key)
	require.NoError(t, err)
	assert.Equal(t, "18446744073709551614", report.TotalGranted.String())
	limit, err = svc.DashboardSubscription(t.Context(), f.user.Id, token.Id)
	require.NoError(t, err)
	assert.Greater(t, limit.HardLimitUSD, 0.0)
	cfg.DisplayType = "USD"
	cfg.QuotaPerUnit = 0
	_, err = svc.DashboardUsage(t.Context(), f.user.Id, token.Id)
	require.Error(t, err)
	handler := billinghttp.New(svc, billinghttp.ManagementHooks{})
	response := httptest.NewRecorder()
	ctx := httptest.NewRequest(http.MethodGet, "/usage", nil)
	router := gin.New()
	router.GET("/usage", handler.GetTokenUsage)
	router.ServeHTTP(response, ctx)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	require.NoError(t, f.db.Delete(&token).Error)
	_, err = svc.DashboardSubscription(t.Context(), f.user.Id, token.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCheckinAuditUsesConfiguredPostgresOrClickHouseLogs(t *testing.T) {
	for _, kind := range []common.DatabaseType{common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse} {
		t.Run(string(kind), func(t *testing.T) {
			f := newTopupFixture(t, 10)
			pool, err := f.db.DB()
			require.NoError(t, err)
			logsDB := f.db
			if kind == common.DatabaseTypePostgreSQL {
				require.NoError(t, schema.UpPostgres(pool, schema.Logs))
			} else {
				dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
				if dsn == "" {
					t.Skip("TEST_CLICKHOUSE_DSN is required")
				}
				admin, err := sql.Open("clickhouse", dsn)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, admin.Close()) })
				name := "checkin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				_, err = admin.Exec("CREATE DATABASE " + name)
				require.NoError(t, err)
				t.Cleanup(func() { _, err := admin.Exec("DROP DATABASE " + name); require.NoError(t, err) })
				parsed, err := url.Parse(dsn)
				require.NoError(t, err)
				parsed.Path = "/" + name
				require.NoError(t, schema.UpClickHouse(parsed.String(), pool))
				logsDB, err = gorm.Open(clickhouse.Open(parsed.String()), &gorm.Config{})
				require.NoError(t, err)
				t.Cleanup(func() { pool, err := logsDB.DB(); require.NoError(t, err); require.NoError(t, pool.Close()) })
			}
			writer := usage.New(usage.Dependencies{DB: logsDB, Kind: kind})
			svc := billing.New(billing.Dependencies{DB: f.db, RewardConfig: func() contract.RewardConfig {
				return contract.RewardConfig{CheckinEnabled: true, MinQuota: 5, MaxQuota: 5}
			}, RewardLog: func(ctx context.Context, id int, message string) {
				writer.RecordLog(ctx, id, usageentity.LogTypeSystem, message)
			}})
			_, err = svc.Checkin(t.Context(), f.user.Id)
			require.NoError(t, err)
			_, err = svc.Checkin(t.Context(), f.user.Id)
			require.Error(t, err)
			var logs []usage.Log
			require.NoError(t, logsDB.Where("user_id = ? AND type = ?", f.user.Id, usageentity.LogTypeSystem).Find(&logs).Error)
			require.Len(t, logs, 1)
			assert.Contains(t, logs[0].Content, "用户签到，获得额度")
		})
	}
}
