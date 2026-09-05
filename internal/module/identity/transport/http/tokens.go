package identityhttp

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListTokens(c *gin.Context) {
	page := common.GetPageQuery(c)
	rows, total, err := h.identity.ListTokens(c.Request.Context(), c.GetInt("id"), page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		tokenError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(rows)
	common.ApiSuccess(c, page)
}

func (h *Handler) SearchTokens(c *gin.Context) {
	page := common.GetPageQuery(c)
	rows, total, err := h.identity.SearchTokens(c.Request.Context(), c.GetInt("id"), c.Query("keyword"), c.Query("token"), page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		tokenError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(rows)
	common.ApiSuccess(c, page)
}

func (h *Handler) GetToken(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	row, err := h.identity.GetToken(c.Request.Context(), id, c.GetInt("id"))
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, row)
}

func (h *Handler) TokenKey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	key, err := h.identity.TokenKey(c.Request.Context(), id, c.GetInt("id"))
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"key": key})
}

func (h *Handler) TokenKeys(c *gin.Context) {
	var request contract.TokenBatch
	if err := c.ShouldBindJSON(&request); err != nil || len(request.Ids) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	keys, err := h.identity.TokenKeys(c.Request.Context(), request.Ids, c.GetInt("id"))
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"keys": keys})
}

func (h *Handler) TokenAutoGroups(c *gin.Context) {
	options, err := h.identity.TokenAutoGroupOptions(c.Request.Context(), tokenActor(c))
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, options)
}

func (h *Handler) CreateToken(c *gin.Context) {
	var request contract.TokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.identity.CreateToken(c.Request.Context(), tokenActor(c), request); err != nil {
		tokenError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) UpdateToken(c *gin.Context) {
	var request contract.TokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	row, err := h.identity.UpdateToken(c.Request.Context(), tokenActor(c), request, c.Query("status_only") != "")
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, row)
}

func (h *Handler) DeleteToken(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := h.identity.DeleteToken(c.Request.Context(), id, c.GetInt("id")); err != nil {
		tokenError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) DeleteTokens(c *gin.Context) {
	var request contract.TokenBatch
	if err := c.ShouldBindJSON(&request); err != nil || len(request.Ids) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	count, err := h.identity.DeleteTokens(c.Request.Context(), request.Ids, c.GetInt("id"))
	if err != nil {
		tokenError(c, err)
		return
	}
	common.ApiSuccess(c, count)
}

func tokenActor(c *gin.Context) contract.TokenActor {
	group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if group == "" {
		group = c.GetString("group")
	}
	return contract.TokenActor{ID: c.GetInt("id"), Group: group}
}

func tokenError(c *gin.Context, err error) {
	var validation *identity.TokenValidationError
	if !errors.As(err, &validation) {
		common.ApiError(c, err)
		return
	}
	var key string
	switch validation.Code {
	case "invalid_params":
		key = i18n.MsgInvalidParams
	case "batch_too_many":
		key = i18n.MsgBatchTooMany
	case "name_too_long":
		key = i18n.MsgTokenNameTooLong
	case "quota_negative":
		key = i18n.MsgTokenQuotaNegative
	case "quota_exceed_max":
		key = i18n.MsgTokenQuotaExceedMax
	case "generate_failed":
		key = i18n.MsgTokenGenerateFailed
	case "expired_cannot_enable":
		key = i18n.MsgTokenExpiredCannotEnable
	case "exhausted_cannot_enable":
		key = i18n.MsgTokenExhaustedCannotEable
	case "auto_groups_too_many":
		key = i18n.MsgTokenAutoGroupsTooMany
	case "auto_groups_duplicate":
		key = i18n.MsgTokenAutoGroupsDuplicate
	case "auto_groups_invalid":
		key = i18n.MsgTokenAutoGroupsInvalid
	default:
		common.ApiError(c, err)
		return
	}
	common.ApiErrorI18n(c, key, validation.Details)
}
