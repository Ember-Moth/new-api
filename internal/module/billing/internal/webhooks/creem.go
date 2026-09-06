package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func (p *Processor) Creem(ctx context.Context, payload []byte, signature, callerIP string) error {
	cfg := p.deps.Config()
	if !cfg.PaymentAllowed || strings.TrimSpace(cfg.CreemSecret) == "" {
		return ErrDisabled
	}
	mac := hmac.New(sha256.New, []byte(cfg.CreemSecret))
	_, _ = mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if signature == "" || !hmac.Equal([]byte(signature), []byte(expected)) {
		return ErrSignature
	}
	var event contract.CreemEvent
	if err := common.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("%w: %v", ErrPayload, err)
	}
	if event.EventType != "checkout.completed" || event.Object.Order.Status != "paid" {
		return nil
	}
	if event.Object.RequestId == "" {
		return ErrPayload
	}
	data, err := common.Marshal(event)
	if err != nil {
		return err
	}
	return p.complete(ctx, event.Object.RequestId, contract.PaymentProviderCreem, string(data), "", event.Object.Customer.Email, callerIP, event.Object.Order.Type == "onetime")
}
