package billinghttp

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetSubscription(c *gin.Context) {
	result, err := h.billing.DashboardSubscription(c.Request.Context(), c.GetInt("id"), c.GetInt("token_id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": types.OpenAIError{Message: err.Error(), Type: "upstream_error"}})
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) GetUsage(c *gin.Context) {
	result, err := h.billing.DashboardUsage(c.Request.Context(), c.GetInt("id"), c.GetInt("token_id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": types.OpenAIError{Message: err.Error(), Type: "new_api_error"}})
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) GetTokenUsage(c *gin.Context) {
	header := c.GetHeader("Authorization")
	if header == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "No Authorization header"})
		return
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid Bearer token"})
		return
	}
	report, err := h.billing.TokenUsage(c.Request.Context(), strings.TrimPrefix(parts[1], "sk-"))
	if err != nil {
		common.SysError("failed to get token by key: " + err.Error())
		common.ApiErrorI18n(c, i18n.MsgTokenGetInfoFailed)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": true, "message": "ok", "data": report})
}
