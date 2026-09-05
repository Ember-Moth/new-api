package identityhttp

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Self(c *gin.Context) {
	user, err := h.identity.Self(c.Request.Context(), userActor(c))
	if err != nil {
		userError(c, err)
		return
	}
	common.ApiSuccess(c, user)
}

func (h *Handler) RotatePersonalAccessToken(c *gin.Context) {
	key, err := h.identity.RotatePersonalAccessToken(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		userError(c, err)
		return
	}
	common.ApiSuccess(c, key)
}

func (h *Handler) AffiliationCode(c *gin.Context) {
	code, err := h.identity.AffiliationCode(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		userError(c, err)
		return
	}
	common.ApiSuccess(c, code)
}

func (h *Handler) UpdateSelf(c *gin.Context) {
	var input contract.SelfUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	var session *contract.AuthIdentity
	if h.hooks.SessionIdentity != nil {
		if identity, ok := h.hooks.SessionIdentity(c); ok {
			session = &identity
		}
	}
	bundle, err := h.identity.UpdateSelf(c.Request.Context(), c.GetInt("id"), input, session)
	if err != nil {
		userError(c, err)
		return
	}
	if input.Preference != "" {
		common.ApiSuccessI18n(c, i18n.MsgUpdateSuccess, nil)
		return
	}
	if bundle != nil {
		common.ApiSuccess(c, bundle)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) DeleteSelf(c *gin.Context) {
	if err := h.identity.DeleteSelf(c.Request.Context(), c.GetInt("id")); err != nil {
		userError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) BindEmail(c *gin.Context) {
	var input contract.BindEmailRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorMsg(c, "invalid request body")
		return
	}
	if c.GetInt("id") == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "not authenticated"})
		return
	}
	if err := h.identity.BindEmail(c.Request.Context(), c.GetInt("id"), input); err != nil {
		userError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) UpdateNotificationSettings(c *gin.Context) {
	var input contract.NotificationSettingsRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if err := h.identity.UpdateNotificationSettings(c.Request.Context(), c.GetInt("id"), input); err != nil {
		userError(c, err)
		return
	}
	common.ApiSuccessI18n(c, i18n.MsgSettingSaved, nil)
}
