package billinghttp

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/pricesync"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetSyncableChannels(c *gin.Context) {
	sources, err := h.billing.PriceSync.Sources(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sources})
}
func (h *Handler) FetchUpstreamRatios(c *gin.Context) {
	var request contract.UpstreamRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.SysError("failed to bind upstream request: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请求参数格式错误"})
		return
	}
	report, err := h.billing.PriceSync.Fetch(c.Request.Context(), request)
	if err != nil {
		var input *pricesync.InputError
		status := http.StatusInternalServerError
		if errors.Is(err, pricesync.ErrNoUpstreams) {
			status = http.StatusOK
		} else if errors.As(err, &input) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": report})
}
