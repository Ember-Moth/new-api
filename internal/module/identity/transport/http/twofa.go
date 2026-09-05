package identityhttp

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SetupTwoFA(c *gin.Context) {
	setup, err := h.identity.SetupTwoFA(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "2FA设置初始化成功，请使用认证器扫描二维码并输入验证码完成设置", "data": setup})
}

func (h *Handler) TwoFAStatus(c *gin.Context) {
	status, err := h.identity.TwoFAStatus(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, status)
}

func (h *Handler) EnableTwoFA(c *gin.Context) {
	var request contract.TwoFACodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	bundle, err := h.identity.EnableTwoFA(c.Request.Context(), c.GetInt("id"), request.Code, h.twoFASession(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "两步验证启用成功", "data": bundle})
}

func (h *Handler) DisableTwoFA(c *gin.Context) {
	var request contract.TwoFACodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	bundle, err := h.identity.DisableTwoFA(c.Request.Context(), c.GetInt("id"), request.Code, h.twoFASession(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "两步验证已禁用", "data": bundle})
}

func (h *Handler) RegenerateTwoFABackupCodes(c *gin.Context) {
	var request contract.TwoFACodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	result, err := h.identity.RegenerateTwoFABackupCodes(c.Request.Context(), c.GetInt("id"), request.Code, h.twoFASession(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "备用码重新生成成功", "data": result})
}

func (h *Handler) TwoFAStats(c *gin.Context) {
	stats, err := h.identity.TwoFAStats(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, stats)
}

func (h *Handler) AdminDisableTwoFA(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "用户ID格式错误")
		return
	}
	if err := h.identity.AdminDisableTwoFA(c.Request.Context(), userActor(c), userID); err != nil {
		common.ApiError(c, err)
		return
	}
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, userID, "user.2fa_disable", nil)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "用户2FA已被强制禁用"})
}

func (h *Handler) twoFASession(c *gin.Context) *contract.AuthIdentity {
	if h.hooks.SessionIdentity != nil {
		if session, ok := h.hooks.SessionIdentity(c); ok {
			return &session
		}
	}
	return nil
}
