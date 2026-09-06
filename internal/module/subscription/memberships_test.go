package subscription_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newMembershipStore(t *testing.T) (*gorm.DB, *memberships.Store) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	accounts := identity.New(identity.Dependencies{DB: db})
	store := memberships.New(memberships.Dependencies{DB: db, Plan: func(tx *gorm.DB, id int) (*entity.SubscriptionPlan, error) {
		if tx == nil {
			tx = db
		}
		var plan entity.SubscriptionPlan
		err := tx.First(&plan, id).Error
		plan.NormalizeDefaults()
		return &plan, err
	}, Groups: memberships.UserGroups{Lock: accounts.LockUserGroup, Set: accounts.SetUserGroup, Refresh: func(int) error { return nil }}})
	return db, store
}

func seedSubscriptionResetPlan(t *testing.T, db *gorm.DB, plan *entity.SubscriptionPlan) {
	t.Helper()
	require.NoError(t, db.Create(plan).Error)
}

func seedSubscriptionResetSub(t *testing.T, db *gorm.DB, sub *entity.UserSubscription) {
	t.Helper()
	require.NoError(t, db.Create(sub).Error)
}

func getSubscriptionResetSub(t *testing.T, db *gorm.DB, id int) entity.UserSubscription {
	t.Helper()
	var sub entity.UserSubscription
	require.NoError(t, db.Where("id = ?", id).First(&sub).Error)
	return sub
}

func TestAdminResetUserSubscriptionsByPlanResetsAllActiveMatchesAndAdvancesTime(t *testing.T) {
	db, store := newMembershipStore(t)

	now := common.GetTimestamp()
	plan := &entity.SubscriptionPlan{
		Id:               9101,
		Title:            "Pro",
		PriceAmount:      10,
		DurationUnit:     entity.SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      1000,
		QuotaResetPeriod: entity.SubscriptionResetDaily,
	}
	otherPlan := &entity.SubscriptionPlan{
		Id:               9102,
		Title:            "Basic",
		PriceAmount:      1,
		DurationUnit:     entity.SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      100,
		QuotaResetPeriod: entity.SubscriptionResetDaily,
	}
	seedSubscriptionResetPlan(t, db, plan)
	seedSubscriptionResetPlan(t, db, otherPlan)

	activeEnd := now + 30*24*3600
	expiredEnd := now - 1
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9201, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 300, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9202, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9203, UserId: 101, PlanId: otherPlan.Id, AmountTotal: 100, AmountUsed: 60, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9204, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 700, StartTime: now - 7200, EndTime: expiredEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9205, UserId: 102, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 800, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9206, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 900, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 120})

	beforeReset := membershipTimestamp(t, db)
	result, err := store.AdminResetUserSubscriptionsByPlan(t.Context(), 101, plan.Id, true)
	afterReset := membershipTimestamp(t, db)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, plan.Id, result.PlanId)
	assert.Equal(t, 2, result.MatchedCount)
	assert.Equal(t, 2, result.ResetCount)
	assert.Equal(t, 1, result.UserCount)
	assert.Equal(t, []int{101}, result.AffectedUserIds)
	assert.True(t, result.AdvanceResetTime)

	for _, id := range []int{9201, 9202} {
		sub := getSubscriptionResetSub(t, db, id)
		assert.Zero(t, sub.AmountUsed)
		assert.GreaterOrEqual(t, sub.LastResetTime, beforeReset)
		assert.LessOrEqual(t, sub.LastResetTime, afterReset)
		assert.Equal(t, plan.NextResetTime(time.Unix(sub.LastResetTime, 0), sub.EndTime), sub.NextResetTime)
	}
	assert.EqualValues(t, 60, getSubscriptionResetSub(t, db, 9203).AmountUsed)
	assert.EqualValues(t, 700, getSubscriptionResetSub(t, db, 9204).AmountUsed)
	assert.EqualValues(t, 800, getSubscriptionResetSub(t, db, 9205).AmountUsed)
	assert.EqualValues(t, 900, getSubscriptionResetSub(t, db, 9206).AmountUsed)
}

func TestAdminResetUserSubscriptionsByPlanKeepsResetTimes(t *testing.T) {
	db, store := newMembershipStore(t)

	now := common.GetTimestamp()
	plan := &entity.SubscriptionPlan{
		Id:               9301,
		Title:            "Team",
		PriceAmount:      20,
		DurationUnit:     entity.SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      2000,
		QuotaResetPeriod: entity.SubscriptionResetMonthly,
	}
	seedSubscriptionResetPlan(t, db, plan)

	lastReset := now - 86400
	nextReset := now + 86400
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9302, UserId: 201, PlanId: plan.Id, AmountTotal: 2000, AmountUsed: 1200, StartTime: now - 172800, EndTime: now + 30*24*3600, Status: "active", LastResetTime: lastReset, NextResetTime: nextReset})

	result, err := store.AdminResetUserSubscriptionsByPlan(t.Context(), 201, plan.Id, false)

	require.NoError(t, err)
	assert.False(t, result.AdvanceResetTime)
	sub := getSubscriptionResetSub(t, db, 9302)
	assert.Zero(t, sub.AmountUsed)
	assert.Equal(t, lastReset, sub.LastResetTime)
	assert.Equal(t, nextReset, sub.NextResetTime)
}

func TestAdminResetUserSubscriptionsByPlanNoActiveMatchReturnsError(t *testing.T) {
	db, store := newMembershipStore(t)

	now := common.GetTimestamp()
	plan := &entity.SubscriptionPlan{
		Id:            9401,
		Title:         "Expired",
		PriceAmount:   10,
		DurationUnit:  entity.SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, db, plan)
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9402, UserId: 301, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 7200, EndTime: now - 1, Status: "active"})

	result, err := store.AdminResetUserSubscriptionsByPlan(t.Context(), 301, plan.Id, true)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "该用户没有有效的此套餐订阅"))
}

func TestAdminResetPlanSubscriptionsResetsAllActiveUsers(t *testing.T) {
	db, store := newMembershipStore(t)

	now := common.GetTimestamp()
	plan := &entity.SubscriptionPlan{
		Id:               9501,
		Title:            "Business",
		PriceAmount:      30,
		DurationUnit:     entity.SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      3000,
		QuotaResetPeriod: entity.SubscriptionResetNever,
	}
	seedSubscriptionResetPlan(t, db, plan)

	activeEnd := now + 30*24*3600
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9502, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1000, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9503, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1100, StartTime: now - 3500, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9504, UserId: 402, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1200, StartTime: now - 3400, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9505, UserId: 403, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1300, StartTime: now - 7200, EndTime: now - 1, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, db, &entity.UserSubscription{Id: 9506, UserId: 404, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1400, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 10})

	result, err := store.AdminResetPlanSubscriptions(t.Context(), plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.MatchedCount)
	assert.Equal(t, 3, result.ResetCount)
	assert.Equal(t, 2, result.UserCount)
	assert.Equal(t, []int{401, 402}, result.AffectedUserIds)
	for _, id := range []int{9502, 9503, 9504} {
		sub := getSubscriptionResetSub(t, db, id)
		assert.Zero(t, sub.AmountUsed)
		assert.Zero(t, sub.LastResetTime)
		assert.Zero(t, sub.NextResetTime)
	}
	assert.EqualValues(t, 1300, getSubscriptionResetSub(t, db, 9505).AmountUsed)
	assert.EqualValues(t, 1400, getSubscriptionResetSub(t, db, 9506).AmountUsed)
}

func TestAdminResetPlanSubscriptionsNoMatchSucceeds(t *testing.T) {
	db, store := newMembershipStore(t)

	plan := &entity.SubscriptionPlan{
		Id:            9601,
		Title:         "Empty",
		PriceAmount:   10,
		DurationUnit:  entity.SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, db, plan)

	result, err := store.AdminResetPlanSubscriptions(t.Context(), plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.MatchedCount)
	assert.Zero(t, result.ResetCount)
	assert.Zero(t, result.UserCount)
	assert.Empty(t, result.AffectedUserIds)
}

func TestMembershipGrantsSerializeLimitsAndPreserveOverlappingGroups(t *testing.T) {
	db, store := newMembershipStore(t)
	user := identityentity.User{Username: "membership-user", Password: "unused", Group: "default", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	noOverflow := false
	plan := entity.SubscriptionPlan{Title: "Limited", Enabled: true, DurationUnit: entity.SubscriptionDurationDay, DurationValue: 30, TotalAmount: 100, MaxPurchasePerUser: 1, UpgradeGroup: "pro", DowngradeGroup: "default", AllowWalletOverflow: &noOverflow}
	require.NoError(t, db.Create(&plan).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := store.AdminBindSubscription(t.Context(), user.Id, plan.Id, "")
			results <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	require.True(t, (first == nil) != (second == nil), "exactly one grant must commit: %v / %v", first, second)
	all, err := store.GetAllUserSubscriptions(t.Context(), user.Id)
	require.NoError(t, err)
	require.Len(t, all, 1)
	granted := all[0].Subscription
	assert.Equal(t, "default", granted.PrevUserGroup)
	assert.Equal(t, int64(100), granted.AmountTotal)
	overflow, err := store.UserActiveSubscriptionsAllowWalletOverflow(t.Context(), user.Id)
	require.NoError(t, err)
	assert.False(t, overflow)
	var updated identityentity.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, "pro", updated.Group)
	assert.EqualValues(t, 1, updated.AuthVersion)
	another := plan
	another.Id = 0
	another.Title = "Additional"
	another.MaxPurchasePerUser = 0
	require.NoError(t, db.Create(&another).Error)
	// Both interleavings leave the newly granted upgrade active, and share the
	// user-before-subscription lock order instead of deadlocking.
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := store.AdminBindSubscription(t.Context(), user.Id, another.Id, "")
		assert.NoError(t, err)
	})
	wg.Go(func() {
		_, err := store.AdminInvalidateUserSubscription(t.Context(), granted.Id)
		assert.NoError(t, err)
	})
	wg.Wait()
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, "pro", updated.Group)
	active, err := store.GetAllActiveUserSubscriptions(t.Context(), user.Id)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, another.Id, active[0].Subscription.PlanId)
	message, err := store.AdminDeleteUserSubscription(t.Context(), active[0].Subscription.Id)
	require.NoError(t, err)
	assert.Contains(t, message, "default")
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, "default", updated.Group)
	hasActive, err := store.HasActiveUserSubscription(t.Context(), user.Id)
	require.NoError(t, err)
	assert.False(t, hasActive)
	_, err = store.AdminBindSubscription(t.Context(), user.Id, plan.Id, "")
	assert.Error(t, err) // Lifetime purchase limit includes cancelled grants.
}

func TestMembershipFailedDowngradeRollsBackCancellationDeletionAndExpiration(t *testing.T) {
	db, store := newMembershipStore(t)
	user := identityentity.User{Username: "rollback-user", Password: "unused", Group: "pro", AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	now := common.GetTimestamp()
	sub := entity.UserSubscription{UserId: user.Id, PlanId: 1, Status: "active", StartTime: now - 3600, EndTime: now + 3600, UpgradeGroup: "pro", PrevUserGroup: "default", DowngradeGroup: "blocked", AmountTotal: 100, AmountUsed: 30}
	require.NoError(t, db.Create(&sub).Error)
	require.NoError(t, db.Exec(`ALTER TABLE users ADD CONSTRAINT reject_membership_group CHECK ("group" <> 'blocked')`).Error)
	_, err := store.AdminInvalidateUserSubscription(t.Context(), sub.Id)
	require.Error(t, err)
	var row entity.UserSubscription
	require.NoError(t, db.First(&row, sub.Id).Error)
	assert.Equal(t, "active", row.Status)
	assert.Equal(t, sub.EndTime, row.EndTime)
	_, err = store.AdminDeleteUserSubscription(t.Context(), sub.Id)
	require.Error(t, err)
	require.NoError(t, db.First(&row, sub.Id).Error)
	require.NoError(t, db.Model(&row).Update("end_time", now-1).Error)
	count, err := store.ExpireDueSubscriptions(t.Context(), 10)
	require.Error(t, err)
	assert.Zero(t, count)
	require.NoError(t, db.First(&row, sub.Id).Error)
	assert.Equal(t, "active", row.Status)
	require.NoError(t, db.Exec("ALTER TABLE users DROP CONSTRAINT reject_membership_group").Error)
	count, err = store.ExpireDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.NoError(t, db.First(&row, sub.Id).Error)
	assert.Equal(t, "expired", row.Status)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, "blocked", user.Group)
	assert.EqualValues(t, 1, user.AuthVersion)
	count, err = store.ExpireDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestMembershipHTTPComplianceAndResetAudit(t *testing.T) {
	db, store := newMembershipStore(t)
	user := identityentity.User{Username: "http-member", Password: "unused", Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	plan := entity.SubscriptionPlan{Title: "HTTP Plan", Enabled: true, DurationUnit: entity.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, QuotaResetPeriod: entity.SubscriptionResetDaily}
	require.NoError(t, db.Create(&plan).Error)
	allowed := false
	var audits []string
	var resetUsers []int
	handler := subscriptionhttp.New(subscription.New(subscription.Dependencies{DB: db, Members: store, PaymentAllowed: func() bool { return allowed }}), subscriptionhttp.ManagementHooks{
		Audit: func(c *gin.Context, id int, action string, params map[string]any) {
			audits = append(audits, action)
			assert.Equal(t, user.Id, id)
			assert.Equal(t, 1, params["reset_count"])
		},
		ResetLogs: func(c *gin.Context, result *contract.SubscriptionResetResult) {
			resetUsers = append(resetUsers, result.AffectedUserIds...)
		},
	})
	router := gin.New()
	router.POST("/bind", handler.AdminBindSubscription)
	router.POST("/users/:id/reset", handler.AdminResetUserSubscriptionsByPlan)
	router.GET("/users/:id", handler.AdminListUserSubscriptions)
	body := planRequest(t, router, http.MethodPost, "/bind", contract.AdminBindSubscriptionRequest{UserId: user.Id, PlanId: plan.Id})
	assert.False(t, body.Success)
	allowed = true
	body = planRequest(t, router, http.MethodPost, "/bind", contract.AdminBindSubscriptionRequest{UserId: user.Id, PlanId: plan.Id})
	require.True(t, body.Success, body.Message)
	var sub entity.UserSubscription
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&sub).Error)
	originalNext := common.GetTimestamp() + 120
	require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_used": 50, "next_reset_time": originalNext}).Error)
	keep := false
	body = planRequest(t, router, http.MethodPost, fmt.Sprintf("/users/%d/reset", user.Id), contract.AdminResetSubscriptionRequest{PlanId: plan.Id, AdvanceResetTime: &keep})
	require.True(t, body.Success, body.Message)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	assert.Equal(t, originalNext, sub.NextResetTime)
	body = planRequest(t, router, http.MethodPost, fmt.Sprintf("/users/%d/reset", user.Id), contract.AdminResetSubscriptionRequest{PlanId: plan.Id})
	require.True(t, body.Success, body.Message)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Equal(t, plan.NextResetTime(time.Unix(sub.LastResetTime, 0), sub.EndTime), sub.NextResetTime)
	assert.Equal(t, []string{"subscription.user_plan_reset", "subscription.user_plan_reset"}, audits)
	assert.Equal(t, []int{user.Id, user.Id}, resetUsers)
	assert.NotContains(t, string(body.Data), "AffectedUserIds")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.GetAllUserSubscriptions(ctx, user.Id)
	assert.ErrorIs(t, err, context.Canceled)
}

func membershipTimestamp(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var now int64
	require.NoError(t, db.Raw("SELECT EXTRACT(EPOCH FROM NOW())::bigint").Scan(&now).Error)
	return now
}

func TestMembershipRemovalStillWorksAfterAccountDeletion(t *testing.T) {
	db, store := newMembershipStore(t)
	now := common.GetTimestamp()
	for _, id := range []int{1, 2, 3} {
		require.NoError(t, db.Create(&identityentity.User{Id: id, Username: fmt.Sprintf("deleted-%d", id), AffCode: fmt.Sprintf("del%d", id), Group: "pro"}).Error)
		require.NoError(t, db.Create(&entity.UserSubscription{Id: id, UserId: id, PlanId: 1, Status: "active", EndTime: now - 10, UpgradeGroup: "pro", DowngradeGroup: "default"}).Error)
	}
	require.NoError(t, db.Delete(&identityentity.User{Id: 1}).Error)
	require.NoError(t, db.Unscoped().Delete(&identityentity.User{Id: 2}).Error)
	require.NoError(t, db.Delete(&identityentity.User{Id: 3}).Error)
	_, err := store.AdminInvalidateUserSubscription(t.Context(), 1)
	require.NoError(t, err)
	_, err = store.AdminDeleteUserSubscription(t.Context(), 2)
	require.NoError(t, err)
	count, err := store.ExpireDueSubscriptions(t.Context(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	var rows []entity.UserSubscription
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, "cancelled", rows[0].Status)
	assert.Equal(t, "expired", rows[1].Status)
}

func TestSelfSubscriptionsPreserveUserScopeAndPropagateDatabaseFailure(t *testing.T) {
	db, members := newMembershipStore(t)
	accounts := identity.New(identity.Dependencies{DB: db})
	service := subscription.New(subscription.Dependencies{DB: db, Members: members, BillingPreference: accounts.BillingPreference})
	user := identityentity.User{Username: "self-subs", AffCode: "self-subs", Setting: `{"billing_preference":"wallet_only"}`}
	other := identityentity.User{Username: "other-subs", AffCode: "other-subs"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&other).Error)
	now := common.GetTimestamp()
	rows := []entity.UserSubscription{
		{UserId: user.Id, PlanId: 1, Status: "active", EndTime: now + 3600, AmountTotal: 100},
		{UserId: user.Id, PlanId: 1, Status: "active", EndTime: now - 1, AmountTotal: 100},
		{UserId: user.Id, PlanId: 1, Status: "cancelled", EndTime: now + 3600, AmountTotal: 100},
		{UserId: other.Id, PlanId: 1, Status: "active", EndTime: now + 3600, AmountTotal: 999},
	}
	require.NoError(t, db.Create(&rows).Error)
	result, err := service.SelfSubscriptions(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Equal(t, "wallet_only", result.BillingPreference)
	require.Len(t, result.AllSubscriptions, 3)
	require.Len(t, result.Subscriptions, 1)
	assert.Equal(t, rows[0].Id, result.Subscriptions[0].Subscription.Id)
	for _, entry := range result.AllSubscriptions {
		assert.Equal(t, user.Id, entry.Subscription.UserId)
	}
	handler := subscriptionhttp.New(service)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", user.Id) })
	router.GET("/self", handler.GetSubscriptionSelf)
	response := planRequest(t, router, http.MethodGet, fmt.Sprintf("/self?user_id=%d", other.Id), nil)
	require.True(t, response.Success, response.Message)
	var view contract.SelfSubscriptions
	require.NoError(t, common.Unmarshal(response.Data, &view))
	require.Len(t, view.Subscriptions, 1)
	assert.Equal(t, user.Id, view.Subscriptions[0].Subscription.UserId)
	require.NoError(t, db.Exec("ALTER TABLE user_subscriptions RENAME TO unavailable_user_subscriptions").Error)
	response = planRequest(t, router, http.MethodGet, "/self", nil)
	assert.False(t, response.Success)
	assert.NotEmpty(t, response.Message)
	require.NoError(t, db.Exec("ALTER TABLE unavailable_user_subscriptions RENAME TO user_subscriptions").Error)
	require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&entity.UserSubscription{}).Error)
	result, err = service.SelfSubscriptions(t.Context(), user.Id)
	require.NoError(t, err)
	assert.NotNil(t, result.Subscriptions)
	assert.Empty(t, result.Subscriptions)
	assert.NotNil(t, result.AllSubscriptions)
	assert.Empty(t, result.AllSubscriptions)
	_, err = service.SelfSubscriptions(t.Context(), 999999)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
