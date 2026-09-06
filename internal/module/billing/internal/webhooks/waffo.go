package webhooks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/waffo-com/waffo-go/core"
	"github.com/waffo-com/waffo-go/utils"
)

func (p *Processor) Waffo(ctx context.Context, payload []byte, signature, callerIP string) (*contract.SignedWebhookResponse, error) {
	cfg := p.deps.Config()
	if !cfg.PaymentAllowed || !cfg.WaffoEnabled || strings.TrimSpace(cfg.WaffoPrivateKey) == "" || strings.TrimSpace(cfg.WaffoPublicKey) == "" {
		return nil, ErrDisabled
	}
	if err := utils.ValidatePrivateKey(cfg.WaffoPrivateKey); err != nil {
		return nil, err
	}
	if err := utils.ValidatePublicKey(cfg.WaffoPublicKey); err != nil {
		return nil, err
	}
	if !utils.Verify(string(payload), signature, cfg.WaffoPublicKey) {
		return nil, ErrSignature
	}
	var event struct {
		EventType string                         `json:"eventType"`
		Result    core.PaymentNotificationResult `json:"result"`
	}
	var processErr error
	if err := common.Unmarshal(payload, &event); err != nil {
		processErr = fmt.Errorf("%w: %v", ErrPayload, err)
	} else if event.EventType == core.EventPayment {
		switch event.Result.OrderStatus {
		case core.OrderStatusPaySuccess:
			if event.Result.MerchantOrderID == "" {
				processErr = ErrPayload
			} else {
				_, processErr = p.deps.TopUps.Complete(ctx, contract.TopUpCompletion{TradeNo: event.Result.MerchantOrderID, Provider: contract.PaymentProviderWaffo, CallerIP: callerIP})
			}
		case core.OrderStatusOrderClose:
			if event.Result.MerchantOrderID == "" {
				processErr = ErrPayload
			} else {
				processErr = p.deps.TopUps.FinishPending(ctx, event.Result.MerchantOrderID, contract.PaymentProviderWaffo, common.TopUpStatusFailed)
			}
			if errors.Is(processErr, contract.ErrTopUpStatusInvalid) {
				processErr = nil
			}
			// In-progress/authorization/capture states and unrelated events are not
			// failures. A later authenticated PAY_SUCCESS may still complete them.
		}
	}
	message := "success"
	if processErr != nil {
		message = "failed"
	}
	body, err := common.Marshal(map[string]string{"message": message})
	if err != nil {
		return nil, err
	}
	responseSignature, err := utils.Sign(string(body), cfg.WaffoPrivateKey)
	if err != nil {
		return nil, err
	}
	return &contract.SignedWebhookResponse{Body: body, Signature: responseSignature}, processErr
}
