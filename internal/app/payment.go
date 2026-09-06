package app

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func paymentGatewayConfig() contract.GatewayConfig {
	methods := make([]string, 0, len(operation_setting.PayMethods))
	for _, method := range operation_setting.PayMethods {
		methods = append(methods, method["type"])
	}
	private, public, key := setting.WaffoPrivateKey, setting.WaffoPublicCert, setting.WaffoApiKey
	if setting.WaffoSandbox {
		private, public, key = setting.WaffoSandboxPrivateKey, setting.WaffoSandboxPublicCert, setting.WaffoSandboxApiKey
	}
	return contract.GatewayConfig{DirectWaffo: contract.WaffoGatewayConfig{Sandbox: setting.WaffoSandbox, APIKey: key, PrivateKey: private, PublicKey: public, MerchantID: setting.WaffoMerchantId, Currency: setting.WaffoCurrency, NotifyURL: setting.WaffoNotifyUrl, ReturnURL: setting.WaffoReturnUrl, AppName: common.SystemName}, StripeKey: setting.StripeApiSecret, StripeWebhookSecret: setting.StripeWebhookSecret, CreemKey: setting.CreemApiKey, CreemWebhookSecret: setting.CreemWebhookSecret, CreemTestMode: setting.CreemTestMode, WaffoMerchantID: setting.WaffoPancakeMerchantID, WaffoPrivateKey: setting.WaffoPancakePrivateKey, EpayAddress: operation_setting.PayAddress, EpayID: operation_setting.EpayId, EpayKey: operation_setting.EpayKey, EpayMethods: methods, CallbackAddress: paymentCallbackAddress(), ServerAddress: system_setting.ServerAddress}
}

// paymentWebhookConfig is independent of wallet product/catalog readiness:
// existing orders and subscription-only deployments still need callbacks.
func paymentWebhookConfig() webhooks.Config {
	private, public := setting.WaffoPrivateKey, setting.WaffoPublicCert
	if setting.WaffoSandbox {
		private, public = setting.WaffoSandboxPrivateKey, setting.WaffoSandboxPublicCert
	}
	return webhooks.Config{EpayConfigured: strings.TrimSpace(operation_setting.PayAddress) != "" && strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "", PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed(), StripeSecret: setting.StripeWebhookSecret, CreemSecret: setting.CreemWebhookSecret,
		WaffoEnabled: setting.WaffoEnabled, WaffoPrivateKey: private, WaffoPublicKey: public,
		PancakeEnabled: strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" && strings.TrimSpace(setting.WaffoPancakePrivateKey) != "", PancakeStoreID: setting.WaffoPancakeStoreID}
}

func walletConfiguration() contract.WalletConfig {
	cfg := paymentGatewayConfig()
	waffoConfigured := strings.TrimSpace(setting.WaffoApiKey) != "" && strings.TrimSpace(setting.WaffoPrivateKey) != "" && strings.TrimSpace(setting.WaffoPublicCert) != ""
	if setting.WaffoSandbox {
		waffoConfigured = strings.TrimSpace(setting.WaffoSandboxApiKey) != "" && strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" && strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
	}
	return contract.WalletConfig{WaffoUnitPrice: setting.WaffoUnitPrice, PancakeUnitPrice: setting.WaffoPancakeUnitPrice, StripeUnitPrice: setting.StripeUnitPrice, StripePromotionCodes: setting.StripePromotionCodesEnabled, PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed(), TermsVersion: operation_setting.CurrentComplianceTermsVersion, Gateway: cfg, TokensDisplay: operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens, QuotaPerUnit: common.QuotaPerUnit, Price: operation_setting.Price, Minimum: operation_setting.MinTopUp, StripeMinimum: setting.StripeMinTopUp, WaffoMinimum: setting.WaffoMinTopUp, PancakeMinimum: setting.WaffoPancakeMinTopUp, StripePriceID: setting.StripePriceId, CreemProducts: setting.CreemProducts, PancakeProductID: setting.WaffoPancakeProductID, WaffoEnabled: setting.WaffoEnabled, WaffoConfigured: waffoConfigured, WaffoMethods: setting.GetWaffoPayMethods(), PayMethods: operation_setting.PayMethods, AmountOptions: operation_setting.GetPaymentSetting().AmountOptions, Discounts: operation_setting.GetPaymentSetting().AmountDiscount, TopupLink: common.TopUpLink}
}

func paymentCallbackAddress() string {
	if operation_setting.CustomCallbackAddress == "" {
		return system_setting.ServerAddress
	}
	return operation_setting.CustomCallbackAddress
}

func rewardConfiguration() contract.RewardConfig {
	setting := operation_setting.GetCheckinSetting()
	return contract.RewardConfig{CheckinEnabled: setting.Enabled, MinQuota: setting.MinQuota, MaxQuota: setting.MaxQuota, QuotaPerUnit: common.QuotaPerUnit}
}
func statementConfiguration() contract.StatementConfig {
	return contract.StatementConfig{TokenStats: common.DisplayTokenStatEnabled, DisplayType: operation_setting.GetQuotaDisplayType(), QuotaPerUnit: common.QuotaPerUnit, ExchangeRate: operation_setting.USDExchangeRate}
}
