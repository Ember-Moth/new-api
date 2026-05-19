package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type SubscriptionStripePaymentIntentPayRequest struct {
	PlanId int `json:"plan_id"`
}

func SubscriptionRequestStripePaymentIntentPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionStripePaymentIntentPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if !isStripePaymentIntentTopUpEnabled() {
		common.ApiErrorMsg(c, "Stripe PaymentIntent 未启用")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}

	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user == nil {
		common.ApiErrorMsg(c, "用户不存在")
		return
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}

	payMoney := decimal.NewFromFloat(plan.PriceAmount).
		Mul(decimal.NewFromFloat(setting.StripePaymentIntentUnitPrice)).
		InexactFloat64()
	stripeAmount := stripePaymentIntentAmount(payMoney, setting.StripePaymentIntentCurrency)
	if stripeAmount <= 0 {
		common.ApiErrorMsg(c, "套餐金额过低")
		return
	}

	reference := fmt.Sprintf("sub-pi-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "sub_ref_" + common.Sha1([]byte(reference))
	currency := strings.ToLower(strings.TrimSpace(setting.StripePaymentIntentCurrency))
	description := fmt.Sprintf("Subscription %s", plan.Title)

	paymentIntentResult, err := genStripePaymentIntentWithMetadata(
		referenceId,
		user.Email,
		stripeAmount,
		currency,
		description,
		map[string]string{
			"source":  "subscription",
			"plan_id": strconv.Itoa(plan.Id),
		},
	)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 订阅支付创建失败 user_id=%d trade_no=%s plan_id=%d stripe_amount=%d currency=%s error=%q", userId, referenceId, plan.Id, stripeAmount, currency, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		GatewayTradeNo:  paymentIntentResult.id,
		GatewayAmount:   stripeAmount,
		GatewayCurrency: currency,
		PaymentMethod:   model.PaymentMethodStripeIntent,
		PaymentProvider: model.PaymentProviderStripeIntent,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 订阅订单创建失败 user_id=%d trade_no=%s plan_id=%d error=%q", userId, referenceId, plan.Id, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 订阅订单创建成功 user_id=%d trade_no=%s plan_id=%d money=%.2f stripe_amount=%d currency=%s", userId, referenceId, plan.Id, plan.PriceAmount, stripeAmount, currency))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"client_secret":     paymentIntentResult.clientSecret,
			"publishable_key":   setting.StripePaymentIntentPublishableKey,
			"trade_no":          referenceId,
			"payment_intent_id": paymentIntentResult.id,
			"amount":            stripeAmount,
			"currency":          currency,
		},
	})
}
