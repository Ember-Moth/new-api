package subscription

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/logger"
)

type CheckoutGateways interface {
	ValidateSubscription(string, string) error
	CreateSubscription(context.Context, billingcontract.CheckoutRequest) (billingcontract.CheckoutSession, error)
	VerifyEpay(map[string]string) (billingcontract.VerifiedPayment, error)
	ReturnURL(string) string
}

type CheckoutError struct {
	Stage string
	Cause error
}

func (e *CheckoutError) Error() string {
	if e.Stage == "order" {
		return "创建订单失败"
	}
	return "拉起支付失败"
}
func (e *CheckoutError) Unwrap() error { return e.Cause }

func (s *Service) StartCheckout(ctx context.Context, userID int, provider string, input contract.CheckoutInput) (billingcontract.CheckoutSession, error) {
	if err := s.RequirePaymentCompliance(); err != nil {
		return billingcontract.CheckoutSession{}, err
	}
	if userID <= 0 || input.PlanId <= 0 {
		return billingcontract.CheckoutSession{}, errors.New("参数错误")
	}
	plan, err := s.plans.Get(ctx, input.PlanId)
	if err != nil {
		return billingcontract.CheckoutSession{}, err
	}
	if !plan.Enabled {
		return billingcontract.CheckoutSession{}, errors.New("套餐未启用")
	}
	if plan.PriceAmount < 0 || math.IsNaN(plan.PriceAmount) || math.IsInf(plan.PriceAmount, 0) {
		return billingcontract.CheckoutSession{}, errors.New("套餐金额无效")
	}
	product, method := "", provider
	switch provider {
	case "stripe":
		product = plan.StripePriceId
		if product == "" {
			return billingcontract.CheckoutSession{}, errors.New("该套餐未配置 StripePriceId")
		}
	case "creem":
		product = plan.CreemProductId
		if product == "" {
			return billingcontract.CheckoutSession{}, errors.New("该套餐未配置 CreemProductId")
		}
	case "waffo_pancake":
		product = plan.WaffoPancakeProductId
		if strings.TrimSpace(product) == "" {
			return billingcontract.CheckoutSession{}, errors.New("该套餐未配置 WaffoPancakeProductId")
		}
	case "epay":
		method = input.PaymentMethod
		if plan.PriceAmount < 0.01 {
			return billingcontract.CheckoutSession{}, errors.New("套餐金额过低")
		}
	default:
		return billingcontract.CheckoutSession{}, errors.New("unsupported payment provider")
	}
	if err := s.Gateways.ValidateSubscription(provider, method); err != nil {
		return billingcontract.CheckoutSession{}, err
	}
	buyer, err := s.checkoutBuyer(ctx, userID)
	if err != nil {
		return billingcontract.CheckoutSession{}, err
	}
	if buyer == nil || buyer.ID != userID {
		return billingcontract.CheckoutSession{}, errors.New("用户不存在")
	}
	if plan.MaxPurchasePerUser > 0 {
		count, err := s.Members.CountUserSubscriptionsByPlan(ctx, userID, plan.Id)
		if err != nil {
			return billingcontract.CheckoutSession{}, err
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return billingcontract.CheckoutSession{}, errors.New("已达到该套餐购买上限")
		}
	}
	reference := fmt.Sprintf("sub-%s-ref-%d-%d-%s", provider, userID, time.Now().UnixNano(), common.GetRandomString(6))
	tradeNo := "sub_ref_" + common.Sha1([]byte(reference))
	if provider == "epay" {
		tradeNo = fmt.Sprintf("SUBUSR%dNO%s%d", userID, common.GetRandomString(6), time.Now().Unix())
	}
	if provider == "waffo_pancake" {
		tradeNo = fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-%d-%s", userID, time.Now().UnixMilli(), common.GetRandomString(6))
	}
	order := entity.SubscriptionOrder{UserId: userID, PlanId: plan.Id, Money: plan.PriceAmount, TradeNo: tradeNo, PaymentMethod: method, PaymentProvider: provider, Status: common.TopUpStatusPending}
	if err := s.Payments.Create(ctx, &order); err != nil {
		return billingcontract.CheckoutSession{}, &CheckoutError{Stage: "order", Cause: err}
	}
	result, err := s.Gateways.CreateSubscription(ctx, billingcontract.CheckoutRequest{Provider: provider, ProductID: product, TradeNo: tradeNo, PaymentMethod: method, Title: plan.Title, Price: plan.PriceAmount, UserID: userID, Email: buyer.Email, Username: buyer.Username, CustomerID: buyer.StripeCustomer})
	if err != nil {
		// The provider may have accepted a timed-out request. Keep the durable order
		// available for verified completion/expiry callbacks instead of guessing.
		logger.LogError(ctx, fmt.Sprintf("subscription checkout failed provider=%s trade_no=%s error=%q", provider, tradeNo, err.Error()))
		return billingcontract.CheckoutSession{}, &CheckoutError{Stage: "gateway", Cause: err}
	}
	result.OrderID = tradeNo
	return result, nil
}
