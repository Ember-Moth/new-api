package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/paymentintent"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/thanhpk/randstr"
)

var stripeAdaptor = &StripeAdaptor{}

// StripePayRequest represents a payment request for Stripe checkout.
type StripePayRequest struct {
	// Amount is the quantity of units to purchase.
	Amount int64 `json:"amount"`
	// PaymentMethod specifies the payment method (e.g., "stripe").
	PaymentMethod string `json:"payment_method"`
	// SuccessURL is the optional custom URL to redirect after successful payment.
	// If empty, defaults to the server's console log page.
	SuccessURL string `json:"success_url,omitempty"`
	// CancelURL is the optional custom URL to redirect when payment is canceled.
	// If empty, defaults to the server's console topup page.
	CancelURL string `json:"cancel_url,omitempty"`
}

type StripeAdaptor struct {
}

type stripePaymentIntentResult struct {
	id           string
	clientSecret string
}

func (*StripeAdaptor) RequestAmount(c *gin.Context, req *StripePayRequest) {
	if req.Amount < getStripeMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getStripeMinTopup())})
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getStripePayMoney(float64(req.Amount), group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func RequestStripePaymentIntentAmount(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if !isStripePaymentIntentTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Stripe PaymentIntent 未启用"})
		return
	}
	if req.Amount < getStripePaymentIntentMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getStripePaymentIntentMinTopup())})
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getStripePaymentIntentPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func (*StripeAdaptor) RequestPay(c *gin.Context, req *StripePayRequest) {
	if req.PaymentMethod != model.PaymentMethodStripe {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if req.Amount < getStripeMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", getStripeMinTopup()), "data": 10})
		return
	}
	if req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量不能大于 10000", "data": 10})
		return
	}

	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付成功重定向URL不在可信任域名列表中", "data": ""})
		return
	}

	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付取消重定向URL不在可信任域名列表中", "data": ""})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户信息失败"})
		return
	}
	chargedMoney := GetChargedAmount(float64(req.Amount), *user)

	reference := fmt.Sprintf("new-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	payLink, err := genStripeLink(referenceId, user.StripeCustomer, user.Email, req.Amount, req.SuccessURL, req.CancelURL)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 创建 Checkout Session 失败 user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           chargedMoney,
		TradeNo:         referenceId,
		PaymentMethod:   model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Stripe 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f", id, referenceId, req.Amount, chargedMoney))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": payLink,
		},
	})
}

func RequestStripeAmount(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	stripeAdaptor.RequestAmount(c, &req)
}

func RequestStripePay(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	stripeAdaptor.RequestPay(c, &req)
}

func RequestStripePaymentIntentPay(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.PaymentMethod != model.PaymentMethodStripeIntent {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if !isStripePaymentIntentTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Stripe PaymentIntent 未启用"})
		return
	}
	if req.Amount < getStripePaymentIntentMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", getStripePaymentIntentMinTopup()), "data": 10})
		return
	}
	if req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量不能大于 10000", "data": 10})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户信息失败"})
		return
	}
	group := user.Group
	payMoney := getStripePaymentIntentPayMoney(req.Amount, group)
	stripeAmount := stripePaymentIntentAmount(payMoney, setting.StripePaymentIntentCurrency)
	if stripeAmount <= 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	chargedMoney := GetChargedAmount(float64(req.Amount), *user)
	reference := fmt.Sprintf("new-api-pi-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	paymentIntentResult, err := genStripePaymentIntent(referenceId, user.Email, stripeAmount, setting.StripePaymentIntentCurrency)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 创建失败 user_id=%d trade_no=%s amount=%d stripe_amount=%d currency=%s error=%q", id, referenceId, req.Amount, stripeAmount, setting.StripePaymentIntentCurrency, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           chargedMoney,
		TradeNo:         referenceId,
		GatewayTradeNo:  paymentIntentResult.id,
		GatewayAmount:   stripeAmount,
		GatewayCurrency: strings.ToLower(strings.TrimSpace(setting.StripePaymentIntentCurrency)),
		PaymentMethod:   model.PaymentMethodStripeIntent,
		PaymentProvider: model.PaymentProviderStripeIntent,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Stripe PaymentIntent 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f stripe_amount=%d currency=%s", id, referenceId, req.Amount, chargedMoney, stripeAmount, setting.StripePaymentIntentCurrency))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"client_secret":     paymentIntentResult.clientSecret,
			"publishable_key":   setting.StripePaymentIntentPublishableKey,
			"trade_no":          referenceId,
			"payment_intent_id": paymentIntentResult.id,
			"amount":            stripeAmount,
			"currency":          strings.ToLower(strings.TrimSpace(setting.StripePaymentIntentCurrency)),
		},
	})
}

func StripeWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	if !isStripeWebhookEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	signature := c.GetHeader("Stripe-Signature")
	logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 收到请求 path=%q client_ip=%s signature_present=%t payload_bytes=%d", c.Request.RequestURI, c.ClientIP(), signature != "", len(payload)))
	event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripeWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})

	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe webhook 验签失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	callerIp := c.ClientIP()
	logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 验签成功 event_type=%s client_ip=%s path=%q", string(event.Type), callerIp, c.Request.RequestURI))
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		sessionCompleted(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionExpired:
		sessionExpired(ctx, event)
	case stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		sessionAsyncPaymentSucceeded(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		sessionAsyncPaymentFailed(ctx, event, callerIp)
	default:
		logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 忽略事件 event_type=%s client_ip=%s", string(event.Type), callerIp))
	}

	c.Status(http.StatusOK)
}

func StripePaymentIntentWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	if !isStripePaymentIntentWebhookEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	signature := c.GetHeader("Stripe-Signature")
	logger.LogInfo(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 收到请求 path=%q client_ip=%s signature_present=%t payload_bytes=%d", c.Request.RequestURI, c.ClientIP(), signature != "", len(payload)))
	event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripePaymentIntentWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})

	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 验签失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	callerIp := c.ClientIP()
	logger.LogInfo(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 验签成功 event_type=%s client_ip=%s path=%q", string(event.Type), callerIp, c.Request.RequestURI))
	var processingErr error
	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		processingErr = paymentIntentSucceeded(ctx, event, callerIp)
	case stripe.EventTypePaymentIntentPaymentFailed:
		processingErr = paymentIntentFailed(ctx, event, callerIp, common.TopUpStatusFailed)
	case stripe.EventTypePaymentIntentCanceled:
		processingErr = paymentIntentFailed(ctx, event, callerIp, common.TopUpStatusExpired)
	default:
		logger.LogInfo(ctx, fmt.Sprintf("Stripe PaymentIntent webhook 忽略事件 event_type=%s client_ip=%s", string(event.Type), callerIp))
	}

	if processingErr != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusOK)
}

func sessionCompleted(ctx context.Context, event stripe.Event, callerIp string) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "complete" != status {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe checkout.completed 状态异常，忽略处理 trade_no=%s status=%s client_ip=%s", referenceId, status, callerIp))
		return
	}

	paymentStatus := event.GetObjectValue("payment_status")
	if paymentStatus != "paid" {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe Checkout 支付未完成，等待异步结果 trade_no=%s payment_status=%s client_ip=%s", referenceId, paymentStatus, callerIp))
		return
	}

	fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentSucceeded handles delayed payment methods (bank transfer, SEPA, etc.)
// that confirm payment after the checkout session completes.
func sessionAsyncPaymentSucceeded(ctx context.Context, event stripe.Event, callerIp string) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	logger.LogInfo(ctx, fmt.Sprintf("Stripe 异步支付成功 trade_no=%s client_ip=%s", referenceId, callerIp))

	fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentFailed marks orders as failed when delayed payment methods
// ultimately fail (e.g. bank transfer not received, SEPA rejected).
func sessionAsyncPaymentFailed(ctx context.Context, event stripe.Event, callerIp string) {
	referenceId := event.GetObjectValue("client_reference_id")
	logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败 trade_no=%s client_ip=%s", referenceId, callerIp))

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败事件缺少订单号 client_ip=%s", callerIp))
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败但本地订单不存在 trade_no=%s client_ip=%s", referenceId, callerIp))
		return
	}

	if topUp.PaymentProvider != model.PaymentProviderStripe {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败但订单支付网关不匹配 trade_no=%s payment_provider=%s client_ip=%s", referenceId, topUp.PaymentProvider, callerIp))
		return
	}

	if topUp.Status != common.TopUpStatusPending {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 异步支付失败但订单状态非 pending，忽略处理 trade_no=%s status=%s client_ip=%s", referenceId, topUp.Status, callerIp))
		return
	}

	topUp.Status = common.TopUpStatusFailed
	if err := topUp.Update(); err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe 标记充值订单失败状态失败 trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值订单已标记为失败 trade_no=%s client_ip=%s", referenceId, callerIp))
}

func isNonRetryableStripePaymentIntentError(err error) bool {
	return errors.Is(err, model.ErrTopUpStatusInvalid) ||
		errors.Is(err, model.ErrTopUpNotFound) ||
		errors.Is(err, model.ErrPaymentMethodMismatch) ||
		errors.Is(err, model.ErrGatewayTradeNoMismatch) ||
		errors.Is(err, model.ErrTopUpAmountMismatch) ||
		errors.Is(err, model.ErrTopUpCurrencyMismatch)
}

func paymentIntentSucceeded(ctx context.Context, event stripe.Event, callerIp string) error {
	referenceId := event.GetObjectValue("metadata", "trade_no")
	gatewayTradeNo := event.GetObjectValue("id")
	status := event.GetObjectValue("status")
	if status != "succeeded" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 状态异常，忽略处理 trade_no=%s status=%s client_ip=%s", referenceId, status, callerIp))
		return nil
	}
	if referenceId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 成功事件缺少订单号 client_ip=%s", callerIp))
		return nil
	}
	if gatewayTradeNo == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 成功事件缺少网关订单号 trade_no=%s client_ip=%s", referenceId, callerIp))
		return nil
	}
	amountReceived, err := strconv.ParseInt(event.GetObjectValue("amount_received"), 10, 64)
	if err != nil || amountReceived <= 0 {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 成功事件金额无效 trade_no=%s payment_intent=%s amount_received=%q client_ip=%s", referenceId, gatewayTradeNo, event.GetObjectValue("amount_received"), callerIp))
		return nil
	}
	currency := strings.ToLower(strings.TrimSpace(event.GetObjectValue("currency")))
	if currency == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 成功事件缺少币种 trade_no=%s payment_intent=%s client_ip=%s", referenceId, gatewayTradeNo, callerIp))
		return nil
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	err = model.RechargeStripePaymentIntent(referenceId, gatewayTradeNo, amountReceived, currency, callerIp)
	if err != nil {
		if errors.Is(err, model.ErrTopUpStatusInvalid) {
			logger.LogInfo(ctx, fmt.Sprintf("Stripe PaymentIntent 订单已处理，忽略重复成功事件 trade_no=%s payment_intent=%s event_type=%s client_ip=%s", referenceId, gatewayTradeNo, string(event.Type), callerIp))
			return nil
		}
		if errors.Is(err, model.ErrTopUpNotFound) {
			logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 成功但本地订单不存在 trade_no=%s payment_intent=%s event_type=%s client_ip=%s", referenceId, gatewayTradeNo, string(event.Type), callerIp))
			return nil
		}
		logger.LogError(ctx, fmt.Sprintf("Stripe PaymentIntent 充值处理失败 trade_no=%s payment_intent=%s amount_received=%d currency=%s event_type=%s client_ip=%s error=%q", referenceId, gatewayTradeNo, amountReceived, currency, string(event.Type), callerIp, err.Error()))
		if isNonRetryableStripePaymentIntentError(err) {
			return nil
		}
		return err
	}

	logger.LogInfo(ctx, fmt.Sprintf("Stripe PaymentIntent 充值成功 trade_no=%s payment_intent=%s amount_received=%.2f currency=%s event_type=%s client_ip=%s", referenceId, gatewayTradeNo, float64(amountReceived)/stripeCurrencyMultiplier(currency), strings.ToUpper(currency), string(event.Type), callerIp))
	return nil
}

func paymentIntentFailed(ctx context.Context, event stripe.Event, callerIp string, targetStatus string) error {
	referenceId := event.GetObjectValue("metadata", "trade_no")
	gatewayTradeNo := event.GetObjectValue("id")
	logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 失败/取消 trade_no=%s payment_intent=%s status=%s client_ip=%s", referenceId, gatewayTradeNo, event.GetObjectValue("status"), callerIp))

	if referenceId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 失败/取消事件缺少订单号 client_ip=%s", callerIp))
		return nil
	}
	if gatewayTradeNo == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe PaymentIntent 失败/取消事件缺少网关订单号 trade_no=%s client_ip=%s", referenceId, callerIp))
		return nil
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	if err := model.UpdatePendingTopUpStatusWithGatewayTradeNo(referenceId, model.PaymentProviderStripeIntent, gatewayTradeNo, targetStatus); err != nil {
		if errors.Is(err, model.ErrTopUpNotFound) || errors.Is(err, model.ErrTopUpStatusInvalid) {
			return nil
		}
		logger.LogError(ctx, fmt.Sprintf("Stripe PaymentIntent 更新充值订单状态失败 trade_no=%s payment_intent=%s target_status=%s client_ip=%s error=%q", referenceId, gatewayTradeNo, targetStatus, callerIp, err.Error()))
		if isNonRetryableStripePaymentIntentError(err) {
			return nil
		}
		return err
	}
	return nil
}

// fulfillOrder is the shared logic for crediting quota after payment is confirmed.
func fulfillOrder(ctx context.Context, event stripe.Event, referenceId string, customerId string, callerIp string) {
	if len(referenceId) == 0 {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 完成订单时缺少订单号 client_ip=%s", callerIp))
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	payload := map[string]any{
		"customer":     customerId,
		"amount_total": event.GetObjectValue("amount_total"),
		"currency":     strings.ToUpper(event.GetObjectValue("currency")),
		"event_type":   string(event.Type),
	}
	if err := model.CompleteSubscriptionOrder(referenceId, common.GetJsonString(payload), model.PaymentProviderStripe, ""); err == nil {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 订阅订单处理成功 trade_no=%s event_type=%s client_ip=%s", referenceId, string(event.Type), callerIp))
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(ctx, fmt.Sprintf("Stripe 订阅订单处理失败 trade_no=%s event_type=%s client_ip=%s error=%q", referenceId, string(event.Type), callerIp, err.Error()))
		return
	}

	err := model.Recharge(referenceId, customerId, callerIp)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe 充值处理失败 trade_no=%s event_type=%s client_ip=%s error=%q", referenceId, string(event.Type), callerIp, err.Error()))
		return
	}

	total, _ := strconv.ParseFloat(event.GetObjectValue("amount_total"), 64)
	currency := strings.ToUpper(event.GetObjectValue("currency"))
	logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值成功 trade_no=%s amount_total=%.2f currency=%s event_type=%s client_ip=%s", referenceId, total/100, currency, string(event.Type), callerIp))
}

func sessionExpired(ctx context.Context, event stripe.Event) {
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "expired" != status {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe checkout.expired 状态异常，忽略处理 trade_no=%s status=%s", referenceId, status))
		return
	}

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, "Stripe checkout.expired 缺少订单号")
		return
	}

	// Subscription order expiration
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	if err := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); err == nil {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 订阅订单已过期 trade_no=%s", referenceId))
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(ctx, fmt.Sprintf("Stripe 订阅订单过期处理失败 trade_no=%s error=%q", referenceId, err.Error()))
		return
	}

	err := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusExpired)
	if errors.Is(err, model.ErrTopUpNotFound) {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 充值订单不存在，无法标记过期 trade_no=%s", referenceId))
		return
	}
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe 充值订单过期处理失败 trade_no=%s error=%q", referenceId, err.Error()))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值订单已过期 trade_no=%s", referenceId))
}

// genStripeLink generates a Stripe Checkout session URL for payment.
// It creates a new checkout session with the specified parameters and returns the payment URL.
//
// Parameters:
//   - referenceId: unique reference identifier for the transaction
//   - customerId: existing Stripe customer ID (empty string if new customer)
//   - email: customer email address for new customer creation
//   - amount: quantity of units to purchase
//   - successURL: custom URL to redirect after successful payment (empty for default)
//   - cancelURL: custom URL to redirect when payment is canceled (empty for default)
//
// Returns the checkout session URL or an error if the session creation fails.
func genStripeLink(referenceId string, customerId string, email string, amount int64, successURL string, cancelURL string) (string, error) {
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		return "", fmt.Errorf("无效的Stripe API密钥")
	}

	// Use custom URLs if provided, otherwise use defaults
	successURL = strings.TrimSpace(successURL)
	cancelURL = strings.TrimSpace(cancelURL)
	if successURL == "" {
		successURL = paymentReturnPath("/console/log")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/console/topup")
	}

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(setting.StripePriceId),
				Quantity: stripe.Int64(amount),
			},
		},
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		AllowPromotionCodes: stripe.Bool(setting.StripePromotionCodesEnabled),
	}

	if "" == customerId {
		if "" != email {
			params.CustomerEmail = stripe.String(email)
		}

		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	stripeSessionClient := session.Client{
		B:   stripe.GetBackend(stripe.APIBackend),
		Key: setting.StripeApiSecret,
	}
	result, err := stripeSessionClient.New(params)
	if err != nil {
		return "", err
	}

	return result.URL, nil
}

func genStripePaymentIntent(referenceId string, email string, amount int64, currency string) (*stripePaymentIntentResult, error) {
	if !strings.HasPrefix(setting.StripePaymentIntentApiSecret, "sk_") && !strings.HasPrefix(setting.StripePaymentIntentApiSecret, "rk_") {
		return nil, fmt.Errorf("无效的Stripe PaymentIntent API密钥")
	}
	if !strings.HasPrefix(setting.StripePaymentIntentPublishableKey, "pk_") {
		return nil, fmt.Errorf("无效的Stripe PaymentIntent Publishable Key")
	}

	currency = strings.ToLower(strings.TrimSpace(currency))

	params := &stripe.PaymentIntentParams{
		Amount:      stripe.Int64(amount),
		Currency:    stripe.String(currency),
		Description: stripe.String(fmt.Sprintf("Top up %s", referenceId)),
		Metadata: map[string]string{
			"trade_no": referenceId,
			"provider": model.PaymentProviderStripeIntent,
		},
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	}
	if email != "" {
		params.ReceiptEmail = stripe.String(email)
	}

	stripePaymentIntentClient := paymentintent.Client{
		B:   stripe.GetBackend(stripe.APIBackend),
		Key: setting.StripePaymentIntentApiSecret,
	}
	result, err := stripePaymentIntentClient.New(params)
	if err != nil {
		return nil, err
	}
	if result.ID == "" || result.ClientSecret == "" {
		return nil, fmt.Errorf("Stripe PaymentIntent 返回数据不完整")
	}
	return &stripePaymentIntentResult{
		id:           result.ID,
		clientSecret: result.ClientSecret,
	}, nil
}

func GetChargedAmount(count float64, user model.User) float64 {
	topUpGroupRatio := common.GetTopupGroupRatio(user.Group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}

	return count * topUpGroupRatio
}

func getStripePayMoney(amount float64, group string) float64 {
	originalAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	// Using float64 for monetary calculations is acceptable here due to the small amounts involved
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	payMoney := amount * setting.StripeUnitPrice * topupGroupRatio * discount
	return payMoney
}

func getStripePaymentIntentPayMoney(amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromFloat(common.QuotaPerUnit))
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}

	return dAmount.
		Mul(decimal.NewFromFloat(setting.StripePaymentIntentUnitPrice)).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount)).
		InexactFloat64()
}

func stripePaymentIntentAmount(payMoney float64, currency string) int64 {
	dAmount := decimal.NewFromFloat(payMoney)
	return dAmount.Mul(decimal.NewFromFloat(stripeCurrencyMultiplier(currency))).Round(0).IntPart()
}

func stripeCurrencyMultiplier(currency string) float64 {
	switch strings.ToLower(strings.TrimSpace(currency)) {
	case "bif", "clp", "djf", "gnf", "jpy", "kmf", "krw", "mga", "pyg", "rwf", "ugx", "vnd", "vuv", "xaf", "xof", "xpf":
		return 1
	default:
		return 100
	}
}

func getStripeMinTopup() int64 {
	minTopup := setting.StripeMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}

func getStripePaymentIntentMinTopup() int64 {
	minTopup := setting.StripePaymentIntentMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
