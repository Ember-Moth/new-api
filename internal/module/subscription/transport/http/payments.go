package subscriptionhttp

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SubscriptionRequestBalancePay(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}

	userId := c.GetInt("id")
	var req contract.SubscriptionBalancePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	if err := h.subscriptions.Payments.PurchaseWithBalance(c.Request.Context(), userId, req.PlanId); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
