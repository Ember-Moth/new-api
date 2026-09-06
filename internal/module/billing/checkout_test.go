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
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	waffonet "github.com/waffo-com/waffo-go/net"
	waffoutils "github.com/waffo-com/waffo-go/utils"
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

func TestStripeWalletCheckoutPreservesPaymentModeAndRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		assert.Equal(t, "Bearer sk_wallet", r.Header.Get("Authorization"))
		assert.Equal(t, "payment", r.PostForm.Get("mode"))
		assert.Equal(t, "always", r.PostForm.Get("customer_creation"))
		assert.Equal(t, "price_wallet", r.PostForm.Get("line_items[0][price]"))
		assert.Equal(t, "3", r.PostForm.Get("line_items[0][quantity]"))
		assert.Equal(t, "https://trusted.example/done", r.PostForm.Get("success_url"))
		assert.Equal(t, "https://console.example/wallet", r.PostForm.Get("cancel_url"))
		assert.Equal(t, "true", r.PostForm.Get("allow_promotion_codes"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_wallet","object":"checkout.session","url":"https://stripe.example/wallet"}`))
	}))
	defer server.Close()
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0)})
	client := checkout.New(checkout.Options{StripeBackend: backend, Config: func() contract.GatewayConfig {
		return contract.GatewayConfig{StripeKey: "sk_wallet", ServerAddress: "https://console.example"}
	}})
	request := contract.CheckoutRequest{InputAmount: 3, ProductID: "price_wallet", Email: "buyer@example.test", SuccessURL: "https://trusted.example/done", AllowPromotionCodes: true}
	result, err := client.StripeWallet(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, "https://stripe.example/wallet", result.PayLink)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.StripeWallet(ctx, request)
	require.ErrorIs(t, err, context.Canceled)
}

type waffoTransportFixture func(context.Context, *waffonet.HttpRequest) (*waffonet.HttpResponse, error)

func (f waffoTransportFixture) Send(ctx context.Context, req *waffonet.HttpRequest) (*waffonet.HttpResponse, error) {
	return f(ctx, req)
}

func TestWaffoWalletSDKSignsOrdersAndPreservesCurrencyFormatting(t *testing.T) {
	merchant, err := waffoutils.GenerateKeyPair()
	require.NoError(t, err)
	upstream, err := waffoutils.GenerateKeyPair()
	require.NoError(t, err)
	for _, tc := range []struct {
		currency, amount, action, url string
		money                         float64
	}{
		{"USD", "29.90", `{"webUrl":"https://waffo.example/pay"}`, "https://waffo.example/pay", 29.9},
		{"JPY", "30", "https://waffo.example/raw", "https://waffo.example/raw", 29.9},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			cfg := contract.GatewayConfig{ServerAddress: "https://console.example/", CallbackAddress: "https://api.example/", DirectWaffo: contract.WaffoGatewayConfig{Sandbox: true, APIKey: "api-test", PrivateKey: merchant.PrivateKey, PublicKey: upstream.PublicKey, MerchantID: "merchant", Currency: tc.currency}}
			transport := waffoTransportFixture(func(ctx context.Context, req *waffonet.HttpRequest) (*waffonet.HttpResponse, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				assert.Equal(t, "api-test", req.Headers["X-API-KEY"])
				assert.True(t, waffoutils.Verify(string(req.Body), req.Headers["X-SIGNATURE"], merchant.PublicKey))
				var body map[string]any
				require.NoError(t, common.Unmarshal(req.Body, &body))
				assert.Equal(t, "wallet-ref", body["paymentRequestId"])
				assert.Equal(t, "wallet-ref", body["merchantOrderId"])
				assert.Equal(t, tc.amount, body["orderAmount"])
				assert.Equal(t, tc.currency, body["orderCurrency"])
				assert.Equal(t, "https://api.example/api/waffo/webhook", body["notifyUrl"])
				assert.Equal(t, "https://console.example/wallet?show_history=true", body["successRedirectUrl"])
				assert.Equal(t, map[string]any{"goodsName": "Recharge 12 credits", "appName": "New API"}, body["goodsInfo"])
				payload, err := common.Marshal(map[string]any{"code": "0", "data": map[string]any{"orderAction": tc.action}})
				require.NoError(t, err)
				sig, err := waffoutils.Sign(string(payload), upstream.PrivateKey)
				require.NoError(t, err)
				return waffonet.NewHttpResponse(200, map[string]string{"X-SIGNATURE": sig}, payload), nil
			})
			client := checkout.New(checkout.Options{Config: func() contract.GatewayConfig { return cfg }, WaffoTransport: transport})
			input := contract.CheckoutRequest{TradeNo: "wallet-ref", UserID: 7, InputAmount: 12, Price: tc.money, PayMethodType: "CARD", PayMethodName: "VISA"}
			result, err := client.WaffoWallet(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, tc.url, result.PaymentURL)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = client.WaffoWallet(ctx, input)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
	for _, body := range []string{`{"code":"declined"}`, `{"code":"0"}`, `{"code":"0","data":{}}`} {
		client := checkout.New(checkout.Options{Config: func() contract.GatewayConfig {
			return contract.GatewayConfig{DirectWaffo: contract.WaffoGatewayConfig{APIKey: "key", PrivateKey: merchant.PrivateKey, PublicKey: upstream.PublicKey}}
		}, WaffoTransport: waffoTransportFixture(func(context.Context, *waffonet.HttpRequest) (*waffonet.HttpResponse, error) {
			return waffonet.NewHttpResponse(200, nil, []byte(body)), nil
		})})
		_, err := client.WaffoWallet(t.Context(), contract.CheckoutRequest{Price: 1})
		require.Error(t, err)
	}
}

func TestPancakeWalletSDKUsesDisplayPriceAndBuyerIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	for _, tc := range []struct {
		money  float64
		amount string
	}{{29, "29.00"}, {29.9, "29.90"}, {29.999, "30.00"}} {
		t.Run(tc.amount, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := common.DecodeJson(r.Body, &body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "issue-session-token") {
					assert.Equal(t, "new-api-user-7", body["buyerIdentity"])
					_, _ = w.Write([]byte(`{"data":{"token":"JWT","expiresAt":"expiry"}}`))
					return
				}
				assert.Equal(t, "wallet-trade", body["orderMerchantExternalId"])
				assert.Equal(t, map[string]any{"amount": tc.amount, "taxCategory": "saas"}, body["priceSnapshot"])
				assert.Equal(t, float64(2700), body["expiresInSeconds"])
				_, _ = w.Write([]byte(`{"data":{"sessionId":"ses_wallet","checkoutUrl":"https://waffo.example/wallet","expiresAt":"expiry"}}`))
			}))
			defer server.Close()
			client := checkout.New(checkout.Options{WaffoBaseURL: server.URL, HTTPClient: server.Client(), Config: func() contract.GatewayConfig {
				return contract.GatewayConfig{WaffoMerchantID: "MER_AbCdEfGhIjKlMnOpQrStUv", WaffoPrivateKey: private}
			}})
			result, err := client.PancakeWallet(t.Context(), contract.CheckoutRequest{ProductID: "PROD_AbCdEfGhIjKlMnOpQrStUv", TradeNo: "wallet-trade", UserID: 7, Price: tc.money})
			require.NoError(t, err)
			assert.Equal(t, "ses_wallet", result.SessionID)
			assert.Equal(t, "JWT", result.Token)
		})
	}
}
