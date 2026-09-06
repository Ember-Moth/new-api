package billinghttp

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetCheckinStatus(c *gin.Context) {
	status, err := h.billing.GetCheckinStatus(c.Request.Context(), c.GetInt("id"), c.Query("month"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, status)
}
func (h *Handler) DoCheckin(c *gin.Context) {
	result, err := h.billing.Checkin(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "签到成功", "data": result})
}
func (h *Handler) TransferAffQuota(c *gin.Context) {
	if err := h.billing.RequirePaymentCompliance(); err != nil {
		common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		return
	}
	var request contract.TransferAffQuotaRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.billing.TransferAffiliate(c.Request.Context(), c.GetInt("id"), request.Quota); err != nil {
		if errors.Is(err, billing.ErrPaymentComplianceRequired) {
			common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
			return
		}
		common.ApiErrorI18n(c, i18n.MsgUserTransferFailed, map[string]any{"Error": err.Error()})
		return
	}
	common.ApiSuccessI18n(c, i18n.MsgUserTransferSuccess, nil)
}
