package purchases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
)

func (s *Service) WaffoQuote(ctx context.Context, userID int, amount int64, provider string) (contract.WalletQuote, error) {
	return s.waffoQuote(ctx, s.deps.Config(), userID, amount, provider)
}

func (s *Service) waffoQuote(ctx context.Context, cfg contract.WalletConfig, userID int, amount int64, provider string) (contract.WalletQuote, error) {
	var minimum int
	switch provider {
	case contract.PaymentProviderWaffo:
		minimum, cfg.Price = cfg.WaffoMinimum, cfg.WaffoUnitPrice
	case contract.PaymentProviderWaffoPancake:
		minimum, cfg.Price = cfg.PancakeMinimum, cfg.PancakeUnitPrice
	default:
		return contract.WalletQuote{}, errors.New("不支持的支付渠道")
	}
	// Waffo minimums are expressed in the input display units, including tokens.
	if amount < int64(minimum) {
		return contract.WalletQuote{}, fmt.Errorf("充值数量不能小于 %d", minimum)
	}
	cfg.Minimum = 0
	return s.quote(ctx, cfg, userID, amount)
}

func (s *Service) StartWaffo(ctx context.Context, userID int, input contract.WaffoWalletRequest, provider string) (contract.CheckoutSession, error) {
	cfg := s.deps.Config()
	if !cfg.PaymentAllowed {
		return contract.CheckoutSession{}, errors.New("payment compliance confirmation required")
	}
	var methodType, methodName, prefix string
	switch provider {
	case contract.PaymentProviderWaffo:
		if !cfg.WaffoEnabled {
			return contract.CheckoutSession{}, errors.New("Waffo 支付未启用")
		}
		if !cfg.WaffoConfigured {
			return contract.CheckoutSession{}, errors.New("支付配置错误")
		}
		prefix = "WAFFO"
		if input.PayMethodIndex != nil {
			index := *input.PayMethodIndex
			if index < 0 || index >= len(cfg.WaffoMethods) {
				return contract.CheckoutSession{}, errors.New("不支持的支付方式")
			}
			methodType, methodName = cfg.WaffoMethods[index].PayMethodType, cfg.WaffoMethods[index].PayMethodName
		} else if input.PayMethodType != "" {
			for _, method := range cfg.WaffoMethods {
				if method.PayMethodType == input.PayMethodType && method.PayMethodName == input.PayMethodName {
					methodType, methodName = method.PayMethodType, method.PayMethodName
					break
				}
			}
			if methodType == "" {
				return contract.CheckoutSession{}, errors.New("不支持的支付方式")
			}
		}
	case contract.PaymentProviderWaffoPancake:
		if !Information(cfg).Pancake {
			return contract.CheckoutSession{}, errors.New("Waffo Pancake 配置不完整")
		}
		prefix = "WAFFO_PANCAKE"
	default:
		return contract.CheckoutSession{}, errors.New("不支持的支付渠道")
	}
	quote, err := s.waffoQuote(ctx, cfg, userID, input.Amount, provider)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if quote.Money < 0.01 {
		return contract.CheckoutSession{}, errors.New("充值金额过低")
	}
	buyer, err := s.deps.Buyer(ctx, userID)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return contract.CheckoutSession{}, errors.New("用户不存在")
	}
	reference := fmt.Sprintf("%s-%d-%d-%s", prefix, userID, time.Now().UnixMilli(), common.GetRandomString(6))
	row := entity.TopUp{UserId: userID, Amount: quote.StoredAmount, Money: quote.Money, TradeNo: reference, PaymentMethod: provider, PaymentProvider: provider, Status: common.TopUpStatusPending}
	if err := s.deps.TopUps.Create(ctx, &row); err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, provider, reference, "order", err)
	}
	request := contract.CheckoutRequest{Provider: provider, TradeNo: reference, ProductID: cfg.PancakeProductID, InputAmount: input.Amount, Price: quote.Money, UserID: userID, Email: buyer.Email, PayMethodType: methodType, PayMethodName: methodName}
	var result contract.CheckoutSession
	if provider == contract.PaymentProviderWaffo {
		result, err = s.deps.Gateway.WaffoWallet(ctx, request)
	} else {
		result, err = s.deps.Gateway.PancakeWallet(ctx, request)
	}
	// A timeout can follow successful provider creation. The durable pending
	// order must remain available to its verified payment callback.
	if err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, provider, reference, "gateway", err)
	}
	result.OrderID = reference
	return result, nil
}
