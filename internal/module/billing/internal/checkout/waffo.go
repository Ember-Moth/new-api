package checkout

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/shopspring/decimal"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

func (c *Client) waffoSubscription(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	expires := 45 * 60
	result, err := c.Waffo(ctx, &contract.WaffoCheckoutParams{ProductID: request.ProductID, BuyerIdentity: contract.WaffoBuyerIdentity(request.UserID), BuyerEmail: request.Email, PriceSnapshot: &contract.WaffoPriceSnapshot{Amount: decimal.NewFromFloat(request.Price).StringFixed(2), TaxCategory: "saas"}, ExpiresInSeconds: &expires, OrderMerchantExternalID: request.TradeNo})
	return contract.CheckoutSession{SessionID: result.SessionID, CheckoutURL: result.CheckoutURL, ExpiresAt: result.ExpiresAt, Token: result.Token, TokenExpiresAt: result.TokenExpiresAt}, err
}

func (c *Client) Waffo(ctx context.Context, request *contract.WaffoCheckoutParams) (contract.WaffoCheckoutSession, error) {
	if request == nil {
		return contract.WaffoCheckoutSession{}, errors.New("missing checkout params")
	}
	if strings.TrimSpace(request.BuyerIdentity) == "" {
		return contract.WaffoCheckoutSession{}, errors.New("missing buyer identity")
	}
	if strings.TrimSpace(request.OrderMerchantExternalID) == "" {
		return contract.WaffoCheckoutSession{}, errors.New("missing order merchant external id")
	}
	cfg := c.options.Config()
	client, err := pancake.New(pancake.Config{MerchantID: cfg.WaffoMerchantID, PrivateKey: cfg.WaffoPrivateKey, BaseURL: c.options.WaffoBaseURL, HTTPClient: c.options.HTTPClient})
	if err != nil {
		return contract.WaffoCheckoutSession{}, err
	}
	params := pancake.AuthenticatedCheckoutParams{BuyerIdentity: request.BuyerIdentity, CreateCheckoutSessionParams: pancake.CreateCheckoutSessionParams{ProductID: request.ProductID, Currency: "USD", ExpiresInSeconds: request.ExpiresInSeconds, OrderMerchantExternalID: &request.OrderMerchantExternalID}}
	if strings.TrimSpace(request.BuyerEmail) != "" {
		params.BuyerEmail = &request.BuyerEmail
	}
	if request.PriceSnapshot != nil {
		params.PriceSnapshot = &pancake.PriceInfo{Amount: request.PriceSnapshot.Amount, TaxCategory: pancake.TaxCategory(request.PriceSnapshot.TaxCategory)}
	}
	result, err := client.Checkout.Authenticated.Create(ctx, params)
	if err != nil {
		return contract.WaffoCheckoutSession{}, err
	}
	if result == nil || strings.TrimSpace(result.CheckoutURL) == "" || strings.TrimSpace(result.SessionID) == "" {
		return contract.WaffoCheckoutSession{}, errors.New("Waffo Pancake returned empty checkout session")
	}
	return contract.WaffoCheckoutSession{SessionID: result.SessionID, CheckoutURL: result.CheckoutURL, ExpiresAt: result.ExpiresAt, Token: result.Token, TokenExpiresAt: result.TokenExpiresAt}, nil
}
