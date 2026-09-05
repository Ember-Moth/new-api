package webhooks

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/topups"
	"github.com/QuantumNous/new-api/internal/module/subscription/payments"
)

var ErrDisabled = errors.New("payment webhook is disabled")
var ErrSignature = errors.New("invalid payment webhook signature")
var ErrPayload = errors.New("invalid payment webhook payload")

type Config struct {
	PaymentAllowed            bool
	StripeSecret, CreemSecret string
}
type SubscriptionOrders interface {
	Complete(context.Context, string, string, string, string) error
	FinishPending(context.Context, string, string, string) error
}
type Dependencies struct {
	Config        func() Config
	TopUps        *topups.Store
	Subscriptions SubscriptionOrders
}
type Processor struct{ deps Dependencies }

func New(deps Dependencies) *Processor { return &Processor{deps: deps} }

func (p *Processor) Enabled(provider string) bool {
	cfg := p.deps.Config()
	if !cfg.PaymentAllowed {
		return false
	}
	switch provider {
	case "stripe":
		return strings.TrimSpace(cfg.StripeSecret) != ""
	case "creem":
		return strings.TrimSpace(cfg.CreemSecret) != ""
	default:
		return false
	}
}

func (p *Processor) finish(ctx context.Context, reference, provider, status string) error {
	err := p.deps.Subscriptions.FinishPending(ctx, reference, provider, status)
	if err == nil {
		return nil
	}
	if !errors.Is(err, payments.ErrOrderNotFound) {
		return err
	}
	err = p.deps.TopUps.FinishPending(ctx, reference, provider, status)
	// A stale failure/expiry must not replace a completed wallet order, and
	// acknowledging that no-op prevents endless retries of a terminal event.
	if errors.Is(err, contract.ErrTopUpStatusInvalid) {
		return nil
	}
	return err
}

func (p *Processor) complete(ctx context.Context, reference, provider, payload, customerID, email, callerIP string, walletAllowed bool) error {
	if reference == "" {
		return ErrPayload
	}
	err := p.deps.Subscriptions.Complete(ctx, reference, payload, provider, "")
	if err == nil {
		return nil
	}
	if !errors.Is(err, payments.ErrOrderNotFound) {
		return err
	}
	if !walletAllowed {
		return nil
	}
	input := contract.TopUpCompletion{TradeNo: reference, Provider: provider, CustomerEmail: email, CallerIP: callerIP}
	if provider == contract.PaymentProviderStripe {
		input.StripeCustomerID = &customerID
	}
	_, err = p.deps.TopUps.Complete(ctx, input)
	return err
}
