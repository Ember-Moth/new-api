package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// PaymentCheckoutClient adapts existing deployment settings for module clients.
func PaymentCheckoutClient() *checkout.Client {
	return checkout.New(checkout.Options{Config: PaymentGatewayConfig})
}
func PaymentGatewayConfig() contract.GatewayConfig {
	methods := make([]string, 0, len(operation_setting.PayMethods))
	for _, method := range operation_setting.PayMethods {
		methods = append(methods, method["type"])
	}
	return contract.GatewayConfig{StripeKey: setting.StripeApiSecret, StripeWebhookSecret: setting.StripeWebhookSecret, CreemKey: setting.CreemApiKey, CreemWebhookSecret: setting.CreemWebhookSecret, CreemTestMode: setting.CreemTestMode, WaffoMerchantID: setting.WaffoPancakeMerchantID, WaffoPrivateKey: setting.WaffoPancakePrivateKey, EpayAddress: operation_setting.PayAddress, EpayID: operation_setting.EpayId, EpayKey: operation_setting.EpayKey, EpayMethods: methods, CallbackAddress: GetCallbackAddress(), ServerAddress: system_setting.ServerAddress}
}

// PaymentWebhookConfig is independent of wallet product/catalog readiness:
// existing orders and subscription-only deployments still need callbacks.
func PaymentWebhookConfig() webhooks.Config {
	private, public := setting.WaffoPrivateKey, setting.WaffoPublicCert
	if setting.WaffoSandbox {
		private, public = setting.WaffoSandboxPrivateKey, setting.WaffoSandboxPublicCert
	}
	return webhooks.Config{EpayConfigured: strings.TrimSpace(operation_setting.PayAddress) != "" && strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "", PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed(), StripeSecret: setting.StripeWebhookSecret, CreemSecret: setting.CreemWebhookSecret,
		WaffoEnabled: setting.WaffoEnabled, WaffoPrivateKey: private, WaffoPublicKey: public,
		PancakeEnabled: strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" && strings.TrimSpace(setting.WaffoPancakePrivateKey) != "", PancakeStoreID: setting.WaffoPancakeStoreID}
}

func WalletConfiguration() contract.WalletConfig {
	cfg := PaymentGatewayConfig()
	waffoConfigured := strings.TrimSpace(setting.WaffoApiKey) != "" && strings.TrimSpace(setting.WaffoPrivateKey) != "" && strings.TrimSpace(setting.WaffoPublicCert) != ""
	if setting.WaffoSandbox {
		waffoConfigured = strings.TrimSpace(setting.WaffoSandboxApiKey) != "" && strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" && strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
	}
	return contract.WalletConfig{PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed(), TermsVersion: operation_setting.CurrentComplianceTermsVersion, Gateway: cfg, TokensDisplay: operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens, QuotaPerUnit: common.QuotaPerUnit, Price: operation_setting.Price, Minimum: operation_setting.MinTopUp, StripeMinimum: setting.StripeMinTopUp, WaffoMinimum: setting.WaffoMinTopUp, PancakeMinimum: setting.WaffoPancakeMinTopUp, StripePriceID: setting.StripePriceId, CreemProducts: setting.CreemProducts, PancakeProductID: setting.WaffoPancakeProductID, WaffoEnabled: setting.WaffoEnabled, WaffoConfigured: waffoConfigured, WaffoMethods: setting.GetWaffoPayMethods(), PayMethods: operation_setting.PayMethods, AmountOptions: operation_setting.GetPaymentSetting().AmountOptions, Discounts: operation_setting.GetPaymentSetting().AmountDiscount, TopupLink: common.TopUpLink}
}
