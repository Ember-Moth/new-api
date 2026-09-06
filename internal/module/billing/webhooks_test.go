package billing_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	waffoutils "github.com/waffo-com/waffo-go/utils"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	subentity "github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	"github.com/QuantumNous/new-api/internal/module/subscription/payments"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	stripehook "github.com/stripe/stripe-go/v81/webhook"
)

type webhookFixture struct {
	topup         *topupFixture
	router        *gin.Engine
	cfg           webhooks.Config
	subscriptions *payments.Store
	plan          subentity.SubscriptionPlan
}

func newWebhookFixture(t *testing.T) *webhookFixture {
	t.Helper()
	f := &webhookFixture{topup: newTopupFixture(t, 10), cfg: webhooks.Config{PaymentAllowed: true, StripeSecret: "whsec_fixture", CreemSecret: "creem_fixture"}, plan: subentity.SubscriptionPlan{Title: "Webhook plan", Enabled: true, DurationUnit: subentity.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100}}
	db := f.topup.db
	require.NoError(t, db.Create(&f.plan).Error)
	accounts := identity.New(identity.Dependencies{DB: db})
	plans := catalog.New(catalog.Dependencies{DB: db})
	members := memberships.New(memberships.Dependencies{DB: db, Groups: memberships.UserGroups{Lock: accounts.LockUserGroup, Set: accounts.SetUserGroup, Refresh: func(int) error { return nil }}})
	f.subscriptions = payments.New(payments.Dependencies{DB: db, Catalog: plans, Members: members, Billing: billing.New(billing.Dependencies{DB: db})})
	processor := webhooks.New(webhooks.Dependencies{Config: func() webhooks.Config { return f.cfg }, TopUps: f.topup.store, Subscriptions: f.subscriptions})
	handler := billinghttp.New(billing.New(billing.Dependencies{Webhooks: processor}), billinghttp.ManagementHooks{})
	f.router = gin.New()
	f.router.POST("/stripe", handler.StripeWebhook)
	f.router.POST("/creem", handler.CreemWebhook)
	return f
}
func stripeFixturePayload(t *testing.T, event, reference, status, payment string) []byte {
	t.Helper()
	body, err := common.Marshal(map[string]any{"id": "evt_fixture", "object": "event", "type": event, "api_version": stripe.APIVersion, "data": map[string]any{"object": map[string]any{"id": "cs_fixture", "object": "checkout.session", "client_reference_id": reference, "status": status, "payment_status": payment, "customer": "cus_fixture", "amount_total": 250, "currency": "usd"}}})
	require.NoError(t, err)
	return body
}
func webhookRequest(f *webhookFixture, provider string, body []byte, valid bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/"+provider, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if provider == "stripe" {
		signed := stripehook.GenerateTestSignedPayload(&stripehook.UnsignedPayload{Payload: body, Secret: f.cfg.StripeSecret})
		signature := signed.Header
		if !valid {
			signature = "bad"
		}
		request.Header.Set("Stripe-Signature", signature)
	} else {
		mac := hmac.New(sha256.New, []byte(f.cfg.CreemSecret))
		_, _ = mac.Write(body)
		signature := hex.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "bad"
		}
		request.Header.Set("creem-signature", signature)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func TestStripeWebhookRetriesStorageFailuresAndCreditsExactlyOnce(t *testing.T) {
	f := newWebhookFixture(t)
	row := entity.TopUp{UserId: f.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: "stripe-wallet", PaymentProvider: "stripe", Status: common.TopUpStatusPending}
	require.NoError(t, f.topup.store.Create(t.Context(), &row))
	paid := stripeFixturePayload(t, "checkout.session.completed", row.TradeNo, "complete", "paid")
	assert.Equal(t, http.StatusBadRequest, webhookRequest(f, "stripe", paid, false).Code)
	unpaid := stripeFixturePayload(t, "checkout.session.completed", row.TradeNo, "complete", "unpaid")
	assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", unpaid, true).Code)
	assert.Zero(t, f.topup.credits.Load())
	// A failed subscription lookup must not fall through to the wallet table.
	require.NoError(t, f.topup.db.Exec("ALTER TABLE subscription_orders RENAME TO unavailable_subscription_orders").Error)
	assert.Equal(t, http.StatusInternalServerError, webhookRequest(f, "stripe", paid, true).Code)
	assert.Zero(t, f.topup.credits.Load())
	require.NoError(t, f.topup.db.Exec("ALTER TABLE unavailable_subscription_orders RENAME TO subscription_orders").Error)
	require.NoError(t, f.topup.db.Exec("ALTER TABLE top_ups ADD CONSTRAINT reject_webhook_credit CHECK (status <> 'success')").Error)
	assert.Equal(t, http.StatusInternalServerError, webhookRequest(f, "stripe", paid, true).Code)
	require.NoError(t, f.topup.db.First(&f.topup.user, f.topup.user.Id).Error)
	assert.Equal(t, 10, f.topup.user.Quota)
	require.NoError(t, f.topup.db.Exec("ALTER TABLE top_ups DROP CONSTRAINT reject_webhook_credit").Error)
	delayed := stripeFixturePayload(t, "checkout.session.async_payment_succeeded", row.TradeNo, "complete", "paid")
	assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", delayed, true).Code)
	assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", paid, true).Code)
	assert.EqualValues(t, 25, f.topup.credits.Load())
	assert.EqualValues(t, 1, f.topup.events.Load())
	require.NoError(t, f.topup.db.First(&f.topup.user, f.topup.user.Id).Error)
	assert.Equal(t, 35, f.topup.user.Quota)
	assert.Equal(t, "cus_fixture", f.topup.user.StripeCustomer)
	lateFailure := stripeFixturePayload(t, "checkout.session.async_payment_failed", row.TradeNo, "complete", "unpaid")
	assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", lateFailure, true).Code)
	lateExpiry := stripeFixturePayload(t, "checkout.session.expired", row.TradeNo, "expired", "unpaid")
	assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", lateExpiry, true).Code)
	require.NoError(t, f.topup.db.First(&row, row.Id).Error)
	assert.Equal(t, common.TopUpStatusSuccess, row.Status)
}

func TestStripeWebhookRoutesSubscriptionEventsAndRejectsForeignProvider(t *testing.T) {
	for _, test := range []struct{ event, status, payment, want string }{{"checkout.session.completed", "complete", "paid", "success"}, {"checkout.session.async_payment_failed", "complete", "unpaid", "failed"}, {"checkout.session.expired", "expired", "unpaid", "expired"}} {
		t.Run(test.event, func(t *testing.T) {
			f := newWebhookFixture(t)
			order := subentity.SubscriptionOrder{UserId: f.topup.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "sub-event", PaymentProvider: "stripe", Status: common.TopUpStatusPending}
			require.NoError(t, f.subscriptions.Create(t.Context(), &order))
			payload := stripeFixturePayload(t, test.event, order.TradeNo, test.status, test.payment)
			assert.Equal(t, http.StatusOK, webhookRequest(f, "stripe", payload, true).Code)
			saved, err := f.subscriptions.Get(t.Context(), order.TradeNo)
			require.NoError(t, err)
			assert.Equal(t, test.want, saved.Status)
			var count int64
			require.NoError(t, f.topup.db.Model(&subentity.UserSubscription{}).Count(&count).Error)
			if test.want == "success" {
				assert.EqualValues(t, 1, count)
			} else {
				assert.Zero(t, count)
			}
			require.NoError(t, f.topup.db.First(&f.topup.user, f.topup.user.Id).Error)
			assert.Equal(t, 10, f.topup.user.Quota)
		})
	}
	f := newWebhookFixture(t)
	order := subentity.SubscriptionOrder{UserId: f.topup.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "foreign-provider", PaymentProvider: "creem", Status: common.TopUpStatusPending}
	require.NoError(t, f.subscriptions.Create(t.Context(), &order))
	require.NoError(t, f.topup.store.Create(t.Context(), &entity.TopUp{UserId: f.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: order.TradeNo, PaymentProvider: "stripe", Status: common.TopUpStatusPending}))
	payload := stripeFixturePayload(t, "checkout.session.completed", order.TradeNo, "complete", "paid")
	assert.Equal(t, http.StatusInternalServerError, webhookRequest(f, "stripe", payload, true).Code)
	assert.Zero(t, f.topup.credits.Load())
}

func TestCreemWebhookSignatureRoutingAndIdempotency(t *testing.T) {
	f := newWebhookFixture(t)
	wallet := entity.TopUp{UserId: f.topup.user.Id, Amount: 123, Money: 2.5, TradeNo: "creem-wallet", PaymentProvider: "creem", Status: common.TopUpStatusPending}
	require.NoError(t, f.topup.store.Create(t.Context(), &wallet))
	payload := []byte(`{"id":"evt_creem","eventType":"checkout.completed","object":{"request_id":"creem-wallet","order":{"status":"paid","type":"onetime"},"customer":{"email":"payer@example.test"}}}`)
	assert.Equal(t, http.StatusUnauthorized, webhookRequest(f, "creem", payload, false).Code)
	assert.Equal(t, http.StatusOK, webhookRequest(f, "creem", payload, true).Code)
	assert.Equal(t, http.StatusOK, webhookRequest(f, "creem", payload, true).Code)
	require.NoError(t, f.topup.db.First(&f.topup.user, f.topup.user.Id).Error)
	assert.Equal(t, 133, f.topup.user.Quota)
	assert.Equal(t, "payer@example.test", f.topup.user.Email)
	assert.EqualValues(t, 1, f.topup.events.Load())
	order := subentity.SubscriptionOrder{UserId: f.topup.user.Id, PlanId: f.plan.Id, Money: 2.5, TradeNo: "creem-sub", PaymentProvider: "creem", Status: common.TopUpStatusPending}
	require.NoError(t, f.subscriptions.Create(t.Context(), &order))
	subscription := []byte(`{"eventType":"checkout.completed","object":{"request_id":"creem-sub","order":{"status":"paid","type":"recurring"}}}`)
	assert.Equal(t, http.StatusOK, webhookRequest(f, "creem", subscription, true).Code)
	var count int64
	require.NoError(t, f.topup.db.Model(&subentity.UserSubscription{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	assert.Equal(t, http.StatusInternalServerError, webhookRequest(f, "creem", []byte(`{"eventType":"checkout.completed","object":{"request_id":"missing","order":{"status":"paid","type":"onetime"}}}`), true).Code)
}

func TestWebhookEnablementPayloadValidationAndSignatureExpiry(t *testing.T) {
	f := newWebhookFixture(t)
	// No wallet products or API keys exist in this configuration. Verification
	// and settlement only require compliance and the corresponding webhook key.
	for _, provider := range []string{"stripe", "creem"} {
		payload := []byte(`{"id":"evt_other","type":"customer.created","eventType":"ignored"}`)
		assert.Equal(t, http.StatusOK, webhookRequest(f, provider, payload, true).Code)
		f.cfg.PaymentAllowed = false
		assert.Equal(t, http.StatusForbidden, webhookRequest(f, provider, payload, true).Code)
		f.cfg.PaymentAllowed = true
		assert.Equal(t, http.StatusBadRequest, webhookRequest(f, provider, []byte(`not-json`), true).Code)
	}
	malformed := []byte(`{"id":"evt_missing","type":"checkout.session.async_payment_succeeded"}`)
	assert.Equal(t, http.StatusBadRequest, webhookRequest(f, "stripe", malformed, true).Code)
	paid := stripeFixturePayload(t, "checkout.session.completed", "missing", "complete", "paid")
	signed := stripehook.GenerateTestSignedPayload(&stripehook.UnsignedPayload{Payload: paid, Secret: f.cfg.StripeSecret, Timestamp: time.Now().Add(-time.Hour)})
	request := httptest.NewRequest(http.MethodPost, "/stripe", bytes.NewReader(paid))
	request.Header.Set("Stripe-Signature", signed.Header)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	f.cfg.CreemSecret = ""
	assert.Equal(t, http.StatusForbidden, webhookRequest(f, "creem", []byte(`{}`), true).Code)
}

type waffoWebhookFixture struct {
	base                          *webhookFixture
	testKey, prodKey, merchantKey *waffoutils.KeyPair
}

func newWaffoWebhookFixture(t *testing.T) *waffoWebhookFixture {
	t.Helper()
	f := &waffoWebhookFixture{base: newWebhookFixture(t)}
	var err error
	f.testKey, err = waffoutils.GenerateKeyPair()
	require.NoError(t, err)
	f.prodKey, err = waffoutils.GenerateKeyPair()
	require.NoError(t, err)
	f.merchantKey, err = waffoutils.GenerateKeyPair()
	require.NoError(t, err)
	f.base.cfg.WaffoEnabled = true
	f.base.cfg.WaffoPrivateKey = f.merchantKey.PrivateKey
	f.base.cfg.WaffoPublicKey = f.testKey.PublicKey
	f.base.cfg.PancakeEnabled = true
	f.base.cfg.PancakeStoreID = "STO_owned"
	processor := webhooks.New(webhooks.Dependencies{Config: func() webhooks.Config { return f.base.cfg }, TopUps: f.base.topup.store, Subscriptions: f.base.subscriptions, PancakePublicKeys: &pancake.WebhookPublicKeys{Test: f.testKey.PublicKey, Prod: f.prodKey.PublicKey}})
	handler := billinghttp.New(billing.New(billing.Dependencies{Webhooks: processor}), billinghttp.ManagementHooks{})
	f.base.router.POST("/waffo", handler.WaffoWebhook)
	f.base.router.POST("/pancake/:env", handler.WaffoPancakeWebhook)
	return f
}
func (f *waffoWebhookFixture) legacyRequest(t *testing.T, reference, status string, valid bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(map[string]any{"eventType": "PAYMENT_NOTIFICATION", "result": map[string]any{"merchantOrderId": reference, "orderStatus": status}})
	require.NoError(t, err)
	signature, err := waffoutils.Sign(string(body), f.testKey.PrivateKey)
	require.NoError(t, err)
	if !valid {
		signature = "invalid"
	}
	request := httptest.NewRequest(http.MethodPost, "/waffo", bytes.NewReader(body))
	request.Header.Set("X-SIGNATURE", signature)
	response := httptest.NewRecorder()
	f.base.router.ServeHTTP(response, request)
	return response
}
func (f *waffoWebhookFixture) pancakeRequest(t *testing.T, route, mode, store, reference, buyer string, key *waffoutils.KeyPair, when time.Time) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(map[string]any{"id": "evt_pancake", "eventType": "order.completed", "mode": mode, "storeId": store, "data": map[string]any{"orderId": "ORD_provider", "orderMerchantExternalId": reference, "merchantProvidedBuyerIdentity": buyer, "amount": "2.5", "currency": "USD"}})
	require.NoError(t, err)
	timestamp := strconv.FormatInt(when.UnixMilli(), 10)
	signature, err := waffoutils.Sign(timestamp+"."+string(body), key.PrivateKey)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/pancake/"+route, bytes.NewReader(body))
	request.Header.Set("X-Waffo-Signature", "t="+timestamp+",v1="+signature)
	response := httptest.NewRecorder()
	f.base.router.ServeHTTP(response, request)
	return response
}

func TestWaffoWebhookKeepsProgressPendingAndSignsRetryResponses(t *testing.T) {
	f := newWaffoWebhookFixture(t)
	row := entity.TopUp{UserId: f.base.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: "waffo-wallet", PaymentProvider: "waffo", Status: common.TopUpStatusPending}
	require.NoError(t, f.base.topup.store.Create(t.Context(), &row))
	assert.Equal(t, http.StatusBadRequest, f.legacyRequest(t, "waffo-wallet", "PAY_SUCCESS", false).Code)
	for _, status := range []string{"PAY_IN_PROGRESS", "AUTHORIZATION_REQUIRED", "AUTHED_WAITING_CAPTURE"} {
		response := f.legacyRequest(t, "waffo-wallet", status, true)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.JSONEq(t, `{"message":"success"}`, response.Body.String())
		assert.True(t, waffoutils.Verify(response.Body.String(), response.Header().Get("X-SIGNATURE"), f.merchantKey.PublicKey))
		require.NoError(t, f.base.topup.db.First(&row, row.Id).Error)
		assert.Equal(t, common.TopUpStatusPending, row.Status)
	}
	require.NoError(t, f.base.topup.db.Exec("ALTER TABLE top_ups ADD CONSTRAINT reject_waffo_credit CHECK (status <> 'success')").Error)
	response := f.legacyRequest(t, "waffo-wallet", "PAY_SUCCESS", true)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"message":"failed"}`, response.Body.String())
	assert.True(t, waffoutils.Verify(response.Body.String(), response.Header().Get("X-SIGNATURE"), f.merchantKey.PublicKey))
	assert.Zero(t, f.base.topup.credits.Load())
	require.NoError(t, f.base.topup.db.Exec("ALTER TABLE top_ups DROP CONSTRAINT reject_waffo_credit").Error)
	for range 2 {
		response = f.legacyRequest(t, "waffo-wallet", "PAY_SUCCESS", true)
		assert.JSONEq(t, `{"message":"success"}`, response.Body.String())
	}
	assert.EqualValues(t, 20, f.base.topup.credits.Load())
	assert.EqualValues(t, 1, f.base.topup.events.Load())
	response = f.legacyRequest(t, "waffo-wallet", "ORDER_CLOSE", true)
	assert.JSONEq(t, `{"message":"success"}`, response.Body.String())
	require.NoError(t, f.base.topup.db.First(&row, row.Id).Error)
	assert.Equal(t, common.TopUpStatusSuccess, row.Status)
	closed := entity.TopUp{UserId: f.base.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: "waffo-close", PaymentProvider: "waffo", Status: common.TopUpStatusPending}
	require.NoError(t, f.base.topup.store.Create(t.Context(), &closed))
	response = f.legacyRequest(t, "waffo-close", "ORDER_CLOSE", true)
	assert.JSONEq(t, `{"message":"success"}`, response.Body.String())
	require.NoError(t, f.base.topup.db.First(&closed, closed.Id).Error)
	assert.Equal(t, common.TopUpStatusFailed, closed.Status)
	assert.EqualValues(t, 20, f.base.topup.credits.Load())
}

func TestPancakeWebhookEnforcesEnvironmentStoreAndBuyerBeforeCredit(t *testing.T) {
	f := newWaffoWebhookFixture(t)
	row := entity.TopUp{UserId: f.base.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: "WAFFO_PANCAKE-wallet", PaymentProvider: "waffo_pancake", Status: common.TopUpStatusPending}
	require.NoError(t, f.base.topup.store.Create(t.Context(), &row))
	buyer := billingcontract.WaffoBuyerIdentity(row.UserId)
	now := time.Now()
	response := f.pancakeRequest(t, "test", "test", "STO_owned", row.TradeNo, buyer, f.prodKey, now)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	response = f.pancakeRequest(t, "test", "prod", "STO_owned", row.TradeNo, buyer, f.testKey, now)
	assert.Equal(t, http.StatusOK, response.Code)
	response = f.pancakeRequest(t, "test", "test", "STO_foreign", row.TradeNo, buyer, f.testKey, now)
	assert.Equal(t, http.StatusOK, response.Code)
	response = f.pancakeRequest(t, "test", "test", "STO_owned", row.TradeNo, "new-api-user-999", f.testKey, now)
	assert.Equal(t, http.StatusOK, response.Code)
	response = f.pancakeRequest(t, "test", "test", "STO_owned", row.TradeNo, buyer, f.testKey, now.Add(-time.Hour))
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Zero(t, f.base.topup.credits.Load())
	require.NoError(t, f.base.topup.db.Exec("ALTER TABLE top_ups RENAME TO unavailable_pancake_topups").Error)
	response = f.pancakeRequest(t, "test", "test", "STO_owned", row.TradeNo, buyer, f.testKey, now)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Equal(t, "retry", response.Body.String())
	require.NoError(t, f.base.topup.db.Exec("ALTER TABLE unavailable_pancake_topups RENAME TO top_ups").Error)
	for range 2 {
		response = f.pancakeRequest(t, "test", "test", "STO_owned", row.TradeNo, buyer, f.testKey, now)
		assert.Equal(t, http.StatusOK, response.Code)
	}
	assert.EqualValues(t, 20, f.base.topup.credits.Load())
	assert.EqualValues(t, 1, f.base.topup.events.Load())
	response = f.pancakeRequest(t, "unknown", "test", "STO_owned", row.TradeNo, buyer, f.testKey, now)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestPancakeSubscriptionLookupFailuresRetryWithoutWalletFallback(t *testing.T) {
	f := newWaffoWebhookFixture(t)
	reference := "WAFFO_PANCAKE_SUB-owned"
	buyer := billingcontract.WaffoBuyerIdentity(f.base.topup.user.Id)
	now := time.Now()
	// Even a wallet record with this prefix cannot substitute for the missing
	// subscription order. The verified event must be retried until it exists.
	require.NoError(t, f.base.topup.store.Create(t.Context(), &entity.TopUp{UserId: f.base.topup.user.Id, Amount: 2, Money: 2.5, TradeNo: reference, PaymentProvider: "waffo_pancake", Status: common.TopUpStatusPending}))
	response := f.pancakeRequest(t, "prod", "prod", "STO_owned", reference, buyer, f.prodKey, now)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Zero(t, f.base.topup.credits.Load())
	order := subentity.SubscriptionOrder{UserId: f.base.topup.user.Id, PlanId: f.base.plan.Id, Money: 2.5, TradeNo: reference, PaymentProvider: "waffo_pancake", Status: common.TopUpStatusPending}
	require.NoError(t, f.base.subscriptions.Create(t.Context(), &order))
	// Remove the deliberately conflicting wallet record before completion,
	// which creates its own zero-credit subscription payment receipt.
	require.NoError(t, f.base.topup.db.Where("trade_no = ?", reference).Delete(&entity.TopUp{}).Error)
	for range 2 {
		response = f.pancakeRequest(t, "prod", "prod", "STO_owned", reference, buyer, f.prodKey, now)
		assert.Equal(t, http.StatusOK, response.Code)
	}
	var count int64
	require.NoError(t, f.base.topup.db.Model(&subentity.UserSubscription{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, f.base.topup.db.First(&f.base.topup.user, f.base.topup.user.Id).Error)
	assert.Equal(t, 10, f.base.topup.user.Quota)
	f.base.cfg.PancakeStoreID = ""
	response = f.pancakeRequest(t, "prod", "prod", "STO_owned", reference, buyer, f.prodKey, now)
	assert.Equal(t, http.StatusForbidden, response.Code)
}
