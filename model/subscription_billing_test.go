package model_test

import (
	"context"
	"math"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useSubscriptionBillingDB(t *testing.T) (*gorm.DB, model.UserSubscription) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	previousDB, previousLog, previousRedis := model.DB, model.LOG_DB, common.RedisEnabled
	model.DB, model.LOG_DB, common.RedisEnabled = db, db, false
	t.Cleanup(func() { model.DB, model.LOG_DB, common.RedisEnabled = previousDB, previousLog, previousRedis })
	user := model.User{Username: "subscription-billing-user", Password: "fixture"}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{Title: "quota plan", PriceAmount: 1, QuotaResetPeriod: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&plan).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(plan.Id) })
	now := common.GetTimestamp()
	sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, StartTime: now - 10, EndTime: now + 3600, NextResetTime: now + 1800, Status: "active"}
	require.NoError(t, db.Create(&sub).Error)
	return db, sub
}

func TestSubscriptionConcurrentDuplicateReservationChargesOnce(t *testing.T) {
	db, sub := useSubscriptionBillingDB(t)
	start := make(chan struct{})
	type outcome struct {
		result *model.SubscriptionPreConsumeResult
		err    error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := model.PreConsumeUserSubscription("same-request", sub.UserId, "model", 0, 30)
			results <- outcome{result, err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		require.NotNil(t, result.result)
		assert.Equal(t, sub.Id, result.result.UserSubscriptionId)
		assert.EqualValues(t, 30, result.result.PreConsumed)
	}
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 30, sub.AmountUsed)
	var count int64
	require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestSubscriptionRefundFailureRollsBackUsageAndReservationTogether(t *testing.T) {
	db, sub := useSubscriptionBillingDB(t)
	_, err := model.PreConsumeUserSubscription("refund-request", sub.UserId, "model", 0, 30)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE FUNCTION fail_subscription_refund() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'refunded' THEN RAISE EXCEPTION 'injected refund failure'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER fail_subscription_refund BEFORE UPDATE ON subscription_pre_consume_records
FOR EACH ROW EXECUTE FUNCTION fail_subscription_refund();`).Error)
	require.Error(t, model.RefundSubscriptionPreConsume("refund-request"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 30, sub.AmountUsed)
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", "refund-request").First(&record).Error)
	assert.Equal(t, "consumed", record.Status)
	require.NoError(t, db.Exec("DROP FUNCTION fail_subscription_refund() CASCADE").Error)
	require.NoError(t, model.RefundSubscriptionPreConsume("refund-request"))
	require.NoError(t, model.RefundSubscriptionPreConsume("refund-request"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
}

func TestSubscriptionUsageOverflowCannotResetAccumulatedCharges(t *testing.T) {
	db, sub := useSubscriptionBillingDB(t)
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_total": 0, "amount_used": int64(math.MaxInt64 - 1)}).Error)
	require.Error(t, model.PostConsumeUserSubscriptionDelta(sub.Id, 10))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, math.MaxInt64-1, sub.AmountUsed)
}

func TestSubscriptionReservationAdjustmentsRemainFullyRefundable(t *testing.T) {
	db, sub := useSubscriptionBillingDB(t)
	_, err := model.PreConsumeUserSubscription("adjusted", sub.UserId, "model", 0, 30)
	require.NoError(t, err)
	adjusted, err := model.AdjustSubscriptionPreConsume("adjusted", 20)
	require.NoError(t, err)
	assert.EqualValues(t, 50, adjusted.PreConsumed)
	assert.EqualValues(t, 30, adjusted.AmountUsedBefore)
	assert.EqualValues(t, 50, adjusted.AmountUsedAfter)
	adjusted, err = model.AdjustSubscriptionPreConsume("adjusted", -10)
	require.NoError(t, err)
	assert.EqualValues(t, 40, adjusted.PreConsumed)
	_, err = model.PreConsumeUserSubscription("adjusted", sub.UserId+1, "model", 0, 30)
	require.Error(t, err)
	_, err = model.AdjustSubscriptionPreConsume("adjusted", math.MaxInt64)
	require.Error(t, err)
	require.NoError(t, model.RefundSubscriptionPreConsume("adjusted"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	_, err = model.AdjustSubscriptionPreConsume("adjusted", 1)
	require.Error(t, err)
}

func TestSubscriptionReservationUsesNextEligiblePlanAndResetsDueQuota(t *testing.T) {
	db, first := useSubscriptionBillingDB(t)
	require.NoError(t, db.Model(&first).Update("amount_used", 80).Error)
	second := first
	second.Id = 0
	second.EndTime += 3600
	second.AmountUsed = 0
	require.NoError(t, db.Create(&second).Error)
	reservation, err := model.PreConsumeUserSubscription("next-plan", first.UserId, "model", 0, 30)
	require.NoError(t, err)
	assert.Equal(t, second.Id, reservation.UserSubscriptionId)
	require.NoError(t, db.First(&first, first.Id).Error)
	assert.EqualValues(t, 80, first.AmountUsed)

	now := common.GetTimestamp()
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Where("id = ?", first.PlanId).
		Updates(map[string]any{"quota_reset_period": model.SubscriptionResetCustom, "quota_reset_custom_seconds": 60}).Error)
	model.InvalidateSubscriptionPlanCache(first.PlanId)
	require.NoError(t, db.Model(&first).Updates(map[string]any{"last_reset_time": now - 120, "next_reset_time": now - 60}).Error)
	reservation, err = model.PreConsumeUserSubscription("reset-plan", first.UserId, "model", 0, 30)
	require.NoError(t, err)
	assert.Equal(t, first.Id, reservation.UserSubscriptionId)
	assert.Zero(t, reservation.AmountUsedBefore)
	assert.EqualValues(t, 30, reservation.AmountUsedAfter)
	require.NoError(t, db.First(&first, first.Id).Error)
	assert.Greater(t, first.NextResetTime, now)
	_, err = model.PreConsumeUserSubscription("oversized", first.UserId, "model", 0, int64(common.MaxQuota)+1)
	require.Error(t, err)
}

func TestBillingSessionRefundIncludesExtraReservationExactlyOnce(t *testing.T) {
	db, sub := useSubscriptionBillingDB(t)
	token := model.Token{UserId: sub.UserId, Key: "subscription-billing-session", RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		RequestId: "session-refund", UserId: sub.UserId, TokenId: token.Id, TokenKey: token.Key,
		OriginModelName: "model", ForcePreConsume: true,
		UserSetting: dto.UserSetting{BillingPreference: "subscription_only"},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	session, apiErr := service.NewBillingSession(ctx, info, 30)
	require.Nil(t, apiErr)
	require.NoError(t, session.Reserve(50))
	assert.EqualValues(t, 50, info.SubscriptionPreConsumed)
	assert.EqualValues(t, 50, info.SubscriptionAmountUsedAfterPreConsume)
	// Another request's settled usage must survive this request's refund.
	require.NoError(t, model.PostConsumeUserSubscriptionDelta(sub.Id, 10))

	listener, err := pgx.Connect(t.Context(), os.Getenv("TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close(context.Background())) })
	channel := "refund_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = listener.Exec(t.Context(), "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)
	// NOTIFY is delivered after the final token-credit transaction commits.
	require.NoError(t, db.Exec(`CREATE FUNCTION notify_billing_refund() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.remain_quota > OLD.remain_quota THEN PERFORM pg_notify('`+channel+`', 'done'); END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER notify_billing_refund AFTER UPDATE ON tokens FOR EACH ROW EXECUTE FUNCTION notify_billing_refund();`).Error)
	session.Refund(ctx)
	wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = listener.WaitForNotification(wait)
	require.NoError(t, err)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 10, sub.AmountUsed)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.False(t, session.NeedsRefund())
}
