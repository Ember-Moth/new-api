package model_test

import (
	"context"
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
