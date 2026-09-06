package identityhttp

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/gin-gonic/gin"
)

func (h *Handler) OAuthBindings(c *gin.Context) {
	rows, err := h.identity.OAuthBindings(c.Request.Context(), userActor(c), 0, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}
func (h *Handler) AdminOAuthBindings(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}
	rows, err := h.identity.OAuthBindings(c.Request.Context(), userActor(c), id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}
func (h *Handler) UnbindOAuth(c *gin.Context) {
	if c.GetInt("id") == 0 {
		common.ApiErrorMsg(c, "未登录")
		return
	}
	provider, err := strconv.Atoi(c.Param("provider_id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的提供商 ID")
		return
	}
	if err := h.identity.UnbindOAuth(c.Request.Context(), userActor(c), 0, provider, false); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "解绑成功"})
}
func (h *Handler) AdminUnbindOAuth(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}
	provider, err := strconv.Atoi(c.Param("provider_id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid provider id")
		return
	}
	if err := h.identity.UnbindOAuth(c.Request.Context(), userActor(c), id, provider, true); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "success"})
}
