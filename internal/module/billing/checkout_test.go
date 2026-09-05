package billing_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
)

func TestStripeSubscriptionCheckoutUsesRequestCredentialsAndCustomerSemantics(t *testing.T) {
	type captured struct {
		auth   string
		values url.Values
	}
	requests := make(chan captured, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		requests <- captured{r.Header.Get("Authorization"), r.PostForm}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test","object":"checkout.session","url":"https://stripe.example/pay"}`))
	}))
	defer server.Close()
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0)})
	var wg sync.WaitGroup
	for _, test := range []struct{ key, customer string }{{"sk_first", ""}, {"sk_second", "cus_existing"}} {
		wg.Go(func() {
			client := checkout.New(checkout.Options{StripeBackend: backend, Config: func() contract.GatewayConfig {
				return contract.GatewayConfig{StripeKey: test.key, StripeWebhookSecret: "webhook", ServerAddress: "https://console.example"}
			}})
			result, err := client.CreateSubscription(t.Context(), contract.CheckoutRequest{Provider: "stripe", TradeNo: test.key, ProductID: "price_month", CustomerID: test.customer, Email: "buyer@example.test"})
			assert.NoError(t, err)
			assert.Equal(t, "https://stripe.example/pay", result.PayLink)
		})
	}
	wg.Wait()
	for range 2 {
		request := <-requests
		assert.Equal(t, "Bearer "+request.values.Get("client_reference_id"), request.auth)
		assert.Equal(t, "subscription", request.values.Get("mode"))
		assert.Empty(t, request.values.Get("customer_creation"))
		assert.Equal(t, "price_month", request.values.Get("line_items[0][price]"))
		assert.Equal(t, "1", request.values.Get("line_items[0][quantity]"))
		assert.Equal(t, "https://console.example/wallet", request.values.Get("success_url"))
		if request.values.Get("customer") == "" {
			assert.Equal(t, "buyer@example.test", request.values.Get("customer_email"))
		} else {
			assert.Equal(t, "cus_existing", request.values.Get("customer"))
			assert.Empty(t, request.values.Get("customer_email"))
		}
	}
}

func TestCreemCheckoutPreservesMetadataAndRejectsFailedResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{{"valid", 200, `{"checkout_url":"https://creem.example/pay"}`, true}, {"missing URL", 200, `{}`, false}, {"bad response", 502, `{"error":"unavailable"}`, false}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
				var body map[string]any
				if err := common.DecodeJson(r.Body, &body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				assert.Equal(t, "prod_month", body["product_id"])
				assert.Equal(t, "reference", body["request_id"])
				assert.Equal(t, map[string]any{"email": "buyer@example.test"}, body["customer"])
				assert.Equal(t, map[string]any{"username": "buyer", "reference_id": "reference", "product_name": "Monthly", "quota": "0"}, body["metadata"])
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := checkout.New(checkout.Options{CreemEndpoint: server.URL, HTTPClient: server.Client(), Config: func() contract.GatewayConfig { return contract.GatewayConfig{CreemKey: "test-key"} }})
			result, err := client.Creem(t.Context(), contract.CheckoutRequest{ProductID: "prod_month", TradeNo: "reference", Email: "buyer@example.test", Username: "buyer", Title: "Monthly"})
			if test.ok {
				require.NoError(t, err)
				assert.Equal(t, "https://creem.example/pay", result.CheckoutURL)
			} else {
				require.Error(t, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = client.Creem(ctx, contract.CheckoutRequest{})
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestWaffoCheckoutRetainsBuyerIdentityAndAuthenticatedToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	var bodies []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := common.DecodeJson(r.Body, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "issue-session-token") {
			assert.Equal(t, "new-api-user-7", body["buyerIdentity"])
			_, _ = w.Write([]byte(`{"data":{"token":"JWT","expiresAt":"2026-09-06T13:00:00Z"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "create-session") {
			assert.Equal(t, "sub-trade", body["orderMerchantExternalId"])
			assert.Equal(t, "USD", body["currency"])
			_, _ = w.Write([]byte(`{"data":{"sessionId":"ses_1","checkoutUrl":"https://waffo.example/pay","expiresAt":"2026-09-06T12:45:00Z"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := checkout.New(checkout.Options{WaffoBaseURL: server.URL, HTTPClient: server.Client(), Config: func() contract.GatewayConfig {
		return contract.GatewayConfig{WaffoMerchantID: "MER_AbCdEfGhIjKlMnOpQrStUv", WaffoPrivateKey: private}
	}})
	result, err := client.CreateSubscription(t.Context(), contract.CheckoutRequest{Provider: "waffo_pancake", ProductID: "PROD_AbCdEfGhIjKlMnOpQrStUv", TradeNo: "sub-trade", UserID: 7, Email: "buyer@example.test", Price: 1.25})
	require.NoError(t, err)
	assert.Equal(t, "ses_1", result.SessionID)
	assert.Equal(t, "JWT", result.Token)
	assert.Equal(t, "https://waffo.example/pay#token=JWT", result.CheckoutURL)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
}

func TestEpayCheckoutSignsParametersAndVerifiesCallbacks(t *testing.T) {
	cfg := contract.GatewayConfig{EpayAddress: "https://pay.example", EpayID: "merchant", EpayKey: "secret", EpayMethods: []string{"alipay"}, CallbackAddress: "https://api.example", ServerAddress: "https://console.example/"}
	client := checkout.New(checkout.Options{Config: func() contract.GatewayConfig { return cfg }})
	require.NoError(t, client.ValidateSubscription("epay", "alipay"))
	require.Error(t, client.ValidateSubscription("epay", "unknown"))
	result, err := client.CreateSubscription(t.Context(), contract.CheckoutRequest{Provider: "epay", TradeNo: "reference", PaymentMethod: "alipay", Price: 1.25, Title: "Monthly"})
	require.NoError(t, err)
	assert.Equal(t, "https://pay.example/submit.php", result.EpayURL)
	assert.Equal(t, "1.25", result.EpayParams["money"])
	assert.Equal(t, "https://api.example/api/subscription/epay/notify", result.EpayParams["notify_url"])
	assert.Equal(t, "https://api.example/api/subscription/epay/return", result.EpayParams["return_url"])
	signed := epay.GenerateParams(map[string]string{"out_trade_no": "reference", "type": "alipay", "trade_status": epay.StatusTradeSuccess, "money": "1.25"}, cfg.EpayKey)
	verified, err := client.VerifyEpay(signed)
	require.NoError(t, err)
	assert.True(t, verified.Paid)
	assert.Equal(t, "reference", verified.TradeNo)
	signed["money"] = "9.99"
	_, err = client.VerifyEpay(signed)
	require.Error(t, err)
	assert.Equal(t, "https://console.example/wallet?pay=success", client.ReturnURL("/wallet?pay=success"))
}
