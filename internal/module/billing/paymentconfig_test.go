package billing_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/paymentconfig"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/system"
	systementity "github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPancakeConfigurationSavePreservesSecretsAndRollsBackAsOneUnit(t *testing.T) {
	f := newTopupFixture(t, 10)
	oldMap := common.OptionMap
	old := paymentconfig.Config{MerchantID: setting.WaffoPancakeMerchantID, PrivateKey: setting.WaffoPancakePrivateKey, ReturnURL: setting.WaffoPancakeReturnURL, StoreID: setting.WaffoPancakeStoreID, ProductID: setting.WaffoPancakeProductID}
	t.Cleanup(func() {
		common.OptionMap = oldMap
		setting.WaffoPancakeMerchantID = old.MerchantID
		setting.WaffoPancakePrivateKey = old.PrivateKey
		setting.WaffoPancakeReturnURL = old.ReturnURL
		setting.WaffoPancakeStoreID = old.StoreID
		setting.WaffoPancakeProductID = old.ProductID
	})
	common.OptionMap = map[string]string{}
	manager := system.NewOptions(system.OptionDependencies{DB: f.db.Session(&gorm.Session{CreateBatchSize: 1, SkipDefaultTransaction: true})})
	service := paymentconfig.New(paymentconfig.Dependencies{SaveOptions: manager.UpdateOptionsBulk, Config: func() paymentconfig.Config {
		return paymentconfig.Config{MerchantID: setting.WaffoPancakeMerchantID, PrivateKey: setting.WaffoPancakePrivateKey, ReturnURL: setting.WaffoPancakeReturnURL, StoreID: setting.WaffoPancakeStoreID, ProductID: setting.WaffoPancakeProductID}
	}})
	require.NoError(t, service.Save(t.Context(), "merchant", "private-original", "https://console.example/", "store", "product"))
	require.NoError(t, service.Save(t.Context(), " merchant-updated ", "  ", "https://console.example/done", " store-updated ", " product-updated "))
	cfg := service.Configuration()
	assert.Equal(t, "private-original", cfg.PrivateKey)
	assert.Equal(t, "merchant-updated", cfg.MerchantID)
	merchant, private := service.ResolveCredentials("", "")
	assert.Equal(t, "merchant-updated", merchant)
	assert.Equal(t, "private-original", private)
	merchant, private = service.ResolveCredentials("typed-merchant", "")
	assert.Equal(t, "typed-merchant", merchant)
	assert.Empty(t, private, "do not combine a newly typed merchant with another merchant's saved key")
	require.NoError(t, f.db.Exec(`CREATE FUNCTION reject_payment_option() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key = 'WaffoPancakeProductID' THEN RAISE EXCEPTION 'injected payment option failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_payment_option BEFORE INSERT OR UPDATE ON options FOR EACH ROW EXECUTE FUNCTION reject_payment_option();`).Error)
	require.Error(t, service.Save(t.Context(), "not-published", "new-private", "new-return", "new-store", "new-product"))
	assert.Equal(t, cfg, service.Configuration())
	var rows []systementity.Option
	require.NoError(t, f.db.Find(&rows).Error)
	values := map[string]string{}
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	assert.Equal(t, "merchant-updated", values["WaffoPancakeMerchantID"])
	assert.Equal(t, "private-original", values["WaffoPancakePrivateKey"])
	assert.Equal(t, "product-updated", values["WaffoPancakeProductID"])
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, service.Save(ctx, "cancelled", "key", "url", "store", "product"), context.Canceled)
}

func TestPancakeManagementKeepsPartialCreationAndFiltersInactiveProducts(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	const storeID = "STO_AbCdEfGhIjKlMnOpQrStUv"
	const productID = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	var failPublish atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := common.DecodeJson(r.Body, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "create-store"):
			assert.Equal(t, "new-api-store", body["name"])
			_, _ = w.Write([]byte(`{"data":{"store":{"id":"` + storeID + `"}}}`))
		case strings.HasSuffix(r.URL.Path, "create-product"):
			assert.Equal(t, storeID, body["storeId"])
			assert.Equal(t, map[string]any{"USD": map[string]any{"amount": "1.00", "taxCategory": "saas"}}, body["prices"])
			_, _ = w.Write([]byte(`{"data":{"product":{"id":"` + productID + `"}}}`))
		case strings.HasSuffix(r.URL.Path, "publish-product"):
			if failPublish.Load() {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"errors":[{"message":"publish rejected"}]}`))
				return
			}
			assert.Equal(t, productID, body["id"])
			_, _ = w.Write([]byte(`{"data":{"product":{"id":"` + productID + `"}}}`))
		default:
			assert.Contains(t, body["query"], "stores(limit: 100)")
			_, _ = w.Write([]byte(`{"data":{"stores":[{"id":"` + storeID + `","name":"Store","status":"active","onetimeProducts":[{"id":"active","name":"Active","status":"active"},{"id":"hidden","name":"Hidden","status":"inactive"}]}]}}`))
		}
	}))
	defer server.Close()
	saved := false
	cfg := paymentconfig.Config{MerchantID: "MER_AbCdEfGhIjKlMnOpQrStUv", PrivateKey: private, StoreID: storeID}
	service := paymentconfig.New(paymentconfig.Dependencies{Config: func() paymentconfig.Config { return cfg }, SaveOptions: func(context.Context, map[string]string) error { saved = true; return nil }, BaseURL: server.URL, HTTPClient: server.Client()})
	handler := billinghttp.New(billing.New(billing.Dependencies{PaymentConfig: service}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.POST("/pair", handler.CreateWaffoPancakePair)
	router.GET("/products", handler.ListWaffoPancakeSubscriptionProductOptions)
	for _, fail := range []bool{false, true} {
		failPublish.Store(fail)
		result := redemptionRequest(t, router, http.MethodPost, "/pair", map[string]any{})
		var data map[string]any
		require.NoError(t, common.Unmarshal(result.Data, &data))
		assert.Equal(t, storeID, data["store_id"])
		if fail {
			assert.Equal(t, "error", result.Message)
			assert.Equal(t, true, data["orphan_store"])
		} else {
			assert.Equal(t, "success", result.Message)
			assert.Equal(t, productID, data["product_id"])
		}
	}
	result := redemptionRequest(t, router, http.MethodGet, "/products", nil)
	assert.Equal(t, "success", result.Message)
	var data struct {
		Products []paymentconfig.CatalogProduct `json:"products"`
	}
	require.NoError(t, common.Unmarshal(result.Data, &data))
	require.Len(t, data.Products, 1)
	assert.Equal(t, "active", data.Products[0].ID)
	assert.False(t, saved, "credential probes and upstream creation do not persist settings")
}

func TestPaymentComplianceConfirmationIsAtomicAndRequiresSession(t *testing.T) {
	f := newTopupFixture(t, 10)
	oldMap := common.OptionMap
	oldPayment := *operation_setting.GetPaymentSetting()
	t.Cleanup(func() { common.OptionMap = oldMap; *operation_setting.GetPaymentSetting() = oldPayment })
	common.OptionMap = map[string]string{}
	*operation_setting.GetPaymentSetting() = operation_setting.PaymentSetting{}
	manager := system.NewOptions(system.OptionDependencies{DB: f.db.Session(&gorm.Session{CreateBatchSize: 1, SkipDefaultTransaction: true})})
	config := paymentconfig.New(paymentconfig.Dependencies{TermsVersion: "v1", SaveOptions: manager.UpdateOptionsBulk})
	handler := billinghttp.New(billing.New(billing.Dependencies{PaymentConfig: config}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 7); c.Set("use_access_token", c.Query("token") == "1") })
	router.POST("/confirm", handler.ConfirmPaymentCompliance)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/confirm?token=1", strings.NewReader(`{"confirmed":true}`))
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusForbidden, response.Code)
	require.NoError(t, f.db.Exec(`CREATE FUNCTION fail_compliance_option() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key = 'payment_setting.compliance_confirmed_ip' THEN RAISE EXCEPTION 'injected confirmation failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER fail_compliance_option BEFORE INSERT OR UPDATE ON options FOR EACH ROW EXECUTE FUNCTION fail_compliance_option();`).Error)
	failed := redemptionRequest(t, router, http.MethodPost, "/confirm", map[string]any{"confirmed": true})
	assert.False(t, failed.Success)
	assert.False(t, operation_setting.GetPaymentSetting().ComplianceConfirmed)
	var count int64
	require.NoError(t, f.db.Model(&systementity.Option{}).Where(`"key" LIKE ?`, "payment_setting.compliance_%").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, f.db.Exec("DROP FUNCTION fail_compliance_option() CASCADE").Error)
	success := redemptionRequest(t, router, http.MethodPost, "/confirm", map[string]any{"confirmed": true})
	assert.True(t, success.Success)
	var confirmation paymentconfig.ComplianceConfirmation
	require.NoError(t, common.Unmarshal(success.Data, &confirmation))
	assert.True(t, confirmation.Confirmed)
	assert.Equal(t, 7, confirmation.ConfirmedBy)
	assert.Equal(t, "v1", confirmation.TermsVersion)
	assert.True(t, operation_setting.IsPaymentComplianceConfirmed())
	require.NoError(t, f.db.Model(&systementity.Option{}).Where(`"key" LIKE ?`, "payment_setting.compliance_%").Count(&count).Error)
	assert.EqualValues(t, 5, count)
}
