package service

import (
	"github.com/QuantumNous/new-api/internal/module/billing/checkout"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// PaymentCheckoutClient adapts existing deployment settings for module clients.
func PaymentCheckoutClient() *checkout.Client {
	return checkout.New(checkout.Options{Config: func() contract.GatewayConfig {
		methods := make([]string, 0, len(operation_setting.PayMethods))
		for _, method := range operation_setting.PayMethods {
			methods = append(methods, method["type"])
		}
		return contract.GatewayConfig{StripeKey: setting.StripeApiSecret, StripeWebhookSecret: setting.StripeWebhookSecret, CreemKey: setting.CreemApiKey, CreemWebhookSecret: setting.CreemWebhookSecret, CreemTestMode: setting.CreemTestMode, WaffoMerchantID: setting.WaffoPancakeMerchantID, WaffoPrivateKey: setting.WaffoPancakePrivateKey, EpayAddress: operation_setting.PayAddress, EpayID: operation_setting.EpayId, EpayKey: operation_setting.EpayKey, EpayMethods: methods, CallbackAddress: GetCallbackAddress(), ServerAddress: system_setting.ServerAddress}
	}})
}

// PaymentWebhookConfig is independent of wallet product/catalog readiness:
// existing orders and subscription-only deployments still need callbacks.
func PaymentWebhookConfig() webhooks.Config {
	return webhooks.Config{PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed(), StripeSecret: setting.StripeWebhookSecret, CreemSecret: setting.CreemWebhookSecret}
}
