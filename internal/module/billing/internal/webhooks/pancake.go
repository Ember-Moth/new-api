package webhooks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

var ErrEnvironment = errors.New("unknown webhook environment")
var ErrOrderIdentity = errors.New("webhook order identity mismatch")

func (p *Processor) Pancake(ctx context.Context, payload []byte, signature, expectedEnv, callerIP string) error {
	cfg := p.deps.Config()
	if !cfg.PaymentAllowed || !cfg.PancakeEnabled || strings.TrimSpace(cfg.PancakeStoreID) == "" {
		return ErrDisabled
	}
	if expectedEnv != "test" && expectedEnv != "prod" {
		return ErrEnvironment
	}
	// Verification is bound to the route's environment, then the signed mode
	// and store are checked again before any local order is read or mutated.
	event, err := pancake.VerifyWebhookTyped[pancake.WebhookEventData](string(payload), signature, &pancake.VerifyWebhookOptions{Environment: pancake.Environment(expectedEnv), PublicKeys: p.deps.PancakePublicKeys})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignature, err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(event.Mode)), expectedEnv) || event.StoreID != strings.TrimSpace(cfg.PancakeStoreID) {
		return ErrOrderIdentity
	}
	if event.EventType != "order.completed" {
		return nil
	}
	reference := ""
	if event.Data.OrderMerchantExternalID != nil {
		reference = strings.TrimSpace(*event.Data.OrderMerchantExternalID)
	}
	if reference == "" {
		return ErrOrderIdentity
	}
	buyer := ""
	if event.Data.MerchantProvidedBuyerIdentity != nil {
		buyer = strings.TrimSpace(*event.Data.MerchantProvidedBuyerIdentity)
	}
	if strings.HasPrefix(reference, "WAFFO_PANCAKE_SUB-") {
		order, err := p.deps.Subscriptions.Get(ctx, reference)
		if err != nil {
			return err
		}
		if order.PaymentProvider != contract.PaymentProviderWaffoPancake {
			return contract.ErrPaymentMethodMismatch
		}
		if buyer != contract.WaffoBuyerIdentity(order.UserId) {
			return ErrOrderIdentity
		}
		return p.deps.Subscriptions.Complete(ctx, reference, string(payload), contract.PaymentProviderWaffoPancake, "")
	}
	order, err := p.deps.TopUps.Get(ctx, reference)
	if err != nil {
		return err
	}
	if order.PaymentProvider != contract.PaymentProviderWaffoPancake {
		return contract.ErrPaymentMethodMismatch
	}
	if buyer != contract.WaffoBuyerIdentity(order.UserId) {
		return ErrOrderIdentity
	}
	_, err = p.deps.TopUps.Complete(ctx, contract.TopUpCompletion{TradeNo: reference, Provider: contract.PaymentProviderWaffoPancake, CallerIP: callerIP})
	return err
}
