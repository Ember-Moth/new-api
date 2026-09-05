package checkout

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
)

func (c *Client) stripeSubscription(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	params := &stripe.CheckoutSessionParams{ClientReferenceID: stripe.String(request.TradeNo), SuccessURL: stripe.String(c.ReturnURL("/wallet")), CancelURL: stripe.String(c.ReturnURL("/wallet")), LineItems: []*stripe.CheckoutSessionLineItemParams{{Price: stripe.String(request.ProductID), Quantity: stripe.Int64(1)}}, Mode: stripe.String(string(stripe.CheckoutSessionModeSubscription))}
	params.Context = ctx
	if request.CustomerID != "" {
		params.Customer = stripe.String(request.CustomerID)
	} else if request.Email != "" {
		params.CustomerEmail = stripe.String(request.Email)
	}
	// Subscription-mode Checkout creates customers itself; customer_creation is
	// only accepted in payment/setup modes (stripe-go's pinned API schema).
	client := session.Client{B: c.options.StripeBackend, Key: c.options.Config().StripeKey}
	result, err := client.New(params)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if result.URL == "" {
		return contract.CheckoutSession{}, errors.New("Stripe returned empty checkout URL")
	}
	return contract.CheckoutSession{PayLink: result.URL}, nil
}
