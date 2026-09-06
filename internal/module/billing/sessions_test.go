package billing_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/accounting"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/sessions"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	subentity "github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type billingSessionFixture struct {
	*topupFixture
	engine *sessions.Engine
	quota  *quota.Store
	input  contract.BillingRequest
	token  identityentity.Token
	sub    subentity.UserSubscription
}

func newBillingSessionFixture(t *testing.T) *billingSessionFixture {
	t.Helper()
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	f := &billingSessionFixture{topupFixture: newTopupFixture(t, 10)}
	require.NoError(t, f.db.Model(&f.user).Update("quota", 100).Error)
	f.token = identityentity.Token{UserId: f.user.Id, Key: "session-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}
	require.NoError(t, f.db.Create(&f.token).Error)
	plan := subentity.SubscriptionPlan{Title: "Session plan", Enabled: true, QuotaResetPeriod: subentity.SubscriptionResetNever}
	require.NoError(t, f.db.Create(&plan).Error)
	now := common.GetTimestamp()
	f.sub = subentity.UserSubscription{UserId: f.user.Id, PlanId: plan.Id, AmountTotal: 100, StartTime: now - 10, EndTime: now + 3600, NextResetTime: now + 1800, Status: "active", AllowWalletOverflow: true}
	require.NoError(t, f.db.Create(&f.sub).Error)
	plans := catalog.New(catalog.Dependencies{DB: f.db})
	f.quota = quota.New(f.db, plans)
	f.engine = sessions.New(sessions.Dependencies{Accounting: accounting.New(accounting.Dependencies{DB: f.db}), Users: usercache.New(f.db), Tokens: tokencache.New(f.db), Subscriptions: f.quota, Memberships: memberships.New(memberships.Dependencies{DB: f.db}), Catalog: plans, TrustQuota: func() int { return 20 }})
	f.input = contract.BillingRequest{RequestID: "billing-session", UserID: f.user.Id, TokenID: f.token.Id, TokenKey: f.token.Key, Preference: "wallet_only", ForcePreConsume: true, TokenQuota: 100}
	return f
}

func TestBillingSessionPreferenceFallbackPreservesTokenAndFundingBalances(t *testing.T) {
	for _, tc := range []struct {
		name, pref, source string
		wallet, sub        int
		overflow           bool
	}{
		{"wallet only", "wallet_only", "wallet", 100, 100, false},
		{"wallet fallback", "wallet_first", "subscription", 0, 100, false},
		{"subscription first", "subscription_first", "subscription", 100, 100, true},
		{"allowed overflow", "subscription_first", "wallet", 100, 10, true},
		{"forbidden overflow", "subscription_first", "", 100, 10, false},
		{"subscription only", "subscription_only", "", 100, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBillingSessionFixture(t)
			require.NoError(t, f.db.Model(&f.user).Update("quota", tc.wallet).Error)
			require.NoError(t, f.db.Model(&f.sub).Updates(map[string]any{"amount_total": tc.sub, "allow_wallet_overflow": tc.overflow}).Error)
			f.input.Preference = tc.pref
			session, err := f.engine.Begin(t.Context(), f.input, 30)
			if tc.source == "" {
				var failure *contract.BillingFailure
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, contract.BillingInsufficientFunds, failure.Kind)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.source, session.Snapshot().Source)
				assert.Equal(t, 30, session.GetPreConsumedQuota())
			}
			require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
			require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
			require.NoError(t, f.db.First(&f.sub, f.sub.Id).Error)
			wantWallet, wantToken, wantUsed := tc.wallet, 100, 0
			if tc.source != "" {
				wantToken = 70
			}
			if tc.source == "wallet" {
				wantWallet -= 30
			}
			if tc.source == "subscription" {
				wantUsed = 30
			}
			assert.Equal(t, wantWallet, f.user.Quota)
			assert.Equal(t, wantToken, f.token.RemainQuota)
			assert.EqualValues(t, wantUsed, f.sub.AmountUsed)
		})
	}
}

func TestBillingSessionRefundIncludesExtraReservationAndSurvivesCancellation(t *testing.T) {
	for _, tc := range []struct {
		source     string
		playground bool
	}{{"wallet", false}, {"subscription", false}, {"wallet", true}} {
		f := newBillingSessionFixture(t)
		f.input.Preference = tc.source + "_only"
		f.input.Playground = tc.playground
		session, err := f.engine.Begin(t.Context(), f.input, 30)
		require.NoError(t, err)
		require.NoError(t, session.Reserve(t.Context(), 50))
		assert.True(t, session.NeedsRefund())
		assert.Equal(t, 50, session.GetPreConsumedQuota())
		if tc.source == "subscription" {
			assert.EqualValues(t, 50, session.Snapshot().SubscriptionPreConsumed)
			require.NoError(t, f.quota.PostConsumeUserSubscriptionDelta(t.Context(), f.sub.Id, 10))
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		results := make(chan error, 2)
		for range 2 {
			go func() { results <- session.Refund(ctx) }()
		}
		require.NoError(t, <-results)
		require.NoError(t, <-results)
		require.Error(t, session.Settle(t.Context(), 20))
		assert.False(t, session.NeedsRefund())
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
		require.NoError(t, f.db.First(&f.sub, f.sub.Id).Error)
		assert.Equal(t, 100, f.user.Quota)
		assert.Equal(t, 100, f.token.RemainQuota)
		assert.Zero(t, f.token.UsedQuota)
		if tc.source == "subscription" {
			assert.EqualValues(t, 10, f.sub.AmountUsed)
		}
	}
}

func TestBillingSessionSettledFundingCannotBeRefundedAfterTokenFailure(t *testing.T) {
	f := newBillingSessionFixture(t)
	session, err := f.engine.Begin(t.Context(), f.input, 30)
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(`CREATE FUNCTION fail_session_token() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected token settlement failure'; END; $$; CREATE TRIGGER fail_session_token BEFORE UPDATE ON tokens FOR EACH ROW EXECUTE FUNCTION fail_session_token();`).Error)
	require.Error(t, session.Settle(t.Context(), 50))
	require.NoError(t, session.Refund(t.Context()))
	require.NoError(t, session.Settle(t.Context(), 50))
	assert.False(t, session.NeedsRefund())
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
	assert.Equal(t, 50, f.user.Quota)
	assert.Equal(t, 70, f.token.RemainQuota)
}

func TestBillingSessionReserveRollsBackFundingWhenTokenRejectsIncrease(t *testing.T) {
	for _, source := range []string{"wallet", "subscription"} {
		f := newBillingSessionFixture(t)
		f.input.Preference = source + "_only"
		require.NoError(t, f.db.Model(&f.token).Update("remain_quota", 40).Error)
		session, err := f.engine.Begin(t.Context(), f.input, 30)
		require.NoError(t, err)
		require.Error(t, session.Reserve(t.Context(), 50))
		assert.Equal(t, 30, session.GetPreConsumedQuota())
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		require.NoError(t, f.db.First(&f.sub, f.sub.Id).Error)
		if source == "wallet" {
			assert.Equal(t, 70, f.user.Quota)
		} else {
			assert.EqualValues(t, 30, f.sub.AmountUsed)
		}
		require.NoError(t, session.Refund(t.Context()))
		require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
		assert.Equal(t, 40, f.token.RemainQuota)
	}
}

func TestBillingSessionTrustBypassAndSubscriptionMinimumStayConsistent(t *testing.T) {
	for _, tc := range []struct {
		pref        string
		force       bool
		amount, pre int
	}{{"wallet_only", false, 30, 0}, {"wallet_only", true, 30, 30}, {"subscription_only", false, 0, 1}} {
		f := newBillingSessionFixture(t)
		f.input.Preference = tc.pref
		f.input.ForcePreConsume = tc.force
		session, err := f.engine.Begin(t.Context(), f.input, tc.amount)
		require.NoError(t, err)
		assert.Equal(t, tc.pre, session.GetPreConsumedQuota())
		require.NoError(t, session.Settle(t.Context(), 0))
		require.NoError(t, session.Refund(t.Context()))
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
		require.NoError(t, f.db.First(&f.sub, f.sub.Id).Error)
		assert.Equal(t, 100, f.user.Quota)
		assert.Equal(t, 100, f.token.RemainQuota)
		assert.Zero(t, f.sub.AmountUsed)
	}
}

func TestBillingSessionRejectsInvalidChargeBoundsWithoutChangingBalances(t *testing.T) {
	f := newBillingSessionFixture(t)
	for _, amount := range []int{-1, common.MaxQuota + 1, math.MaxInt} {
		_, err := f.engine.Begin(t.Context(), f.input, amount)
		require.Error(t, err)
	}
	session, err := f.engine.Begin(t.Context(), f.input, 30)
	require.NoError(t, err)
	require.Error(t, session.Reserve(t.Context(), math.MaxInt))
	require.Error(t, session.Settle(t.Context(), -1))
	assert.Equal(t, 30, session.GetPreConsumedQuota())
	require.NoError(t, session.Refund(t.Context()))
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
	assert.Equal(t, 100, f.user.Quota)
	assert.Equal(t, 100, f.token.RemainQuota)
}

func TestBillingSessionStorageAndRollbackFailuresDoNotTriggerWalletFallback(t *testing.T) {
	for _, rollbackFailure := range []bool{false, true} {
		f := newBillingSessionFixture(t)
		f.input.Preference = "subscription_first"
		if rollbackFailure {
			require.NoError(t, f.db.Model(&f.sub).Update("amount_total", 10).Error)
			require.NoError(t, f.db.Exec(`CREATE FUNCTION fail_session_rollback() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.remain_quota > OLD.remain_quota THEN RAISE EXCEPTION 'rollback failed'; END IF; RETURN NEW; END; $$; CREATE TRIGGER fail_session_rollback BEFORE UPDATE ON tokens FOR EACH ROW EXECUTE FUNCTION fail_session_rollback();`).Error)
		} else {
			require.NoError(t, f.db.Exec(`CREATE FUNCTION fail_session_subscription() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'subscription quota insufficient: injected DB error'; END; $$; CREATE TRIGGER fail_session_subscription BEFORE UPDATE ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION fail_session_subscription();`).Error)
		}
		_, err := f.engine.Begin(t.Context(), f.input, 30)
		var failure *contract.BillingFailure
		require.True(t, errors.As(err, &failure))
		assert.Equal(t, contract.BillingStorageFailure, failure.Kind)
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		require.NoError(t, f.db.First(&f.token, f.token.Id).Error)
		assert.Equal(t, 100, f.user.Quota)
		if rollbackFailure {
			assert.Equal(t, 70, f.token.RemainQuota)
		} else {
			assert.Equal(t, 100, f.token.RemainQuota)
		}
	}
}
