package subscriptionhttp

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SubscriptionRequestStripePay(c *gin.Context) { h.startCheckout(c, "stripe") }
func (h *Handler) SubscriptionRequestCreemPay(c *gin.Context)  { h.startCheckout(c, "creem") }
func (h *Handler) SubscriptionRequestWaffoPancakePay(c *gin.Context) {
	h.startCheckout(c, "waffo_pancake")
}
func (h *Handler) SubscriptionRequestEpay(c *gin.Context) { h.startCheckout(c, "epay") }

func (h *Handler) startCheckout(c *gin.Context, provider string) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}
	var input contract.CheckoutInput
	if err := c.ShouldBindJSON(&input); err != nil || input.PlanId <= 0 {
		if provider == "creem" {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		} else {
			common.ApiErrorMsg(c, "参数错误")
		}
		return
	}
	result, err := h.subscriptions.StartCheckout(c.Request.Context(), c.GetInt("id"), provider, input)
	if err != nil {
		var checkoutErr *subscription.CheckoutError
		if provider != "epay" && errors.As(err, &checkoutErr) {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		} else {
			planError(c, err)
		}
		return
	}
	switch provider {
	case "epay":
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": result.EpayParams, "url": result.EpayURL})
	case "stripe":
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"pay_link": result.PayLink}})
	case "creem":
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"checkout_url": result.CheckoutURL, "order_id": result.OrderID}})
	case "waffo_pancake":
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"checkout_url": result.CheckoutURL, "session_id": result.SessionID, "expires_at": result.ExpiresAt, "order_id": result.OrderID, "token": result.Token, "token_expires_at": result.TokenExpiresAt}})
	}
}

func (h *Handler) SubscriptionEpayNotify(c *gin.Context) {
	params, err := epayCallbackParams(c)
	if err != nil || len(params) == 0 {
		c.String(http.StatusOK, "fail")
		return
	}
	verified, err := h.subscriptions.Gateways.VerifyEpay(params)
	if err != nil || !verified.Paid {
		c.String(http.StatusOK, "fail")
		return
	}
	if err := h.subscriptions.Payments.Complete(c.Request.Context(), verified.TradeNo, verified.Payload, "epay", verified.PaymentMethod); err != nil {
		c.String(http.StatusOK, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}

func (h *Handler) SubscriptionEpayReturn(c *gin.Context) {
	suffix := "/wallet?pay=fail"
	params, err := epayCallbackParams(c)
	if err == nil && len(params) > 0 {
		verified, verifyErr := h.subscriptions.Gateways.VerifyEpay(params)
		if verifyErr == nil {
			suffix = "/wallet?pay=pending"
			if verified.Paid {
				suffix = "/wallet?pay=fail"
				if err := h.subscriptions.Payments.Complete(c.Request.Context(), verified.TradeNo, verified.Payload, "epay", verified.PaymentMethod); err == nil {
					suffix = "/wallet?pay=success"
				}
			}
		}
	}
	c.Redirect(http.StatusFound, h.subscriptions.Gateways.ReturnURL(suffix))
}

func epayCallbackParams(c *gin.Context) (map[string]string, error) {
	values := c.Request.URL.Query()
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err != nil {
			return nil, err
		}
		values = c.Request.PostForm
	}
	params := make(map[string]string, len(values))
	for key := range values {
		params[key] = values.Get(key)
	}
	return params, nil
}
