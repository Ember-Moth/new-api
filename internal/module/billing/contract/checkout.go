package contract

import "fmt"

type GatewayConfig struct {
	StripeKey, StripeWebhookSecret   string
	CreemKey, CreemWebhookSecret     string
	CreemTestMode                    bool
	WaffoMerchantID, WaffoPrivateKey string
	EpayAddress, EpayID, EpayKey     string
	EpayMethods                      []string
	ServerAddress, CallbackAddress   string
}

type CheckoutRequest struct {
	InputAmount                                        int64
	Provider, ProductID, TradeNo, PaymentMethod, Title string
	Price                                              float64
	UserID                                             int
	Email, Username, CustomerID                        string
	Quota                                              int64
}

type CheckoutSession struct {
	PayLink, CheckoutURL, SessionID, ExpiresAt, OrderID, Token, TokenExpiresAt string
	EpayURL                                                                    string
	EpayParams                                                                 map[string]string
}

type VerifiedPayment struct {
	TradeNo, PaymentMethod, Payload string
	Paid                            bool
}

type WaffoPriceSnapshot struct{ Amount, TaxCategory string }
type WaffoCheckoutParams struct {
	ProductID, BuyerIdentity string
	PriceSnapshot            *WaffoPriceSnapshot
	BuyerEmail               string
	ExpiresInSeconds         *int
	OrderMerchantExternalID  string
}

func WaffoBuyerIdentity(userID int) string { return fmt.Sprintf("new-api-user-%d", userID) }

type WaffoCheckoutSession struct{ SessionID, CheckoutURL, ExpiresAt, OrderID, Token, TokenExpiresAt string }
