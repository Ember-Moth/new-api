package systemhttp

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/gin-gonic/gin"
)

type ManagementHooks struct {
	Audit func(*gin.Context, string, map[string]any)
}

func (h *Handler) GetOptions(c *gin.Context) {
	values, err := h.service.GetOptions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, values)
}
func (h *Handler) UpdateOption(c *gin.Context) {
	var request contract.OptionUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	if err := h.service.UpdateManagedOption(c.Request.Context(), request); err != nil {
		if errors.Is(err, system.ErrPaymentComplianceRequired) {
			common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		} else {
			common.ApiError(c, err)
		}
		return
	}
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, "option.update", map[string]any{"key": request.Key})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
