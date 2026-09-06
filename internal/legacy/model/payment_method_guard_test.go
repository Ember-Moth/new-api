package model

import (
	"testing"
	"time"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	billingentity "github.com/QuantumNous/new-api/internal/module/billing/entity"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUserForPaymentGuardTest(t *testing.T, id int, quota int) *User {
	t.Helper()
	user := &User{
		Id:       id,
		Username: "payment_guard_user",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func insertSubscriptionPlanForPaymentGuardTest(t *testing.T, id int) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:            id,
		Title:         "Guard Plan",
		PriceAmount:   9.99,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   1000,
	}
	require.NoError(t, DB.Create(plan).Error)
	return plan
}

func insertSubscriptionOrderForPaymentGuardTest(t *testing.T, tradeNo string, userID int, planID int, paymentProvider string) {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:          userID,
		PlanId:          planID,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, SubscriptionPayments().Create(t.Context(), order))
}

func insertTopUpForPaymentGuardTest(t *testing.T, tradeNo string, userID int, paymentProvider string) {
	t.Helper()
	topUp := &billingentity.TopUp{
		UserId:          userID,
		Amount:          2,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, TopUpStore().Create(t.Context(), topUp))
}

func getTopUpStatusForPaymentGuardTest(t *testing.T, tradeNo string) string {
	t.Helper()
	topUp, getErr := TopUpStore().Get(t.Context(), tradeNo)
	require.NoError(t, getErr)
	require.NotNil(t, topUp)
	return topUp.Status
}

func countUserSubscriptionsForPaymentGuardTest(t *testing.T, userID int) int64 {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
	return count
}

func getUserQuotaForPaymentGuardTest(t *testing.T, userID int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func TestRechargeWaffoPancake_RejectsMismatchedPaymentMethod(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 101, 0)
	insertTopUpForPaymentGuardTest(t, "waffo-pancake-guard", 101, billingcontract.PaymentProviderStripe)

	_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: "waffo-pancake-guard", Provider: billingcontract.PaymentProviderWaffoPancake})
	require.Error(t, err)

	topUp, getErr := TopUpStore().Get(t.Context(), "waffo-pancake-guard")
	require.NoError(t, getErr)
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusPending, topUp.Status)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 101))
}

func TestUpdatePendingTopUpStatus_RejectsMismatchedPaymentProvider(t *testing.T) {
	testCases := []struct {
		name                    string
		tradeNo                 string
		storedPaymentProvider   string
		expectedPaymentProvider string
		targetStatus            string
	}{
		{
			name:                    "stripe expire",
			tradeNo:                 "stripe-expire-guard",
			storedPaymentProvider:   billingcontract.PaymentProviderCreem,
			expectedPaymentProvider: billingcontract.PaymentProviderStripe,
			targetStatus:            common.TopUpStatusExpired,
		},
		{
			name:                    "waffo failed",
			tradeNo:                 "waffo-failed-guard",
			storedPaymentProvider:   billingcontract.PaymentProviderStripe,
			expectedPaymentProvider: billingcontract.PaymentProviderWaffo,
			targetStatus:            common.TopUpStatusFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 150, 0)
			insertTopUpForPaymentGuardTest(t, tc.tradeNo, 150, tc.storedPaymentProvider)

			err := TopUpStore().FinishPending(t.Context(), tc.tradeNo, tc.expectedPaymentProvider, tc.targetStatus)
			require.ErrorIs(t, err, billingcontract.ErrPaymentMethodMismatch)
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, tc.tradeNo))
		})
	}
}

func TestCompleteSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 202, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 301)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-guard-order", 202, plan.Id, billingcontract.PaymentProviderStripe)

	err := SubscriptionPayments().Complete(t.Context(), "sub-guard-order", `{"provider":"epay"}`, billingcontract.PaymentProviderEpay, "alipay")
	require.ErrorIs(t, err, billingcontract.ErrPaymentMethodMismatch)

	order, getErr := SubscriptionPayments().Get(t.Context(), "sub-guard-order")
	require.NoError(t, getErr)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 202))

	topUp, getErr := TopUpStore().Get(t.Context(), "sub-guard-order")
	assert.ErrorIs(t, getErr, billingcontract.ErrTopUpNotFound)
	assert.Nil(t, topUp)
}

func TestExpireSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 303, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 401)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-expire-guard", 303, plan.Id, billingcontract.PaymentProviderStripe)

	err := SubscriptionPayments().FinishPending(t.Context(), "sub-expire-guard", billingcontract.PaymentProviderCreem, common.TopUpStatusExpired)
	require.ErrorIs(t, err, billingcontract.ErrPaymentMethodMismatch)

	order, getErr := SubscriptionPayments().Get(t.Context(), "sub-expire-guard")
	require.NoError(t, getErr)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}

func createEpayTestOrder(t *testing.T, userId int, tradeNo string, provider string, status string) billingentity.TopUp {
	t.Helper()
	topUp := billingentity.TopUp{
		UserId:          userId,
		Amount:          2,
		Money:           10.0,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: provider,
		CreateTime:      common.GetTimestamp(),
		Status:          status,
	}
	require.NoError(t, DB.Create(&topUp).Error)
	return topUp
}

func TestRechargeEpayCreditsQuotaExactlyOnce(t *testing.T) {
	truncateTables(t)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	user := insertUserForPaymentGuardTest(t, 501, 0)
	order := createEpayTestOrder(t, user.Id, "EPAYTESTONCE", billingcontract.PaymentProviderEpay, common.TopUpStatusPending)

	alreadyDone, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	assert.Equal(t, 2*500000, getUserQuotaForPaymentGuardTest(t, user.Id))

	reloaded, getErr := TopUpStore().Get(t.Context(), order.TradeNo)
	require.NoError(t, getErr)
	require.NotNil(t, reloaded)
	assert.Equal(t, common.TopUpStatusSuccess, reloaded.Status)
	assert.NotZero(t, reloaded.CompleteTime)

	alreadyDone, err = TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	assert.Equal(t, 2*500000, getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestRechargeEpayKeepsRedisAndDatabaseCreditInSync(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 5
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	user := insertUserForPaymentGuardTest(t, 502, 7)
	require.NoError(t, populateUserCache(*user))
	order := createEpayTestOrder(t, user.Id, "EPAYTESTREDISSYNC", billingcontract.PaymentProviderEpay, common.TopUpStatusPending)

	alreadyDone, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	assert.Equal(t, 17, getUserQuotaForPaymentGuardTest(t, user.Id))
	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 17, cached.Quota)

	alreadyDone, err = TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	cached, err = cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 17, cached.Quota)
}

func TestRechargeEpayUpdatesPaymentMethodToActual(t *testing.T) {
	truncateTables(t)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	user := insertUserForPaymentGuardTest(t, 503, 0)
	order := createEpayTestOrder(t, user.Id, "EPAYTESTMETHOD", billingcontract.PaymentProviderEpay, common.TopUpStatusPending)

	alreadyDone, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "wxpay", CallerIP: "127.0.0.1"})
	require.NoError(t, err)
	assert.False(t, alreadyDone)

	reloaded, getErr := TopUpStore().Get(t.Context(), order.TradeNo)
	require.NoError(t, getErr)
	require.NotNil(t, reloaded)
	assert.Equal(t, "wxpay", reloaded.PaymentMethod)
	assert.Equal(t, 2*500000, getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestRechargeEpayRejectsForeignAndNonPendingOrders(t *testing.T) {
	truncateTables(t)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	user := insertUserForPaymentGuardTest(t, 504, 7)

	t.Run("order from another payment provider", func(t *testing.T) {
		order := createEpayTestOrder(t, user.Id, "EPAYTESTSTRIPE", billingcontract.PaymentProviderStripe, common.TopUpStatusPending)
		_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
		assert.ErrorIs(t, err, billingcontract.ErrPaymentMethodMismatch)
		assert.Equal(t, 7, getUserQuotaForPaymentGuardTest(t, user.Id))
	})

	t.Run("order that is not pending", func(t *testing.T) {
		order := createEpayTestOrder(t, user.Id, "EPAYTESTEXPIRED", billingcontract.PaymentProviderEpay, common.TopUpStatusExpired)
		_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
		assert.ErrorIs(t, err, billingcontract.ErrTopUpStatusInvalid)
		assert.Equal(t, 7, getUserQuotaForPaymentGuardTest(t, user.Id))
	})

	t.Run("missing order", func(t *testing.T) {
		_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: "EPAYTESTMISSING", Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
		assert.ErrorIs(t, err, billingcontract.ErrTopUpNotFound)
	})
}

func TestRechargeEpayRejectsQuotaOverflowBeforeCompletingOrder(t *testing.T) {
	truncateTables(t)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = float64(common.MaxWalletQuota + 1)
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	user := insertUserForPaymentGuardTest(t, 505, 3)
	order := createEpayTestOrder(t, user.Id, "EPAYTESTOVERFLOW", billingcontract.PaymentProviderEpay, common.TopUpStatusPending)

	_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
	require.Error(t, err)
	assert.Equal(t, 3, getUserQuotaForPaymentGuardTest(t, user.Id))
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, order.TradeNo))
}

func TestRechargeEpayEnforcesFinalWalletQuotaLimit(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	testCases := []struct {
		name         string
		currentQuota int
		wantErr      bool
		wantQuota    int
		wantStatus   string
	}{
		{
			name:         "allows exact highest representable wallet balance",
			currentQuota: common.MaxWalletQuota - 1_000_000,
			wantQuota:    common.MaxWalletQuota,
			wantStatus:   common.TopUpStatusSuccess,
		},
		{
			name:         "rejects balance above wallet quota domain",
			currentQuota: common.MaxWalletQuota - 999_999,
			wantErr:      true,
			wantQuota:    common.MaxWalletQuota - 999_999,
			wantStatus:   common.TopUpStatusPending,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			user := insertUserForPaymentGuardTest(t, 506, tc.currentQuota)
			order := createEpayTestOrder(t, user.Id, "EPAYTESTWALLETLIMIT", billingcontract.PaymentProviderEpay, common.TopUpStatusPending)

			_, err := TopUpStore().Complete(t.Context(), billingcontract.TopUpCompletion{TradeNo: order.TradeNo, Provider: billingcontract.PaymentProviderEpay, ActualMethod: "alipay", CallerIP: "127.0.0.1"})
			if tc.wantErr {
				require.ErrorIs(t, err, billingcontract.ErrTopUpQuotaLimitExceeded)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantQuota, getUserQuotaForPaymentGuardTest(t, user.Id))
			assert.Equal(t, tc.wantStatus, getTopUpStatusForPaymentGuardTest(t, order.TradeNo))
		})
	}
}
