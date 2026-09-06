package purchases

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/shopspring/decimal"
)

type InputError struct {
	Minimum bool
	Limit   int64
}

func (e *InputError) Error() string {
	if e.Minimum {
		return fmt.Sprintf("充值数量不能小于 %d", e.Limit)
	}
	return "充值数量不能大于 10000"
}

type RedirectError struct{ Success bool }

func (e *RedirectError) Error() string {
	if e.Success {
		return "支付成功重定向URL不在可信任域名列表中"
	}
	return "支付取消重定向URL不在可信任域名列表中"
}

func (s *Service) StripeQuote(ctx context.Context, userID int, amount int64) (contract.StripeWalletQuote, error) {
	return s.stripeQuote(ctx, s.deps.Config(), userID, amount)
}
func (s *Service) stripeQuote(ctx context.Context, cfg contract.WalletConfig, userID int, amount int64) (contract.StripeWalletQuote, error) {
	if cfg.QuotaPerUnit <= 0 || math.IsNaN(cfg.QuotaPerUnit) || math.IsInf(cfg.QuotaPerUnit, 0) {
		return contract.StripeWalletQuote{}, errors.New("额度单位配置错误")
	}
	minimum := int64(cfg.StripeMinimum)
	if cfg.TokensDisplay {
		value, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(minimum).Mul(decimal.NewFromFloat(cfg.QuotaPerUnit)))
		if err != nil {
			return contract.StripeWalletQuote{}, errors.New("最低充值配置无效")
		}
		minimum = int64(value)
	}
	if amount < minimum || amount <= 0 {
		return contract.StripeWalletQuote{}, &InputError{Minimum: true, Limit: minimum}
	}
	if amount > 10000 {
		return contract.StripeWalletQuote{}, &InputError{Limit: 10000}
	}
	buyer, err := s.deps.Buyer(ctx, userID)
	if err != nil {
		return contract.StripeWalletQuote{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return contract.StripeWalletQuote{}, errors.New("用户不存在")
	}
	ratio := s.deps.GroupRatio(buyer.Group)
	if ratio == 0 {
		ratio = 1
	}
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) || cfg.StripeUnitPrice <= 0 || math.IsNaN(cfg.StripeUnitPrice) || math.IsInf(cfg.StripeUnitPrice, 0) {
		return contract.StripeWalletQuote{}, errors.New("Stripe 价格配置无效")
	}
	creditBase := float64(amount) * ratio
	if math.IsNaN(creditBase) || math.IsInf(creditBase, 0) {
		return contract.StripeWalletQuote{}, errors.New("充值额度超出系统可表示范围")
	}
	credit, err := ValidateCredit(decimal.NewFromFloat(creditBase).Mul(decimal.NewFromFloat(cfg.QuotaPerUnit)))
	if err != nil {
		return contract.StripeWalletQuote{}, err
	}
	if err := s.deps.TopUps.ValidateCapacity(ctx, userID, credit); err != nil {
		return contract.StripeWalletQuote{}, err
	}
	discount := 1.0
	if value, ok := cfg.Discounts[int(amount)]; ok {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return contract.StripeWalletQuote{}, errors.New("充值折扣配置无效")
		}
		if value > 0 {
			discount = value
		}
	}
	quantity := decimal.NewFromInt(amount)
	if cfg.TokensDisplay {
		quantity = quantity.Div(decimal.NewFromFloat(cfg.QuotaPerUnit))
	}
	money := quantity.Mul(decimal.NewFromFloat(cfg.StripeUnitPrice)).Mul(decimal.NewFromFloat(ratio)).Mul(decimal.NewFromFloat(discount)).InexactFloat64()
	if money <= 0 || math.IsNaN(money) || math.IsInf(money, 0) {
		return contract.StripeWalletQuote{}, errors.New("充值金额无效")
	}
	return contract.StripeWalletQuote{Money: money, CreditBase: creditBase, CreditedQuota: credit, Quantity: amount}, nil
}

func (s *Service) StartStripe(ctx context.Context, userID int, input contract.StripeWalletRequest) (contract.CheckoutSession, error) {
	cfg := s.deps.Config()
	if !cfg.PaymentAllowed {
		return contract.CheckoutSession{}, errors.New("payment compliance confirmation required")
	}
	if input.PaymentMethod != contract.PaymentProviderStripe {
		return contract.CheckoutSession{}, errors.New("不支持的支付渠道")
	}
	quote, err := s.stripeQuote(ctx, cfg, userID, input.Amount)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if input.SuccessURL != "" && (s.deps.ValidateRedirect == nil || s.deps.ValidateRedirect(input.SuccessURL) != nil) {
		return contract.CheckoutSession{}, &RedirectError{Success: true}
	}
	if input.CancelURL != "" && (s.deps.ValidateRedirect == nil || s.deps.ValidateRedirect(input.CancelURL) != nil) {
		return contract.CheckoutSession{}, &RedirectError{}
	}
	if cfg.StripePriceID == "" {
		return contract.CheckoutSession{}, errors.New("Stripe 未配置商品")
	}
	if err := s.deps.Gateway.ValidateSubscription(contract.PaymentProviderStripe, ""); err != nil {
		return contract.CheckoutSession{}, err
	}
	buyer, err := s.deps.Buyer(ctx, userID)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return contract.CheckoutSession{}, errors.New("用户不存在")
	}
	reference := fmt.Sprintf("new-api-ref-%d-%d-%s", userID, time.Now().UnixMilli(), common.GetRandomString(4))
	reference = "ref_" + common.Sha1([]byte(reference))
	row := entity.TopUp{UserId: userID, Amount: input.Amount, Money: quote.CreditBase, TradeNo: reference, PaymentMethod: contract.PaymentMethodStripe, PaymentProvider: contract.PaymentProviderStripe, Status: common.TopUpStatusPending}
	if err := s.deps.TopUps.Create(ctx, &row); err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, "stripe", reference, "order", err)
	}
	result, err := s.deps.Gateway.StripeWallet(ctx, contract.CheckoutRequest{Provider: contract.PaymentProviderStripe, TradeNo: reference, ProductID: cfg.StripePriceID, InputAmount: input.Amount, Email: buyer.Email, CustomerID: buyer.StripeCustomer, SuccessURL: input.SuccessURL, CancelURL: input.CancelURL, AllowPromotionCodes: cfg.StripePromotionCodes})
	if err != nil {
		return contract.CheckoutSession{}, checkoutFailure(ctx, "stripe", reference, "gateway", err)
	}
	result.OrderID = reference
	return result, nil
}
