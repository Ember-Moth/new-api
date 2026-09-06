package subscription_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newQuotaFixture(t *testing.T) (*gorm.DB, entity.UserSubscription, *quota.Store, *catalog.Store) {
	t.Helper()
	db, _ := newMembershipStore(t)
	user := identityentity.User{Username: "quota-user", Password: "fixture"}
	require.NoError(t, db.Create(&user).Error)
	plan := entity.SubscriptionPlan{Title: "quota plan", PriceAmount: 1, QuotaResetPeriod: entity.SubscriptionResetNever}
	require.NoError(t, db.Create(&plan).Error)
	now := common.GetTimestamp()
	sub := entity.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, StartTime: now - 10, EndTime: now + 3600, NextResetTime: now + 1800, Status: "active"}
	require.NoError(t, db.Create(&sub).Error)
	plans := catalog.New(catalog.Dependencies{DB: db})
	return db, sub, quota.New(db, plans), plans
}

func TestSubscriptionConcurrentDuplicateReservationChargesOnce(t *testing.T) {
	db, sub, store, _ := newQuotaFixture(t)
	start := make(chan struct{})
	type outcome struct {
		result *contract.SubscriptionPreConsumeResult
		err    error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.PreConsumeUserSubscription(t.Context(), "same-request", sub.UserId, "model", 0, 30)
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
	require.NoError(t, db.Model(&entity.SubscriptionPreConsumeRecord{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestSubscriptionRefundFailureRollsBackUsageAndReservationTogether(t *testing.T) {
	db, sub, store, _ := newQuotaFixture(t)
	_, err := store.PreConsumeUserSubscription(t.Context(), "refund-request", sub.UserId, "model", 0, 30)
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
	require.Error(t, store.RefundSubscriptionPreConsume(t.Context(), "refund-request"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 30, sub.AmountUsed)
	var record entity.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", "refund-request").First(&record).Error)
	assert.Equal(t, "consumed", record.Status)
	require.NoError(t, db.Exec("DROP FUNCTION fail_subscription_refund() CASCADE").Error)
	require.NoError(t, store.RefundSubscriptionPreConsume(t.Context(), "refund-request"))
	require.NoError(t, store.RefundSubscriptionPreConsume(t.Context(), "refund-request"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
}

func TestSubscriptionUsageOverflowCannotResetAccumulatedCharges(t *testing.T) {
	db, sub, store, _ := newQuotaFixture(t)
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_total": 0, "amount_used": int64(math.MaxInt64 - 1)}).Error)
	require.Error(t, store.PostConsumeUserSubscriptionDelta(t.Context(), sub.Id, 10))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, math.MaxInt64-1, sub.AmountUsed)
}

func TestSubscriptionReservationAdjustmentsRemainFullyRefundable(t *testing.T) {
	db, sub, store, _ := newQuotaFixture(t)
	_, err := store.PreConsumeUserSubscription(t.Context(), "adjusted", sub.UserId, "model", 0, 30)
	require.NoError(t, err)
	adjusted, err := store.AdjustSubscriptionPreConsume(t.Context(), "adjusted", 20)
	require.NoError(t, err)
	assert.EqualValues(t, 50, adjusted.PreConsumed)
	assert.EqualValues(t, 30, adjusted.AmountUsedBefore)
	assert.EqualValues(t, 50, adjusted.AmountUsedAfter)
	adjusted, err = store.AdjustSubscriptionPreConsume(t.Context(), "adjusted", -10)
	require.NoError(t, err)
	assert.EqualValues(t, 40, adjusted.PreConsumed)
	_, err = store.PreConsumeUserSubscription(t.Context(), "adjusted", sub.UserId+1, "model", 0, 30)
	require.Error(t, err)
	_, err = store.AdjustSubscriptionPreConsume(t.Context(), "adjusted", math.MaxInt64)
	require.Error(t, err)
	require.NoError(t, store.RefundSubscriptionPreConsume(t.Context(), "adjusted"))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	_, err = store.AdjustSubscriptionPreConsume(t.Context(), "adjusted", 1)
	require.Error(t, err)
}

func TestSubscriptionReservationUsesNextEligiblePlanAndResetsDueQuota(t *testing.T) {
	db, first, store, plans := newQuotaFixture(t)
	require.NoError(t, db.Model(&first).Update("amount_used", 80).Error)
	second := first
	second.Id = 0
	second.EndTime += 3600
	second.AmountUsed = 0
	require.NoError(t, db.Create(&second).Error)
	reservation, err := store.PreConsumeUserSubscription(t.Context(), "next-plan", first.UserId, "model", 0, 30)
	require.NoError(t, err)
	assert.Equal(t, second.Id, reservation.UserSubscriptionId)
	require.NoError(t, db.First(&first, first.Id).Error)
	assert.EqualValues(t, 80, first.AmountUsed)

	now := common.GetTimestamp()
	require.NoError(t, db.Model(&entity.SubscriptionPlan{}).Where("id = ?", first.PlanId).
		Updates(map[string]any{"quota_reset_period": entity.SubscriptionResetCustom, "quota_reset_custom_seconds": 60}).Error)
	require.NoError(t, plans.Invalidate(first.PlanId))
	require.NoError(t, db.Model(&first).Updates(map[string]any{"last_reset_time": now - 120, "next_reset_time": now - 60}).Error)
	reservation, err = store.PreConsumeUserSubscription(t.Context(), "reset-plan", first.UserId, "model", 0, 30)
	require.NoError(t, err)
	assert.Equal(t, first.Id, reservation.UserSubscriptionId)
	assert.Zero(t, reservation.AmountUsedBefore)
	assert.EqualValues(t, 30, reservation.AmountUsedAfter)
	require.NoError(t, db.First(&first, first.Id).Error)
	assert.Greater(t, first.NextResetTime, now)
	_, err = store.PreConsumeUserSubscription(t.Context(), "oversized", first.UserId, "model", 0, int64(common.MaxQuota)+1)
	require.Error(t, err)
}
func TestSubscriptionCatalogTransactionIsolationAndInvalidation(t *testing.T) {
	db, sub, _, plans := newQuotaFixture(t)
	cached, err := plans.Plan(t.Context(), nil, sub.PlanId)
	require.NoError(t, err)
	assert.Equal(t, "quota plan", cached.Title)
	info, err := plans.PlanInfo(t.Context(), sub.Id)
	require.NoError(t, err)
	assert.Equal(t, "quota plan", info.PlanTitle)
	rollback := errors.New("rollback catalog transaction")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.SubscriptionPlan{}).Where("id = ?", sub.PlanId).Update("title", "uncommitted").Error; err != nil {
			return err
		}
		transactional, err := plans.Plan(t.Context(), tx, sub.PlanId)
		require.NoError(t, err)
		assert.Equal(t, "uncommitted", transactional.Title)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	after, err := plans.Plan(t.Context(), nil, sub.PlanId)
	require.NoError(t, err)
	assert.Equal(t, "quota plan", after.Title)
	require.NoError(t, db.Model(&entity.SubscriptionPlan{}).Where("id = ?", sub.PlanId).Update("title", "committed").Error)
	require.NoError(t, plans.Invalidate(sub.PlanId))
	info, err = plans.PlanInfo(t.Context(), sub.Id)
	require.NoError(t, err)
	assert.Equal(t, "committed", info.PlanTitle)
	// No cache publication is allowed even when the first read of a new plan is
	// inside a transaction that subsequently rolls back.
	var transient entity.SubscriptionPlan
	err = db.Transaction(func(tx *gorm.DB) error {
		transient = entity.SubscriptionPlan{Title: "transient"}
		if err := tx.Create(&transient).Error; err != nil {
			return err
		}
		_, err := plans.Plan(t.Context(), tx, transient.Id)
		require.NoError(t, err)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	_, err = plans.Plan(t.Context(), nil, transient.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSubscriptionResetUsesFreshPlanAndCountsCommittedChanges(t *testing.T) {
	db, sub, store, plans := newQuotaFixture(t)
	now := common.GetTimestamp()
	require.NoError(t, db.Model(&entity.SubscriptionPlan{}).Where("id = ?", sub.PlanId).Updates(map[string]any{"quota_reset_period": "custom", "quota_reset_custom_seconds": 60}).Error)
	_, err := plans.Plan(t.Context(), nil, sub.PlanId)
	require.NoError(t, err)
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_used": 80, "last_reset_time": now - 120, "next_reset_time": now - 60}).Error)
	// The display cache still says custom. The transaction must honor the
	// newly disabled reset schedule and retire its old due timestamp.
	require.NoError(t, db.Model(&entity.SubscriptionPlan{}).Where("id = ?", sub.PlanId).Update("quota_reset_period", "never").Error)
	count, err := store.ResetDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 80, sub.AmountUsed)
	assert.Zero(t, sub.NextResetTime)
	count, err = store.ResetDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&entity.SubscriptionPlan{}).Where("id = ?", sub.PlanId).Updates(map[string]any{"quota_reset_period": "custom", "quota_reset_custom_seconds": 60}).Error)
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"last_reset_time": now - 120, "next_reset_time": now - 60}).Error)
	require.NoError(t, db.Exec(`ALTER TABLE user_subscriptions ADD CONSTRAINT prevent_reset_fixture CHECK (amount_used > 0)`).Error)
	count, err = store.ResetDueSubscriptions(t.Context(), 10)
	require.Error(t, err)
	assert.Zero(t, count)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 80, sub.AmountUsed)
	require.NoError(t, db.Exec("ALTER TABLE user_subscriptions DROP CONSTRAINT prevent_reset_fixture").Error)
	count, err = store.ResetDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	assert.Greater(t, sub.NextResetTime, now)
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_used": 20, "end_time": now - 1, "next_reset_time": now - 60}).Error)
	count, err = store.ResetDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Zero(t, count)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 20, sub.AmountUsed)
}

func TestSubscriptionMaintenanceCleanupAndCancellation(t *testing.T) {
	db, members := newMembershipStore(t)
	plans := catalog.New(catalog.Dependencies{DB: db})
	quotas := quota.New(db, plans)
	runtime := subscription.New(subscription.Dependencies{DB: db, Members: members, Quota: quotas})
	now := common.GetTimestamp()
	// Fixtures use committed old timestamps so cleanup exercises the real SQL
	// retention predicate instead of entity hooks stamping the current time.
	require.NoError(t, db.Exec(`INSERT INTO subscription_pre_consume_records (request_id,user_id,user_subscription_id,pre_consumed,status,created_at,updated_at) VALUES ('old',1,1,0,'refunded',?,?),('recent',1,1,0,'refunded',?,?)`, now-8*86400, now-8*86400, now, now).Error)
	require.NoError(t, runtime.RunMaintenance(t.Context()))
	var records []entity.SubscriptionPreConsumeRecord
	require.NoError(t, db.Find(&records).Error)
	require.Len(t, records, 1)
	assert.Equal(t, "recent", records[0].RequestId)
	ctx, cancel := context.WithCancel(t.Context())
	done := runtime.StartMaintenance(ctx, true)
	assert.Equal(t, done, runtime.StartMaintenance(ctx, true))
	cancel()
	<-done
	require.ErrorIs(t, runtime.RunMaintenance(ctx), context.Canceled)
	replica := subscription.New(subscription.Dependencies{})
	<-replica.StartMaintenance(t.Context(), false)
	cutoff, err := quotas.CleanupSubscriptionPreConsumeRecords(t.Context(), math.MaxInt64)
	require.NoError(t, err)
	assert.Zero(t, cutoff)
}
