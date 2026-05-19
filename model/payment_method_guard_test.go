package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUserForPaymentGuardTest(t *testing.T, id int, quota int) {
	t.Helper()
	user := &User{
		Id:       id,
		Username: "payment_guard_user",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}
	require.NoError(t, DB.Create(user).Error)
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
	require.NoError(t, order.Insert())
}

func insertStripePaymentIntentSubscriptionOrderForPaymentGuardTest(t *testing.T, tradeNo string, userID int, planID int, gatewayTradeNo string, gatewayAmount int64, gatewayCurrency string) {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:          userID,
		PlanId:          planID,
		Money:           9.99,
		TradeNo:         tradeNo,
		GatewayTradeNo:  gatewayTradeNo,
		GatewayAmount:   gatewayAmount,
		GatewayCurrency: gatewayCurrency,
		PaymentMethod:   PaymentMethodStripeIntent,
		PaymentProvider: PaymentProviderStripeIntent,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, order.Insert())
}

func insertTopUpForPaymentGuardTest(t *testing.T, tradeNo string, userID int, paymentProvider string) {
	t.Helper()
	topUp := &TopUp{
		UserId:          userID,
		Amount:          2,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())
}

func insertStripePaymentIntentTopUpForPaymentGuardTest(t *testing.T, tradeNo string, userID int, gatewayTradeNo string, gatewayAmount int64, gatewayCurrency string) {
	t.Helper()
	topUp := &TopUp{
		UserId:          userID,
		Amount:          2,
		Money:           2,
		TradeNo:         tradeNo,
		GatewayTradeNo:  gatewayTradeNo,
		GatewayAmount:   gatewayAmount,
		GatewayCurrency: gatewayCurrency,
		PaymentMethod:   PaymentMethodStripeIntent,
		PaymentProvider: PaymentProviderStripeIntent,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())
}

func getTopUpStatusForPaymentGuardTest(t *testing.T, tradeNo string) string {
	t.Helper()
	topUp := GetTopUpByTradeNo(tradeNo)
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

func TestRechargeStripePaymentIntent_RequiresGatewayAmountAndCurrencyMatch(t *testing.T) {
	testCases := []struct {
		name            string
		gatewayTradeNo  string
		gatewayAmount   int64
		gatewayCurrency string
		expectedError   error
	}{
		{
			name:            "gateway trade number mismatch",
			gatewayTradeNo:  "pi_other",
			gatewayAmount:   100,
			gatewayCurrency: "cny",
			expectedError:   ErrGatewayTradeNoMismatch,
		},
		{
			name:            "amount mismatch",
			gatewayTradeNo:  "pi_expected",
			gatewayAmount:   101,
			gatewayCurrency: "cny",
			expectedError:   ErrTopUpAmountMismatch,
		},
		{
			name:            "currency mismatch",
			gatewayTradeNo:  "pi_expected",
			gatewayAmount:   100,
			gatewayCurrency: "usd",
			expectedError:   ErrTopUpCurrencyMismatch,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 175, 0)
			insertStripePaymentIntentTopUpForPaymentGuardTest(t, "stripe-pi-guard", 175, "pi_expected", 100, "cny")

			err := RechargeStripePaymentIntent("stripe-pi-guard", tc.gatewayTradeNo, tc.gatewayAmount, tc.gatewayCurrency, "127.0.0.1")
			require.ErrorIs(t, err, tc.expectedError)
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, "stripe-pi-guard"))
			assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 175))
		})
	}
}

func TestRechargeStripePaymentIntent_CompletesMatchingOrder(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 176, 0)
	insertStripePaymentIntentTopUpForPaymentGuardTest(t, "stripe-pi-success", 176, "pi_success", 100, "cny")

	err := RechargeStripePaymentIntent("stripe-pi-success", "pi_success", 100, "CNY", "127.0.0.1")
	require.NoError(t, err)

	assert.Equal(t, common.TopUpStatusSuccess, getTopUpStatusForPaymentGuardTest(t, "stripe-pi-success"))
	assert.Equal(t, int(2*common.QuotaPerUnit), getUserQuotaForPaymentGuardTest(t, 176))
}

func TestUpdatePendingTopUpStatusWithGatewayTradeNo_RejectsMismatchedGatewayTradeNo(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 177, 0)
	insertStripePaymentIntentTopUpForPaymentGuardTest(t, "stripe-pi-failed", 177, "pi_expected", 100, "cny")

	err := UpdatePendingTopUpStatusWithGatewayTradeNo("stripe-pi-failed", PaymentProviderStripeIntent, "pi_other", common.TopUpStatusFailed)
	require.ErrorIs(t, err, ErrGatewayTradeNoMismatch)
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, "stripe-pi-failed"))
}

func TestRechargeWaffoPancake_RejectsMismatchedPaymentMethod(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 101, 0)
	insertTopUpForPaymentGuardTest(t, "waffo-pancake-guard", 101, PaymentProviderStripe)

	err := RechargeWaffoPancake("waffo-pancake-guard")
	require.Error(t, err)

	topUp := GetTopUpByTradeNo("waffo-pancake-guard")
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
			storedPaymentProvider:   PaymentProviderCreem,
			expectedPaymentProvider: PaymentProviderStripe,
			targetStatus:            common.TopUpStatusExpired,
		},
		{
			name:                    "waffo failed",
			tradeNo:                 "waffo-failed-guard",
			storedPaymentProvider:   PaymentProviderStripe,
			expectedPaymentProvider: PaymentProviderWaffo,
			targetStatus:            common.TopUpStatusFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 150, 0)
			insertTopUpForPaymentGuardTest(t, tc.tradeNo, 150, tc.storedPaymentProvider)

			err := UpdatePendingTopUpStatus(tc.tradeNo, tc.expectedPaymentProvider, tc.targetStatus)
			require.ErrorIs(t, err, ErrPaymentMethodMismatch)
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, tc.tradeNo))
		})
	}
}

func TestCompleteSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 202, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 301)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-guard-order", 202, plan.Id, PaymentProviderStripe)

	err := CompleteSubscriptionOrder("sub-guard-order", `{"provider":"epay"}`, PaymentProviderEpay, "alipay")
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-guard-order")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 202))

	topUp := GetTopUpByTradeNo("sub-guard-order")
	assert.Nil(t, topUp)
}

func TestCompleteSubscriptionOrderWithGatewayTradeNo_RequiresGatewayAmountAndCurrencyMatch(t *testing.T) {
	testCases := []struct {
		name            string
		gatewayTradeNo  string
		gatewayAmount   int64
		gatewayCurrency string
		expectedError   error
	}{
		{
			name:            "gateway trade number mismatch",
			gatewayTradeNo:  "pi_other",
			gatewayAmount:   100,
			gatewayCurrency: "cny",
			expectedError:   ErrGatewayTradeNoMismatch,
		},
		{
			name:            "amount mismatch",
			gatewayTradeNo:  "pi_expected",
			gatewayAmount:   101,
			gatewayCurrency: "cny",
			expectedError:   ErrTopUpAmountMismatch,
		},
		{
			name:            "currency mismatch",
			gatewayTradeNo:  "pi_expected",
			gatewayAmount:   100,
			gatewayCurrency: "usd",
			expectedError:   ErrTopUpCurrencyMismatch,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 404, 0)
			plan := insertSubscriptionPlanForPaymentGuardTest(t, 504)
			insertStripePaymentIntentSubscriptionOrderForPaymentGuardTest(t, "sub-pi-guard", 404, plan.Id, "pi_expected", 100, "cny")

			err := CompleteSubscriptionOrderWithGatewayTradeNo("sub-pi-guard", `{"provider":"stripe_payment_intent"}`, PaymentProviderStripeIntent, PaymentMethodStripeIntent, tc.gatewayTradeNo, tc.gatewayAmount, tc.gatewayCurrency)
			require.ErrorIs(t, err, tc.expectedError)

			order := GetSubscriptionOrderByTradeNo("sub-pi-guard")
			require.NotNil(t, order)
			assert.Equal(t, common.TopUpStatusPending, order.Status)
			assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 404))
		})
	}
}

func TestCompleteSubscriptionOrderWithGatewayTradeNo_CompletesMatchingOrder(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 405, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 505)
	insertStripePaymentIntentSubscriptionOrderForPaymentGuardTest(t, "sub-pi-success", 405, plan.Id, "pi_success", 100, "cny")

	err := CompleteSubscriptionOrderWithGatewayTradeNo("sub-pi-success", `{"provider":"stripe_payment_intent"}`, PaymentProviderStripeIntent, PaymentMethodStripeIntent, "pi_success", 100, "CNY")
	require.NoError(t, err)

	order := GetSubscriptionOrderByTradeNo("sub-pi-success")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
	assert.Equal(t, int64(1), countUserSubscriptionsForPaymentGuardTest(t, 405))

	topUp := GetTopUpByTradeNo("sub-pi-success")
	require.NotNil(t, topUp)
	assert.Equal(t, PaymentProviderStripeIntent, topUp.PaymentProvider)
	assert.Equal(t, "pi_success", topUp.GatewayTradeNo)
	assert.Equal(t, int64(100), topUp.GatewayAmount)
	assert.Equal(t, "cny", topUp.GatewayCurrency)
}

func TestPurchaseSubscriptionWithWallet_DeductsQuotaAndCreatesSubscription(t *testing.T) {
	truncateTables(t)
	userID := 407
	initialQuota := int(10 * common.QuotaPerUnit)
	insertUserForPaymentGuardTest(t, userID, initialQuota)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 507)
	plan.PriceAmount = 2
	require.NoError(t, DB.Save(plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)

	result, err := PurchaseSubscriptionWithWallet(userID, plan.Id)
	require.NoError(t, err)
	require.NotNil(t, result)

	quotaCost := int(2 * common.QuotaPerUnit)
	assert.Equal(t, quotaCost, result.QuotaCost)
	assert.Equal(t, initialQuota-quotaCost, getUserQuotaForPaymentGuardTest(t, userID))
	assert.Equal(t, int64(1), countUserSubscriptionsForPaymentGuardTest(t, userID))

	order := GetSubscriptionOrderByTradeNo(result.Order.TradeNo)
	require.NotNil(t, order)
	assert.Equal(t, PaymentProviderWallet, order.PaymentProvider)
	assert.Equal(t, PaymentMethodWallet, order.PaymentMethod)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)

	topUp := GetTopUpByTradeNo(result.Order.TradeNo)
	require.NotNil(t, topUp)
	assert.Equal(t, PaymentProviderWallet, topUp.PaymentProvider)
	assert.Equal(t, PaymentMethodWallet, topUp.PaymentMethod)
}

func TestPurchaseSubscriptionWithWallet_RejectsInsufficientQuota(t *testing.T) {
	truncateTables(t)
	userID := 408
	insertUserForPaymentGuardTest(t, userID, int(common.QuotaPerUnit))
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 508)
	plan.PriceAmount = 2
	require.NoError(t, DB.Save(plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)

	result, err := PurchaseSubscriptionWithWallet(userID, plan.Id)
	require.Error(t, err)
	require.Nil(t, result)
	assert.Equal(t, int(common.QuotaPerUnit), getUserQuotaForPaymentGuardTest(t, userID))
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, userID))
}

func TestUpdatePendingSubscriptionOrderStatusWithGatewayTradeNo_RejectsMismatchedGatewayTradeNo(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 406, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 506)
	insertStripePaymentIntentSubscriptionOrderForPaymentGuardTest(t, "sub-pi-failed", 406, plan.Id, "pi_expected", 100, "cny")

	err := UpdatePendingSubscriptionOrderStatusWithGatewayTradeNo("sub-pi-failed", PaymentProviderStripeIntent, "pi_other", common.TopUpStatusFailed)
	require.ErrorIs(t, err, ErrGatewayTradeNoMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-pi-failed")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}

func TestExpireSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 303, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 401)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-expire-guard", 303, plan.Id, PaymentProviderStripe)

	err := ExpireSubscriptionOrder("sub-expire-guard", PaymentProviderCreem)
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-expire-guard")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}
