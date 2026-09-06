package purchases

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/shopspring/decimal"
)

func (s *Service) StartCreem(ctx context.Context, userID int, input contract.CreemWalletRequest) (contract.CheckoutSession, error) {
	cfg := s.deps.Config()
	if !cfg.PaymentAllowed {
		return contract.CheckoutSession{}, errors.New("payment compliance confirmation required")
	}
	if input.PaymentMethod != contract.PaymentProviderCreem {
		return contract.CheckoutSession{}, errors.New("不支持的支付渠道")
	}
	if input.ProductId == "" {
		return contract.CheckoutSession{}, errors.New("请选择产品")
	}
	var products []contract.CreemProduct
	if err := common.UnmarshalJsonStr(cfg.CreemProducts, &products); err != nil {
		return contract.CheckoutSession{}, errors.New("产品配置错误")
	}
	var selected *contract.CreemProduct
	for i := range products {
		if products[i].ProductId == input.ProductId {
			selected = &products[i]
			break
		}
	}
	if selected == nil {
		return contract.CheckoutSession{}, errors.New("产品不存在")
	}
	if selected.Price < 0 || math.IsNaN(selected.Price) || math.IsInf(selected.Price, 0) {
		return contract.CheckoutSession{}, errors.New("产品配置错误")
	}
	credit, err := ValidateCredit(decimal.NewFromInt(selected.Quota))
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if err := s.deps.TopUps.ValidateCapacity(ctx, userID, credit); err != nil {
		return contract.CheckoutSession{}, err
	}
	buyer, err := s.deps.Buyer(ctx, userID)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return contract.CheckoutSession{}, errors.New("用户不存在")
	}
	if err := s.deps.Gateway.ValidateSubscription(contract.PaymentProviderCreem, ""); err != nil {
		return contract.CheckoutSession{}, err
	}
	reference := fmt.Sprintf("creem-api-ref-%d-%d-%s", userID, time.Now().UnixMilli(), common.GetRandomString(4))
	reference = "ref_" + common.Sha1([]byte(reference))
	row := entity.TopUp{UserId: userID, Amount: selected.Quota, Money: selected.Price, TradeNo: reference, PaymentMethod: contract.PaymentMethodCreem, PaymentProvider: contract.PaymentProviderCreem, Status: common.TopUpStatusPending}
	if err := s.deps.TopUps.Create(ctx, &row); err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, "creem", reference, "order", err)
	}
	result, err := s.deps.Gateway.Creem(ctx, contract.CheckoutRequest{TradeNo: reference, ProductID: selected.ProductId, Title: selected.Name, Email: buyer.Email, Username: buyer.Username, Quota: selected.Quota})
	if err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, "creem", reference, "gateway", err)
	}
	result.OrderID = reference
	return result, nil
}
