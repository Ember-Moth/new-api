package billing_test

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/module/billing/purchases"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalletAmountConversionPreservesStoredUnitsAndBounds(t *testing.T) {
	for _, test := range []struct {
		tokens         bool
		amount, stored int64
		credit         int
	}{{false, 4294, 4294, 2147000000}, {false, 4295, 4295, 2147500000}, {true, 2147500000, 4295, 2147500000}, {true, 4294500000, 8589, 4294500000}, {true, 1250001, 2, 1000000}} {
		stored, credit, err := purchases.ConvertAmount(test.amount, 500000, test.tokens)
		require.NoError(t, err)
		assert.Equal(t, test.stored, stored)
		assert.Equal(t, test.credit, credit)
	}
	maxAmount := int64(common.MaxWalletQuota / 500000)
	_, _, err := purchases.ConvertAmount(maxAmount, 500000, false)
	require.NoError(t, err)
	_, _, err = purchases.ConvertAmount(maxAmount+1, 500000, false)
	assert.EqualError(t, err, fmt.Sprintf("单笔充值数量不能大于 %d", maxAmount))
	for _, unit := range []float64{0, -1, math.NaN(), math.Inf(1), math.SmallestNonzeroFloat64} {
		_, _, err := purchases.ConvertAmount(math.MaxInt64, unit, true)
		require.Error(t, err)
	}
	_, _, err = purchases.ConvertAmount(0, 500000, false)
	require.Error(t, err)
}

func TestWalletInformationReadinessAndConfigurationIsolation(t *testing.T) {
	cfg := contract.WalletConfig{PaymentAllowed: true, TermsVersion: "terms", Gateway: contract.GatewayConfig{StripeKey: "secret-stripe", StripeWebhookSecret: "secret-hook", CreemKey: "secret-creem", WaffoMerchantID: "merchant", WaffoPrivateKey: "secret-key", EpayAddress: "https://pay.example", EpayID: "id", EpayKey: "secret-epay"}, StripePriceID: "price", CreemProducts: `[{"productId":"prod"}]`, PancakeProductID: "product", WaffoEnabled: true, WaffoConfigured: true, PayMethods: []map[string]string{{"type": "alipay", "name": "original"}}, Discounts: map[int]float64{10: 0.5}}
	info := purchases.Information(cfg)
	assert.True(t, info.Online)
	assert.True(t, info.Stripe)
	assert.True(t, info.Creem)
	assert.True(t, info.Waffo)
	assert.True(t, info.Pancake)
	require.Len(t, info.PayMethods, 4)
	assert.Equal(t, "waffo_pancake", info.PayMethods[2]["type"])
	assert.Equal(t, "waffo", info.PayMethods[3]["type"])
	info.PayMethods[0]["name"] = "changed"
	info.Discounts[10] = 0.9
	assert.Equal(t, "original", cfg.PayMethods[0]["name"])
	assert.Equal(t, 0.5, cfg.Discounts[10])
	encoded, err := common.Marshal(info)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "secret-")
	cfg.PaymentAllowed = false
	info = purchases.Information(cfg)
	assert.False(t, info.Online)
	assert.False(t, info.Stripe)
	assert.False(t, info.Creem)
	assert.False(t, info.Waffo)
	assert.False(t, info.Pancake)
	assert.False(t, info.Redemption)
	assert.Empty(t, info.PayMethods)
	cfg.PaymentAllowed = true
	cfg.StripePriceID = ""
	cfg.CreemProducts = "[]"
	cfg.WaffoConfigured = false
	cfg.PancakeProductID = ""
	cfg.PayMethods = nil
	info = purchases.Information(cfg)
	assert.False(t, info.Online)
	assert.False(t, info.Stripe)
	assert.False(t, info.Creem)
	assert.False(t, info.Waffo)
	assert.False(t, info.Pancake)
}

func TestWalletEpayQuoteOrderAndSignedSettlementUseTheSameUnits(t *testing.T) {
	f := newTopupFixture(t, 500000)
	require.NoError(t, f.db.Model(&f.user).Update("group", "vip").Error)
	cfg := contract.WalletConfig{PaymentAllowed: true, QuotaPerUnit: 500000, TokensDisplay: true, Minimum: 1, Price: 7.3, Discounts: map[int]float64{1250001: 0.5}, Gateway: contract.GatewayConfig{EpayAddress: "https://pay.example", EpayID: "merchant", EpayKey: "secret", EpayMethods: []string{"alipay"}, CallbackAddress: "https://api.example", ServerAddress: "https://console.example"}}
	accounts := identity.New(identity.Dependencies{DB: f.db})
	gateway := checkout.New(checkout.Options{Config: func() contract.GatewayConfig { return cfg.Gateway }})
	quotes := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(group string) float64 { assert.Equal(t, "vip", group); return 2 }, TopUps: f.store, Gateway: gateway})
	processor := webhooks.New(webhooks.Dependencies{Config: func() webhooks.Config {
		return webhooks.Config{PaymentAllowed: cfg.PaymentAllowed, EpayConfigured: true}
	}, EpayVerifier: gateway.VerifyEpay, TopUps: f.store})
	handler := billinghttp.New(billing.New(billing.Dependencies{Purchases: quotes, Webhooks: processor}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
	router.POST("/amount", handler.RequestAmount)
	router.POST("/pay", handler.RequestEpay)
	router.POST("/notify", handler.EpayNotify)
	quoted := redemptionRequest(t, router, http.MethodPost, "/amount", contract.WalletPayRequest{Amount: 1250001})
	assert.Equal(t, "success", quoted.Message)
	assert.Equal(t, `"18.25"`, string(quoted.Data))
	result, err := quotes.StartEpay(t.Context(), f.user.Id, contract.WalletPayRequest{Amount: 1250001, PaymentMethod: "alipay"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(result.OrderID, "USR"))
	assert.Equal(t, "18.25", result.EpayParams["money"])
	assert.Equal(t, "https://api.example/api/user/epay/notify", result.EpayParams["notify_url"])
	assert.Equal(t, "https://console.example/usage-logs", result.EpayParams["return_url"])
	row, err := f.store.Get(t.Context(), result.OrderID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, row.Amount)
	// Removing selectable methods does not invalidate a signed notification for
	// an order already created with those methods.
	cfg.Gateway.EpayMethods = nil
	values := url.Values{}
	for key, value := range epay.GenerateParams(map[string]string{"out_trade_no": result.OrderID, "type": "alipay", "trade_status": epay.StatusTradeSuccess, "money": "18.25"}, "secret") {
		values.Set(key, value)
	}
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Equal(t, "success", response.Body.String())
	}
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 1000010, f.user.Quota)
	assert.EqualValues(t, 1, f.events.Load())
	cfg.PaymentAllowed = false
	response := redemptionRequest(t, router, http.MethodPost, "/pay", contract.WalletPayRequest{Amount: 1250001, PaymentMethod: "alipay"})
	assert.Equal(t, "error", response.Message)
	var count int64
	require.NoError(t, f.db.Model(&entity.TopUp{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestWalletQuoteRejectsInvalidFactorsAndUnsettleableAmounts(t *testing.T) {
	f := newTopupFixture(t, 500000)
	cfg := contract.WalletConfig{QuotaPerUnit: 500000, Price: 7.3, Minimum: 1}
	accounts := identity.New(identity.Dependencies{DB: f.db})
	quotes := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(string) float64 { return 1 }, TopUps: f.store})
	_, err := quotes.Quote(t.Context(), f.user.Id, int64(common.MaxWalletQuota/500000)+1)
	require.Error(t, err)
	require.NoError(t, f.db.Model(&f.user).Update("quota", common.MaxWalletQuota-100000).Error)
	_, err = quotes.Quote(t.Context(), f.user.Id, 1)
	assert.ErrorIs(t, err, contract.ErrTopUpQuotaLimitExceeded)
	require.NoError(t, f.db.Model(&f.user).Update("quota", 10).Error)
	for _, price := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		cfg.Price = price
		_, err = quotes.Quote(t.Context(), f.user.Id, 1)
		require.Error(t, err)
	}
	cfg.Price = 7.3
	cfg.Discounts = map[int]float64{1: math.Inf(1)}
	_, err = quotes.Quote(t.Context(), f.user.Id, 1)
	require.Error(t, err)
}
