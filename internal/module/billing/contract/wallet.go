package contract

import "github.com/QuantumNous/new-api/constant"

type WalletConfig struct {
	StripeUnitPrice                                      float64
	StripePromotionCodes                                 bool
	PaymentAllowed                                       bool
	TermsVersion                                         string
	Gateway                                              GatewayConfig
	TokensDisplay                                        bool
	QuotaPerUnit, Price                                  float64
	Minimum, StripeMinimum, WaffoMinimum, PancakeMinimum int
	StripePriceID, CreemProducts, PancakeProductID       string
	WaffoEnabled, WaffoConfigured                        bool
	WaffoMethods                                         []constant.WaffoPayMethod
	PayMethods                                           []map[string]string
	AmountOptions                                        []int
	Discounts                                            map[int]float64
	TopupLink                                            string
}

type TopUpInfo struct {
	Online         bool                      `json:"enable_online_topup"`
	Stripe         bool                      `json:"enable_stripe_topup"`
	Creem          bool                      `json:"enable_creem_topup"`
	Waffo          bool                      `json:"enable_waffo_topup"`
	Pancake        bool                      `json:"enable_waffo_pancake_topup"`
	Redemption     bool                      `json:"enable_redemption"`
	PaymentAllowed bool                      `json:"payment_compliance_confirmed"`
	TermsVersion   string                    `json:"payment_compliance_terms_version"`
	WaffoMethods   []constant.WaffoPayMethod `json:"waffo_pay_methods"`
	CreemProducts  string                    `json:"creem_products"`
	PayMethods     []map[string]string       `json:"pay_methods"`
	Minimum        int                       `json:"min_topup"`
	StripeMinimum  int                       `json:"stripe_min_topup"`
	WaffoMinimum   int                       `json:"waffo_min_topup"`
	PancakeMinimum int                       `json:"waffo_pancake_min_topup"`
	AmountOptions  []int                     `json:"amount_options"`
	Discounts      map[int]float64           `json:"discount"`
	TopupLink      string                    `json:"topup_link"`
}

type WalletPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}
type WalletQuote struct {
	InputAmount, StoredAmount int64
	CreditedQuota             int
	Money                     float64
}

type StripeWalletRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}
type StripeWalletQuote struct {
	Money, CreditBase float64
	CreditedQuota     int
	Quantity          int64
}
type CreemWalletRequest struct {
	ProductId     string `json:"product_id"`
	PaymentMethod string `json:"payment_method"`
}
type CreemProduct struct {
	ProductId string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
}
