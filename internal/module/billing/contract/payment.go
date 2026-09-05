package contract

import "errors"

var ErrPaymentMethodMismatch = errors.New("payment method mismatch")
var ErrInsufficientBalance = errors.New("余额不足")

type SubscriptionReceipt struct {
	UserID                                  int
	Money                                   float64
	TradeNo, PaymentMethod, PaymentProvider string
	CreatedAt                               int64
}

const (
	PaymentMethodStripe       = "stripe"
	PaymentMethodCreem        = "creem"
	PaymentMethodWaffo        = "waffo"
	PaymentMethodWaffoPancake = "waffo_pancake"
	PaymentMethodBalance      = "balance"
)

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderBalance      = "balance"
)

var (
	ErrTopUpNotFound            = errors.New("topup not found")
	ErrTopUpStatusInvalid       = errors.New("topup status invalid")
	ErrInvalidTopUpQuota        = errors.New("invalid top-up quota")
	ErrTopUpQuotaLimitExceeded  = errors.New("top-up quota limit exceeded")
	ErrWalletQuotaLimitExceeded = errors.New("wallet quota limit exceeded")
)

var ErrRedeemFailed = errors.New("redeem.failed")

type AdminCompleteTopupRequest struct {
	TradeNo string `json:"trade_no"`
}

type TopUpCompletion struct {
	TradeNo, Provider, ActualMethod, CallerIP string
	StripeCustomerID                          *string
	CustomerEmail                             string
	Manual                                    bool
}

type TopUpQuery struct {
	UserID        int
	Admin         bool
	Keyword       string
	Offset, Limit int
}
type TopUpEvent struct {
	UserID                              int
	Content, CallerIP, Method, Provider string
}
type RedeemRequest struct {
	Key string `json:"key"`
}
