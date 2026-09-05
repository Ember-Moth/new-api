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
	"github.com/QuantumNous/new-api/internal/module/billing/topups"
	identitycontract "github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/shopspring/decimal"
)

type Gateway interface {
	ValidateSubscription(string, string) error
	EpayWallet(context.Context, contract.CheckoutRequest) (contract.CheckoutSession, error)
}
type Dependencies struct {
	Config     func() contract.WalletConfig
	Buyer      func(context.Context, int) (*identitycontract.CheckoutBuyer, error)
	GroupRatio func(string) float64
	TopUps     *topups.Store
	Gateway    Gateway
}
type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

func (s *Service) Quote(ctx context.Context, userID int, amount int64) (contract.WalletQuote, error) {
	cfg := s.deps.Config()
	return s.quote(ctx, cfg, userID, amount)
}
func (s *Service) quote(ctx context.Context, cfg contract.WalletConfig, userID int, amount int64) (contract.WalletQuote, error) {
	stored, credit, err := ConvertAmount(amount, cfg.QuotaPerUnit, cfg.TokensDisplay)
	if err != nil {
		return contract.WalletQuote{}, err
	}
	minimum := int64(cfg.Minimum)
	if cfg.TokensDisplay {
		value, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(minimum).Mul(decimal.NewFromFloat(cfg.QuotaPerUnit)))
		if err != nil {
			return contract.WalletQuote{}, errors.New("最低充值配置无效")
		}
		minimum = int64(value)
	}
	if amount < minimum {
		return contract.WalletQuote{}, fmt.Errorf("充值数量不能小于 %d", minimum)
	}
	if err := s.deps.TopUps.ValidateCapacity(ctx, userID, credit); err != nil {
		return contract.WalletQuote{}, err
	}
	buyer, err := s.deps.Buyer(ctx, userID)
	if err != nil {
		return contract.WalletQuote{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return contract.WalletQuote{}, errors.New("用户不存在")
	}
	ratio := s.deps.GroupRatio(buyer.Group)
	if ratio == 0 {
		ratio = 1
	}
	discount := 1.0
	if value, ok := cfg.Discounts[int(amount)]; ok {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return contract.WalletQuote{}, errors.New("充值折扣配置无效")
		}
		if value > 0 {
			discount = value
		}
	}
	if cfg.Price <= 0 || math.IsNaN(cfg.Price) || math.IsInf(cfg.Price, 0) || ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return contract.WalletQuote{}, errors.New("充值价格配置无效")
	}
	quantity := decimal.NewFromInt(amount)
	if cfg.TokensDisplay {
		quantity = quantity.Div(decimal.NewFromFloat(cfg.QuotaPerUnit))
	}
	money := quantity.Mul(decimal.NewFromFloat(cfg.Price)).Mul(decimal.NewFromFloat(ratio)).Mul(decimal.NewFromFloat(discount)).InexactFloat64()
	if money <= 0 || math.IsNaN(money) || math.IsInf(money, 0) {
		return contract.WalletQuote{}, errors.New("充值金额无效")
	}
	return contract.WalletQuote{InputAmount: amount, StoredAmount: stored, CreditedQuota: credit, Money: money}, nil
}

func (s *Service) StartEpay(ctx context.Context, userID int, input contract.WalletPayRequest) (contract.CheckoutSession, error) {
	cfg := s.deps.Config()
	if !cfg.PaymentAllowed {
		return contract.CheckoutSession{}, errors.New("payment compliance confirmation required")
	}
	quote, err := s.quote(ctx, cfg, userID, input.Amount)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if quote.Money < 0.01 {
		return contract.CheckoutSession{}, errors.New("充值金额过低")
	}
	if err := s.deps.Gateway.ValidateSubscription(contract.PaymentProviderEpay, input.PaymentMethod); err != nil {
		return contract.CheckoutSession{}, err
	}
	reference := fmt.Sprintf("USR%dNO%s%d", userID, common.GetRandomString(6), time.Now().Unix())
	row := entity.TopUp{UserId: userID, Amount: quote.StoredAmount, Money: quote.Money, TradeNo: reference, PaymentMethod: input.PaymentMethod, PaymentProvider: contract.PaymentProviderEpay, Status: common.TopUpStatusPending}
	if err := s.deps.TopUps.Create(ctx, &row); err != nil {
		return contract.CheckoutSession{}, fmt.Errorf("创建订单失败: %w", err)
	}
	result, err := s.deps.Gateway.EpayWallet(ctx, contract.CheckoutRequest{Provider: contract.PaymentProviderEpay, TradeNo: reference, PaymentMethod: input.PaymentMethod, Price: quote.Money, InputAmount: input.Amount})
	if err != nil {
		return contract.CheckoutSession{}, fmt.Errorf("拉起支付失败: %w", err)
	}
	result.OrderID = reference
	return result, nil
}
