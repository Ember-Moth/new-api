package billinghttp

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ConfirmPaymentCompliance(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "This operation requires dashboard session authentication. API access token is not allowed."})
		return
	}
	var request struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if !request.Confirmed {
		common.ApiErrorMsg(c, "请确认合规声明")
		return
	}
	confirmation, err := h.billing.PaymentConfig.ConfirmCompliance(c.Request.Context(), c.GetInt("id"), c.ClientIP())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("payment compliance confirmed user_id=%d ip=%s terms_version=%s confirmed_at=%d", confirmation.ConfirmedBy, c.ClientIP(), confirmation.TermsVersion, confirmation.ConfirmedAt))
	common.ApiSuccess(c, confirmation)
}
