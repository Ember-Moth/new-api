package billing_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
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

type walletGatewayFixture struct {
	create func(context.Context, contract.CheckoutRequest) (contract.CheckoutSession, error)
}

func (g walletGatewayFixture) ValidateSubscription(string, string) error { return nil }
func (g walletGatewayFixture) EpayWallet(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
	return g.create(ctx, r)
}
func (g walletGatewayFixture) StripeWallet(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
	return g.create(ctx, r)
}
func (g walletGatewayFixture) Creem(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
	return g.create(ctx, r)
}

func TestStripeWalletQuoteAndCheckoutPreserveCreditBasis(t *testing.T) {
	f := newTopupFixture(t, 10)
	cfg := contract.WalletConfig{PaymentAllowed: true, QuotaPerUnit: 10, StripeMinimum: 1, StripeUnitPrice: 3, StripePriceID: "price_wallet", StripePromotionCodes: true, Discounts: map[int]float64{2: 0.5}}
	accounts := identity.New(identity.Dependencies{DB: f.db})
	var submitted contract.CheckoutRequest
	gateway := walletGatewayFixture{create: func(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
		submitted = r
		row, err := f.store.Get(ctx, r.TradeNo)
		require.NoError(t, err)
		assert.Equal(t, common.TopUpStatusPending, row.Status)
		assert.Equal(t, 4.0, row.Money)
		assert.EqualValues(t, 2, row.Amount)
		return contract.CheckoutSession{PayLink: "https://stripe.example/pay"}, nil
	}}
	ratio := 2.0
	service := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(string) float64 { return ratio }, TopUps: f.store, Gateway: gateway, ValidateRedirect: func(target string) error {
		if strings.HasPrefix(target, "https://trusted.example/") {
			return nil
		}
		return errors.New("untrusted")
	}})
	quote, err := service.StripeQuote(t.Context(), f.user.Id, 2)
	require.NoError(t, err)
	assert.Equal(t, 6.0, quote.Money)
	assert.Equal(t, 4.0, quote.CreditBase)
	assert.Equal(t, 40, quote.CreditedQuota)
	result, err := service.StartStripe(t.Context(), f.user.Id, contract.StripeWalletRequest{Amount: 2, PaymentMethod: "stripe", SuccessURL: "https://trusted.example/done"})
	require.NoError(t, err)
	assert.Equal(t, "https://stripe.example/pay", result.PayLink)
	assert.True(t, strings.HasPrefix(result.OrderID, "ref_"))
	assert.Equal(t, "price_wallet", submitted.ProductID)
	assert.EqualValues(t, 2, submitted.InputAmount)
	assert.True(t, submitted.AllowPromotionCodes)
	_, err = f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: result.OrderID, Provider: "stripe"})
	require.NoError(t, err)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 50, f.user.Quota)
	ratio = 0
	quote, err = service.StripeQuote(t.Context(), f.user.Id, 1)
	require.NoError(t, err)
	assert.Equal(t, 10, quote.CreditedQuota)
	ratio = math.Inf(1)
	_, err = service.StripeQuote(t.Context(), f.user.Id, 1)
	require.Error(t, err)
	ratio = 1
	_, err = service.StripeQuote(t.Context(), f.user.Id, 10001)
	require.Error(t, err)
	handler := billinghttp.New(billing.New(billing.Dependencies{Purchases: service}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
	router.POST("/stripe", handler.RequestStripePay)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(`{"amount":2,"payment_method":"stripe","success_url":"https://evil.example/"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	var count int64
	require.NoError(t, f.db.Model(&entity.TopUp{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	cfg.QuotaPerUnit = math.NaN()
	_, err = service.StripeQuote(t.Context(), f.user.Id, 1)
	require.Error(t, err)
}

func TestWalletCheckoutFailureRetainsStripeAndCreemOrders(t *testing.T) {
	for _, provider := range []string{"stripe", "creem"} {
		t.Run(provider, func(t *testing.T) {
			f := newTopupFixture(t, 10)
			cfg := contract.WalletConfig{PaymentAllowed: true, QuotaPerUnit: 10, StripeMinimum: 1, StripeUnitPrice: 1, StripePriceID: "price_wallet", CreemProducts: `[{"productId":"prod_credit","name":"Credit","price":2.5,"quota":123}]`}
			accounts := identity.New(identity.Dependencies{DB: f.db})
			reference := ""
			gateway := walletGatewayFixture{create: func(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
				reference = r.TradeNo
				row, err := f.store.Get(ctx, r.TradeNo)
				require.NoError(t, err)
				assert.Equal(t, provider, row.PaymentProvider)
				if provider == "creem" {
					assert.EqualValues(t, 123, r.Quota)
					assert.Equal(t, "prod_credit", r.ProductID)
					assert.EqualValues(t, 123, row.Amount)
				}
				return contract.CheckoutSession{}, errors.New("provider timeout")
			}}
			service := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(string) float64 { return 1 }, TopUps: f.store, Gateway: gateway})
			if provider == "stripe" {
				_, err := service.StartStripe(t.Context(), f.user.Id, contract.StripeWalletRequest{Amount: 2, PaymentMethod: provider})
				require.EqualError(t, err, "拉起支付失败")
			} else {
				_, err := service.StartCreem(t.Context(), f.user.Id, contract.CreemWalletRequest{ProductId: "prod_credit", PaymentMethod: provider})
				require.EqualError(t, err, "拉起支付失败")
			}
			row, err := f.store.Get(t.Context(), reference)
			require.NoError(t, err)
			assert.Equal(t, common.TopUpStatusPending, row.Status)
			_, err = f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: reference, Provider: provider})
			require.NoError(t, err)
			assert.EqualValues(t, 1, f.events.Load())
		})
	}
}

func TestCreemWalletSelectionAndCreditValidation(t *testing.T) {
	f := newTopupFixture(t, 10)
	accounts := identity.New(identity.Dependencies{DB: f.db})
	cfg := contract.WalletConfig{PaymentAllowed: true, CreemProducts: `[{"productId":"product","price":1,"quota":50}]`}
	called := false
	service := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, TopUps: f.store, Gateway: walletGatewayFixture{create: func(context.Context, contract.CheckoutRequest) (contract.CheckoutSession, error) {
		called = true
		return contract.CheckoutSession{CheckoutURL: "https://creem.example/pay"}, nil
	}}})
	_, err := service.StartCreem(t.Context(), f.user.Id, contract.CreemWalletRequest{ProductId: "missing", PaymentMethod: "creem"})
	require.Error(t, err)
	assert.False(t, called)
	cfg.CreemProducts = `[{"productId":"product","price":1,"quota":0}]`
	_, err = service.StartCreem(t.Context(), f.user.Id, contract.CreemWalletRequest{ProductId: "product", PaymentMethod: "creem"})
	require.Error(t, err)
	assert.False(t, called)
	cfg.CreemProducts = `[{"productId":"product","price":1,"quota":50}]`
	handler := billinghttp.New(billing.New(billing.Dependencies{Purchases: service}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
	router.POST("/creem", handler.RequestCreemPay)
	response := redemptionRequest(t, router, http.MethodPost, "/creem", map[string]any{"product_id": "product", "payment_method": "creem", "quota": 999999, "price": 0, "user_id": 999})
	assert.Equal(t, "success", response.Message)
	assert.True(t, called)
	var row entity.TopUp
	require.NoError(t, f.db.First(&row).Error)
	assert.EqualValues(t, 50, row.Amount)
	assert.Equal(t, 1.0, row.Money)
	assert.Equal(t, f.user.Id, row.UserId)
	_, err = purchases.ValidateCredit(decimal.Zero)
	assert.EqualError(t, err, "充值额度必须大于 0")
	_, err = purchases.ValidateCredit(decimal.NewFromInt(common.MaxWalletQuota + 1))
	assert.EqualError(t, err, "充值额度超出系统可表示范围")
}

func (g walletGatewayFixture) WaffoWallet(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
	return g.create(ctx, r)
}
func (g walletGatewayFixture) PancakeWallet(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
	return g.create(ctx, r)
}

func TestWaffoWalletQuoteAppliesDisplayUnitsAndServerPricing(t *testing.T) {
	f := newTopupFixture(t, 500000)
	accounts := identity.New(identity.Dependencies{DB: f.db})
	cfg := contract.WalletConfig{QuotaPerUnit: 500000, WaffoUnitPrice: 2.5, PancakeUnitPrice: 2.5, WaffoMinimum: 1, PancakeMinimum: 1, Discounts: map[int]float64{10: 0.8, 1500000: 0.5, 20: 0}}
	ratio := 1.2
	service := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(string) float64 { return ratio }, TopUps: f.store})
	for _, provider := range []string{"waffo", "waffo_pancake"} {
		for _, tc := range []struct {
			amount       int64
			tokens       bool
			ratio, money float64
			stored       int64
		}{{10, false, 1.2, 24, 10}, {1500000, true, 1.2, 4.5, 3}, {20, false, 1, 50, 20}} {
			cfg.TokensDisplay, ratio = tc.tokens, tc.ratio
			quote, err := service.WaffoQuote(t.Context(), f.user.Id, tc.amount, provider)
			require.NoError(t, err)
			assert.Equal(t, tc.money, quote.Money)
			assert.Equal(t, tc.stored, quote.StoredAmount)
			assert.EqualValues(t, tc.stored*500000, quote.CreditedQuota)
		}
		cfg.TokensDisplay = true
		_, err := service.WaffoQuote(t.Context(), f.user.Id, 1, provider)
		require.Error(t, err, "a token amount below one persisted unit cannot settle")
		cfg.TokensDisplay = false
		_, err = service.WaffoQuote(t.Context(), f.user.Id, math.MaxInt64, provider)
		require.Error(t, err)
		ratio = math.NaN()
		_, err = service.WaffoQuote(t.Context(), f.user.Id, 10, provider)
		require.Error(t, err)
		ratio = 1
		cfg.Discounts[10] = math.Inf(1)
		_, err = service.WaffoQuote(t.Context(), f.user.Id, 10, provider)
		require.Error(t, err)
		cfg.Discounts[10] = 0.8
	}
}

func TestWaffoWalletCheckoutKeepsPendingOrdersForRetryAndUsesServerMethods(t *testing.T) {
	for _, provider := range []string{"waffo", "waffo_pancake"} {
		t.Run(provider, func(t *testing.T) {
			f := newTopupFixture(t, 10)
			accounts := identity.New(identity.Dependencies{DB: f.db})
			cfg := contract.WalletConfig{PaymentAllowed: true, QuotaPerUnit: 10, TokensDisplay: true, WaffoUnitPrice: 2, PancakeUnitPrice: 2, WaffoMinimum: 2, PancakeMinimum: 2, WaffoEnabled: true, WaffoConfigured: true, PancakeProductID: "server-product", WaffoMethods: []constant.WaffoPayMethod{{PayMethodType: "CARD", PayMethodName: "VISA"}}, Gateway: contract.GatewayConfig{WaffoMerchantID: "merchant", WaffoPrivateKey: "private"}}
			var submitted contract.CheckoutRequest
			fail := false
			gateway := walletGatewayFixture{create: func(ctx context.Context, r contract.CheckoutRequest) (contract.CheckoutSession, error) {
				submitted = r
				row, err := f.store.Get(ctx, r.TradeNo)
				require.NoError(t, err)
				assert.Equal(t, common.TopUpStatusPending, row.Status)
				assert.EqualValues(t, 2, row.Amount)
				assert.Equal(t, 5.0, row.Money)
				assert.Equal(t, provider, row.PaymentProvider)
				if fail {
					return contract.CheckoutSession{}, context.DeadlineExceeded
				}
				return contract.CheckoutSession{PaymentURL: "https://pay.example", CheckoutURL: "https://checkout.example", SessionID: "session", Token: "token"}, nil
			}}
			service := purchases.New(purchases.Dependencies{Config: func() contract.WalletConfig { return cfg }, Buyer: accounts.CheckoutBuyer, GroupRatio: func(string) float64 { return 1 }, TopUps: f.store, Gateway: gateway})
			index := 0
			input := contract.WaffoWalletRequest{Amount: 25, PayMethodIndex: &index, PayMethodType: "forged", PayMethodName: "forged"}
			handler := billinghttp.New(billing.New(billing.Dependencies{Purchases: service}), billinghttp.ManagementHooks{})
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
			if provider == "waffo" {
				router.POST("/pay", handler.RequestWaffoPay)
			} else {
				router.POST("/pay", handler.RequestWaffoPancakePay)
			}
			response := redemptionRequest(t, router, http.MethodPost, "/pay", input)
			assert.Equal(t, "success", response.Message)
			var data map[string]any
			require.NoError(t, common.Unmarshal(response.Data, &data))
			assert.Equal(t, submitted.TradeNo, data["order_id"])
			if provider == "waffo" {
				assert.Equal(t, "https://pay.example", data["payment_url"])
				assert.Equal(t, "CARD", submitted.PayMethodType)
				assert.Equal(t, "VISA", submitted.PayMethodName)
				index = -1
				_, err := service.StartWaffo(t.Context(), f.user.Id, input, provider)
				require.EqualError(t, err, "不支持的支付方式")
				index = 0
			} else {
				assert.Equal(t, "token", data["token"])
				assert.Equal(t, "server-product", submitted.ProductID)
				assert.Equal(t, f.user.Id, submitted.UserID)
			}
			fail = true
			_, err := service.StartWaffo(t.Context(), f.user.Id, input, provider)
			require.EqualError(t, err, "拉起支付失败")
			row, err := f.store.Get(t.Context(), submitted.TradeNo)
			require.NoError(t, err)
			assert.Equal(t, common.TopUpStatusPending, row.Status)
			for range 2 {
				_, err = f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: submitted.TradeNo, Provider: provider})
				require.NoError(t, err)
			}
			require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
			assert.Equal(t, 30, f.user.Quota)
			assert.EqualValues(t, 1, f.events.Load())
			cfg.PaymentAllowed = false
			_, err = service.StartWaffo(t.Context(), f.user.Id, input, provider)
			require.Error(t, err)
			var count int64
			require.NoError(t, f.db.Model(&entity.TopUp{}).Count(&count).Error)
			assert.EqualValues(t, 2, count)
		})
	}
}
