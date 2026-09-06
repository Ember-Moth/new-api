package webhooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"
)

func (p *Processor) Stripe(ctx context.Context, payload []byte, signature, callerIP string) error {
	cfg := p.deps.Config()
	if !cfg.PaymentAllowed || strings.TrimSpace(cfg.StripeSecret) == "" {
		return ErrDisabled
	}
	event, err := webhook.ConstructEventWithOptions(payload, signature, cfg.StripeSecret, webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignature, err)
	}
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted, stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded, stripe.EventTypeCheckoutSessionAsyncPaymentFailed, stripe.EventTypeCheckoutSessionExpired:
	default:
		return nil
	}
	if event.Data == nil {
		return ErrPayload
	}
	var session stripe.CheckoutSession
	if err := common.Unmarshal(event.Data.Raw, &session); err != nil {
		return fmt.Errorf("%w: %v", ErrPayload, err)
	}
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		if session.Status != stripe.CheckoutSessionStatusComplete || session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
			return nil
		}
	case stripe.EventTypeCheckoutSessionExpired:
		if session.Status != stripe.CheckoutSessionStatusExpired {
			return nil
		}
	}
	reference := session.ClientReferenceID
	if reference == "" {
		return ErrPayload
	}
	switch event.Type {
	case stripe.EventTypeCheckoutSessionExpired:
		return p.finish(ctx, reference, contract.PaymentProviderStripe, common.TopUpStatusExpired)
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		return p.finish(ctx, reference, contract.PaymentProviderStripe, common.TopUpStatusFailed)
	}
	customer := ""
	if session.Customer != nil {
		customer = session.Customer.ID
	}
	data, err := common.Marshal(map[string]any{"customer": customer, "amount_total": fmt.Sprint(session.AmountTotal), "currency": strings.ToUpper(string(session.Currency)), "event_type": string(event.Type)})
	if err != nil {
		return err
	}
	return p.complete(ctx, reference, contract.PaymentProviderStripe, string(data), customer, "", callerIP, true)
}
