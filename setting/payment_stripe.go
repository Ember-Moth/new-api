package setting

import (
	"fmt"
	"strings"
)

var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripePriceId = ""
var StripeUnitPrice = 8.0
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false

var StripePaymentIntentEnabled = false
var StripePaymentIntentPublishableKey = ""
var StripePaymentIntentApiSecret = ""
var StripePaymentIntentWebhookSecret = ""
var StripePaymentIntentCurrency = "cny"
var StripePaymentIntentUnitPrice = 1.0
var StripePaymentIntentMinTopUp = 1
var StripePaymentIntentPaymentMethodTypes = ""

func NormalizeStripePaymentIntentPaymentMethodTypes(value string) (string, error) {
	rawTypes := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	seen := make(map[string]struct{}, len(rawTypes))
	normalizedTypes := make([]string, 0, len(rawTypes))

	for _, rawType := range rawTypes {
		paymentMethodType := strings.ToLower(strings.TrimSpace(rawType))
		if paymentMethodType == "" {
			continue
		}
		for _, r := range paymentMethodType {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				return "", fmt.Errorf("无效的 Stripe PaymentIntent 支付方式：%s", rawType)
			}
		}
		if _, ok := seen[paymentMethodType]; ok {
			continue
		}
		seen[paymentMethodType] = struct{}{}
		normalizedTypes = append(normalizedTypes, paymentMethodType)
	}

	return strings.Join(normalizedTypes, ","), nil
}

func StripePaymentIntentPaymentMethodTypeList() []string {
	normalizedTypes, err := NormalizeStripePaymentIntentPaymentMethodTypes(StripePaymentIntentPaymentMethodTypes)
	if err != nil || normalizedTypes == "" {
		return nil
	}
	return strings.Split(normalizedTypes, ",")
}
