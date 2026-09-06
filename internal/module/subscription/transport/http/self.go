package subscriptionhttp

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetSubscriptionSelf(c *gin.Context) {
	result, err := h.subscriptions.SelfSubscriptions(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}
