package webhooks

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func (p *Processor) Epay(ctx context.Context, params map[string]string, callerIP string) error {
	if !p.Enabled("epay") {
		return ErrDisabled
	}
	if len(params) == 0 {
		return ErrPayload
	}
	event, err := p.deps.EpayVerifier(params)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignature, err)
	}
	if !event.Paid {
		return nil
	}
	_, err = p.deps.TopUps.Complete(ctx, contract.TopUpCompletion{TradeNo: event.TradeNo, Provider: contract.PaymentProviderEpay, ActualMethod: event.PaymentMethod, CallerIP: callerIP})
	return err
}
