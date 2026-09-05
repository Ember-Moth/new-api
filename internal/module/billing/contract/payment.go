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
