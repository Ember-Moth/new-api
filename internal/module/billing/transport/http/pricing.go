package billinghttp

import (
	"net/http"

	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetPricing(c *gin.Context) {
	view, err := h.billing.Pricing.View(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}
func (h *Handler) ResetModelRatio(c *gin.Context) {
	if err := h.billing.Pricing.ResetModelRatio(c.Request.Context()); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "重置模型倍率成功"})
}
func (h *Handler) GetRatioConfig(c *gin.Context) {
	if !ratio_setting.IsExposeRatioEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "倍率配置接口未启用"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": ratio_setting.GetExposedData()})
}
