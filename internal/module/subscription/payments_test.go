package subscription_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	billingcheckout "github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/identity"
	subscriptioncontract "github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	billingentity "github.com/QuantumNous/new-api/internal/module/billing/entity"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/payments"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type paymentFixture struct {
	db               *gorm.DB
	store            *payments.Store
	user             identityentity.User
	plan             entity.SubscriptionPlan
	logs             atomic.Int64
	cachedDebits     atomic.Int64
	cacheFailure     atomic.Bool
	cancelAfterDebit context.CancelFunc
	logCanceled      atomic.Bool
}

func newPaymentFixture(t *testing.T, unit float64) *paymentFixture {
	t.Helper()
	db, members := newMembershipStore(t)
	f := &paymentFixture{db: db, user: identityentity.User{Username: "payment-user", Password: "unused", Quota: 100, Group: "default", AuthVersion: 1}, plan: entity.SubscriptionPlan{Title: "Payment plan", Enabled: true, PriceAmount: 1.25, DurationUnit: entity.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, UpgradeGroup: "pro", DowngradeGroup: "default"}}
	require.NoError(t, db.Create(&f.user).Error)
	require.NoError(t, db.Create(&f.plan).Error)
	f.store = payments.New(payments.Dependencies{DB: db, Catalog: catalog.New(catalog.Dependencies{DB: db}), Members: members, Billing: billing.New(billing.Dependencies{DB: db}), QuotaPerUnit: func() float64 { return unit }, AfterDebit: func(id, amount int) error {
		f.cachedDebits.Add(int64(amount))
		if f.cancelAfterDebit != nil {
			f.cancelAfterDebit()
		}
		if f.cacheFailure.Load() {
			return errors.New("cache unavailable")
		}
		return nil
	}, Log: func(ctx context.Context, id int, message string) {
		f.logCanceled.Store(ctx.Err() != nil)
		f.logs.Add(1)
	}})
	return f
}

func TestSubscriptionPaymentCompletionIsAtomicAndIdempotent(t *testing.T) {
	f := newPaymentFixture(t, 10)
	order := entity.SubscriptionOrder{UserId: f.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "verified-order", PaymentProvider: "epay", PaymentMethod: "alipay", Status: common.TopUpStatusPending}
	require.NoError(t, f.store.Create(t.Context(), &order))
	require.NoError(t, f.db.Model(&f.plan).Update("enabled", false).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- f.store.Complete(t.Context(), order.TradeNo, "verified", "epay", "wxpay") }()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	saved, err := f.store.Get(t.Context(), order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, common.TopUpStatusSuccess, saved.Status)
	assert.Equal(t, "wxpay", saved.PaymentMethod)
	assert.Equal(t, "verified", saved.ProviderPayload)
	var receipts []billingentity.TopUp
	require.NoError(t, f.db.Find(&receipts).Error)
	require.Len(t, receipts, 1)
	assert.Equal(t, "wxpay", receipts[0].PaymentMethod)
	assert.Equal(t, "epay", receipts[0].PaymentProvider)
	assert.Equal(t, 2.5, receipts[0].Money)
	assert.Zero(t, receipts[0].Amount)
	var subs []entity.UserSubscription
	require.NoError(t, f.db.Find(&subs).Error)
	require.Len(t, subs, 1)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, "pro", f.user.Group)
	assert.Equal(t, 100, f.user.Quota)
	assert.EqualValues(t, 1, f.logs.Load())
	require.NoError(t, f.store.FinishPending(t.Context(), order.TradeNo, "epay", common.TopUpStatusFailed))
	require.NoError(t, f.store.FinishPending(t.Context(), order.TradeNo, "epay", common.TopUpStatusExpired))
	saved, err = f.store.Get(t.Context(), order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, common.TopUpStatusSuccess, saved.Status)
	assert.Equal(t, "verified", saved.ProviderPayload)
	assert.ErrorIs(t, f.store.Complete(t.Context(), order.TradeNo, "", "stripe", ""), billingcontract.ErrPaymentMethodMismatch)
}

func TestSubscriptionReceiptFailureRollsBackGrantAndOrder(t *testing.T) {
	f := newPaymentFixture(t, 10)
	order := entity.SubscriptionOrder{UserId: f.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "receipt-failure", PaymentProvider: "stripe", PaymentMethod: "stripe", Status: common.TopUpStatusPending}
	require.NoError(t, f.store.Create(t.Context(), &order))
	require.NoError(t, f.db.Exec("ALTER TABLE top_ups ADD CONSTRAINT reject_subscription_receipt CHECK (money <> 2.5)").Error)
	require.Error(t, f.store.Complete(t.Context(), order.TradeNo, "verified", "stripe", ""))
	saved, err := f.store.Get(t.Context(), order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, common.TopUpStatusPending, saved.Status)
	var count int64
	require.NoError(t, f.db.Model(&entity.UserSubscription{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, "default", f.user.Group)
	assert.Zero(t, f.logs.Load())
	require.NoError(t, f.db.Exec("ALTER TABLE top_ups DROP CONSTRAINT reject_subscription_receipt").Error)
	require.NoError(t, f.db.Exec("ALTER TABLE subscription_orders RENAME TO unavailable_subscription_orders").Error)
	err = f.store.Complete(t.Context(), order.TradeNo, "", "stripe", "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, payments.ErrOrderNotFound)
	err = f.store.FinishPending(t.Context(), order.TradeNo, "stripe", common.TopUpStatusExpired)
	require.Error(t, err)
	assert.NotErrorIs(t, err, payments.ErrOrderNotFound)
	require.NoError(t, f.db.Exec("ALTER TABLE unavailable_subscription_orders RENAME TO subscription_orders").Error)
	require.NoError(t, f.store.Complete(t.Context(), order.TradeNo, "verified", "stripe", ""))
	assert.EqualValues(t, 1, f.logs.Load())
}

func TestSubscriptionReceiptCannotCompleteWalletTopUpWithSameReference(t *testing.T) {
	f := newPaymentFixture(t, 10)
	order := entity.SubscriptionOrder{UserId: f.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "shared-reference", PaymentProvider: "epay", PaymentMethod: "alipay", Status: common.TopUpStatusPending}
	require.NoError(t, f.store.Create(t.Context(), &order))
	topup := billingentity.TopUp{UserId: f.user.Id, Amount: 100, Money: 2.5, TradeNo: order.TradeNo, PaymentProvider: "epay", PaymentMethod: "alipay", Status: common.TopUpStatusPending}
	require.NoError(t, f.db.Create(&topup).Error)
	require.ErrorIs(t, f.store.Complete(t.Context(), order.TradeNo, "", "epay", "alipay"), billingcontract.ErrPaymentMethodMismatch)
	require.NoError(t, f.db.First(&topup, topup.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, topup.Status)
	assert.EqualValues(t, 100, topup.Amount)
	var count int64
	require.NoError(t, f.db.Model(&entity.UserSubscription{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 100, f.user.Quota)
	assert.Equal(t, "default", f.user.Group)
}

func TestSubscriptionBalancePurchaseSerializesWalletAndRollsBackOrderFailure(t *testing.T) {
	f := newPaymentFixture(t, 10)
	require.NoError(t, f.db.Model(&f.plan).Update("price_amount", 6).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- f.store.PurchaseWithBalance(t.Context(), f.user.Id, f.plan.Id) }()
	}
	close(start)
	a, b := <-results, <-results
	require.True(t, (a == nil) != (b == nil), "one wallet purchase must succeed: %v / %v", a, b)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 40, f.user.Quota)
	assert.EqualValues(t, 60, f.cachedDebits.Load())
	assert.EqualValues(t, 1, f.logs.Load())
	var orders []entity.SubscriptionOrder
	require.NoError(t, f.db.Find(&orders).Error)
	require.Len(t, orders, 1)
	assert.Equal(t, "balance", orders[0].PaymentProvider)
	assert.Equal(t, "charged_quota=60", orders[0].ProviderPayload)
	// A failed order insert rolls back both the earlier debit and group/grant.
	other := newPaymentFixture(t, 10)
	require.NoError(t, other.db.Exec("ALTER TABLE subscription_orders ADD CONSTRAINT reject_balance_order CHECK (payment_provider <> 'balance')").Error)
	require.Error(t, other.store.PurchaseWithBalance(t.Context(), other.user.Id, other.plan.Id))
	require.NoError(t, other.db.First(&other.user, other.user.Id).Error)
	assert.Equal(t, 100, other.user.Quota)
	assert.Equal(t, "default", other.user.Group)
	var count int64
	require.NoError(t, other.db.Model(&entity.UserSubscription{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.Zero(t, other.cachedDebits.Load())
	assert.Zero(t, other.logs.Load())
	require.NoError(t, other.db.Exec("ALTER TABLE subscription_orders DROP CONSTRAINT reject_balance_order").Error)
	other.cacheFailure.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	other.cancelAfterDebit = cancel
	require.NoError(t, other.store.PurchaseWithBalance(ctx, other.user.Id, other.plan.Id))
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.False(t, other.logCanceled.Load(), "a committed purchase must still emit its audit event")
	require.NoError(t, other.db.First(&other.user, other.user.Id).Error)
	assert.Equal(t, 87, other.user.Quota) // ceil(1.25 * 10)
	assert.EqualValues(t, 1, other.logs.Load())
}

func TestSubscriptionBalanceRejectsInvalidMoneyAndPreservesHTTPIdentity(t *testing.T) {
	for _, unit := range []float64{math.NaN(), math.Inf(1), -1, float64(common.MaxWalletQuota) * 2} {
		f := newPaymentFixture(t, unit)
		require.Error(t, f.store.PurchaseWithBalance(t.Context(), f.user.Id, f.plan.Id))
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		assert.Equal(t, 100, f.user.Quota)
		assert.Zero(t, f.cachedDebits.Load())
	}
	f := newPaymentFixture(t, 10)
	require.NoError(t, f.db.Exec("UPDATE subscription_plans SET price_amount = 'NaN' WHERE id = ?", f.plan.Id).Error)
	require.Error(t, f.store.PurchaseWithBalance(t.Context(), f.user.Id, f.plan.Id))
	require.NoError(t, f.db.Model(&f.plan).Update("price_amount", 1.25).Error)
	allowed := false
	handler := subscriptionhttp.New(subscription.New(subscription.Dependencies{DB: f.db, Payments: f.store, PaymentAllowed: func() bool { return allowed }}))
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
	router.POST("/balance/pay", handler.SubscriptionRequestBalancePay)
	body := planRequest(t, router, http.MethodPost, "/balance/pay", map[string]any{"plan_id": f.plan.Id, "user_id": 999})
	assert.False(t, body.Success)
	allowed = true
	body = planRequest(t, router, http.MethodPost, "/balance/pay", map[string]any{"plan_id": f.plan.Id, "user_id": 999})
	require.True(t, body.Success, body.Message)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 87, f.user.Quota)
	var order entity.SubscriptionOrder
	require.NoError(t, f.db.First(&order).Error)
	assert.Equal(t, f.user.Id, order.UserId)
}

type checkoutGatewayFixture struct {
	create   func(context.Context, billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error)
	verifier *billingcheckout.Client
}

func (g checkoutGatewayFixture) ValidateSubscription(string, string) error { return nil }
func (g checkoutGatewayFixture) CreateSubscription(ctx context.Context, r billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error) {
	return g.create(ctx, r)
}
func (g checkoutGatewayFixture) VerifyEpay(params map[string]string) (billingcontract.VerifiedPayment, error) {
	return g.verifier.VerifyEpay(params)
}
func (g checkoutGatewayFixture) ReturnURL(suffix string) string {
	return "https://console.example" + suffix
}

func TestSubscriptionCheckoutPersistsBeforeGatewayAndPreservesResponseContracts(t *testing.T) {
	for _, provider := range []string{"stripe", "creem", "waffo_pancake", "epay"} {
		t.Run(provider, func(t *testing.T) {
			f := newPaymentFixture(t, 10)
			require.NoError(t, f.db.Model(&f.plan).Updates(map[string]any{"stripe_price_id": "price_month", "creem_product_id": "prod_creem", "waffo_pancake_product_id": "prod_waffo"}).Error)
			require.NoError(t, f.db.Model(&f.user).Updates(map[string]any{"email": "buyer@example.test", "stripe_customer": "cus_saved"}).Error)
			accounts := identity.New(identity.Dependencies{DB: f.db})
			var reference string
			gateways := checkoutGatewayFixture{create: func(ctx context.Context, r billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error) {
				persisted, err := f.store.Get(ctx, r.TradeNo)
				require.NoError(t, err)
				assert.Equal(t, common.TopUpStatusPending, persisted.Status)
				assert.Equal(t, provider, persisted.PaymentProvider)
				assert.Equal(t, f.user.Id, r.UserID)
				assert.Equal(t, "buyer@example.test", r.Email)
				assert.Equal(t, "cus_saved", r.CustomerID)
				reference = r.TradeNo
				return billingcontract.CheckoutSession{PayLink: "https://stripe.example/pay", CheckoutURL: "https://checkout.example/pay", SessionID: "session", Token: "token", ExpiresAt: "expiry", TokenExpiresAt: "token-expiry", EpayURL: "https://epay.example/submit.php", EpayParams: map[string]string{"sign": "signed"}}, nil
			}}
			runtime := subscription.New(subscription.Dependencies{DB: f.db, Members: memberships.New(memberships.Dependencies{DB: f.db}), Payments: f.store, CheckoutBuyer: accounts.CheckoutBuyer, Gateways: gateways, PaymentAllowed: func() bool { return true }})
			handler := subscriptionhttp.New(runtime)
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
			switch provider {
			case "stripe":
				router.POST("/pay", handler.SubscriptionRequestStripePay)
			case "creem":
				router.POST("/pay", handler.SubscriptionRequestCreemPay)
			case "waffo_pancake":
				router.POST("/pay", handler.SubscriptionRequestWaffoPancakePay)
			case "epay":
				router.POST("/pay", handler.SubscriptionRequestEpay)
			}
			body := planRequest(t, router, http.MethodPost, "/pay", map[string]any{"plan_id": f.plan.Id, "payment_method": "alipay", "user_id": 999, "price": 0.01})
			assert.Equal(t, "success", body.Message)
			var data map[string]any
			require.NoError(t, common.Unmarshal(body.Data, &data))
			switch provider {
			case "stripe":
				assert.Equal(t, map[string]any{"pay_link": "https://stripe.example/pay"}, data)
				assert.True(t, strings.HasPrefix(reference, "sub_ref_"))
			case "creem":
				assert.Equal(t, map[string]any{"checkout_url": "https://checkout.example/pay", "order_id": reference}, data)
			case "waffo_pancake":
				assert.True(t, strings.HasPrefix(reference, "WAFFO_PANCAKE_SUB-"))
				assert.Equal(t, "token", data["token"])
				assert.Equal(t, "token-expiry", data["token_expires_at"])
				assert.Equal(t, reference, data["order_id"])
			case "epay":
				assert.True(t, strings.HasPrefix(reference, "SUBUSR"))
				assert.Equal(t, map[string]any{"sign": "signed"}, data)
			}
			saved, err := f.store.Get(t.Context(), reference)
			require.NoError(t, err)
			assert.Equal(t, f.plan.PriceAmount, saved.Money)
		})
	}
}

func TestSubscriptionCheckoutFailureRetainsOrderForVerifiedCallback(t *testing.T) {
	for _, callbackDuringRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(callbackDuringRequest), func(t *testing.T) {
			f := newPaymentFixture(t, 10)
			require.NoError(t, f.db.Model(&f.plan).Update("stripe_price_id", "price_month").Error)
			accounts := identity.New(identity.Dependencies{DB: f.db})
			reference := ""
			gateways := checkoutGatewayFixture{create: func(ctx context.Context, r billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error) {
				reference = r.TradeNo
				if callbackDuringRequest {
					require.NoError(t, f.store.Complete(ctx, reference, "verified", "stripe", ""))
				}
				return billingcontract.CheckoutSession{}, errors.New("gateway timeout")
			}}
			runtime := subscription.New(subscription.Dependencies{DB: f.db, Members: memberships.New(memberships.Dependencies{DB: f.db}), Payments: f.store, CheckoutBuyer: accounts.CheckoutBuyer, Gateways: gateways, PaymentAllowed: func() bool { return true }})
			_, err := runtime.StartCheckout(t.Context(), f.user.Id, "stripe", subscriptioncontract.CheckoutInput{PlanId: f.plan.Id})
			require.Error(t, err)
			var checkoutErr *subscription.CheckoutError
			require.ErrorAs(t, err, &checkoutErr)
			assert.Equal(t, "gateway", checkoutErr.Stage)
			saved, err := f.store.Get(t.Context(), reference)
			require.NoError(t, err)
			if callbackDuringRequest {
				assert.Equal(t, common.TopUpStatusSuccess, saved.Status)
			} else {
				assert.Equal(t, common.TopUpStatusPending, saved.Status)
			}
			require.NoError(t, f.store.Complete(t.Context(), reference, "verified", "stripe", ""))
			assert.EqualValues(t, 1, f.logs.Load())
		})
	}
	f := newPaymentFixture(t, 10)
	accounts := identity.New(identity.Dependencies{DB: f.db})
	called := false
	runtime := subscription.New(subscription.Dependencies{DB: f.db, Members: memberships.New(memberships.Dependencies{DB: f.db}), Payments: f.store, CheckoutBuyer: accounts.CheckoutBuyer, Gateways: checkoutGatewayFixture{create: func(context.Context, billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error) {
		called = true
		return billingcontract.CheckoutSession{}, nil
	}}, PaymentAllowed: func() bool { return true }})
	_, err := runtime.StartCheckout(t.Context(), f.user.Id, "stripe", subscriptioncontract.CheckoutInput{PlanId: f.plan.Id})
	require.Error(t, err)
	assert.False(t, called)
	require.NoError(t, f.db.Model(&f.plan).Updates(map[string]any{"stripe_price_id": "price_month", "enabled": false}).Error)
	_, err = runtime.StartCheckout(t.Context(), f.user.Id, "stripe", subscriptioncontract.CheckoutInput{PlanId: f.plan.Id})
	require.Error(t, err)
	assert.False(t, called)
	require.NoError(t, f.db.Model(&f.plan).Updates(map[string]any{"enabled": true, "max_purchase_per_user": 1}).Error)
	require.NoError(t, f.db.Create(&entity.UserSubscription{UserId: f.user.Id, PlanId: f.plan.Id, Status: "cancelled", EndTime: 1}).Error)
	_, err = runtime.StartCheckout(t.Context(), f.user.Id, "stripe", subscriptioncontract.CheckoutInput{PlanId: f.plan.Id})
	require.Error(t, err)
	assert.False(t, called)
	require.NoError(t, f.db.Model(&f.plan).Update("max_purchase_per_user", 0).Error)
	require.NoError(t, f.db.Exec("ALTER TABLE subscription_orders ADD CONSTRAINT reject_checkout_fixture CHECK (payment_provider <> 'stripe')").Error)
	_, err = runtime.StartCheckout(t.Context(), f.user.Id, "stripe", subscriptioncontract.CheckoutInput{PlanId: f.plan.Id})
	require.Error(t, err)
	assert.False(t, called)
}

func TestSubscriptionEpayCallbacksRequireSignatureAndRemainIdempotent(t *testing.T) {
	f := newPaymentFixture(t, 10)
	order := entity.SubscriptionOrder{UserId: f.user.Id, PlanId: f.plan.Id, Money: 1.25, TradeNo: "epay-sub", PaymentProvider: "epay", PaymentMethod: "alipay", Status: common.TopUpStatusPending}
	require.NoError(t, f.store.Create(t.Context(), &order))
	gateway := billingcheckout.New(billingcheckout.Options{Config: func() billingcontract.GatewayConfig {
		return billingcontract.GatewayConfig{EpayAddress: "https://epay.example", EpayID: "merchant", EpayKey: "secret", ServerAddress: "https://console.example"}
	}})
	handler := subscriptionhttp.New(subscription.New(subscription.Dependencies{Payments: f.store, Gateways: gateway}))
	router := gin.New()
	router.GET("/notify", handler.SubscriptionEpayNotify)
	router.POST("/notify", handler.SubscriptionEpayNotify)
	router.GET("/return", handler.SubscriptionEpayReturn)
	signed := epay.GenerateParams(map[string]string{"out_trade_no": order.TradeNo, "type": "wxpay", "trade_status": epay.StatusTradeSuccess, "money": "1.25"}, "secret")
	values := url.Values{}
	for key, value := range signed {
		values.Set(key, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/notify?out_trade_no=epay-sub&sign=bad", nil))
	assert.Equal(t, "fail", response.Body.String())
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Equal(t, "success", response.Body.String())
	}
	assert.EqualValues(t, 1, f.logs.Load())
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/return?"+values.Encode(), nil))
	assert.Equal(t, http.StatusFound, response.Code)
	assert.Equal(t, "https://console.example/wallet?pay=success", response.Header().Get("Location"))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/return?sign=bad", nil))
	assert.Equal(t, "https://console.example/wallet?pay=fail", response.Header().Get("Location"))
}
