package identityhttp

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/gin-gonic/gin"
)

type ManagementHooks struct {
	WriteRefreshCookie   func(*gin.Context, string)
	ClearRefreshCookie   func(*gin.Context)
	RequireSecurityProof func(*gin.Context, string, []string) bool
	PasskeyLogin         func(*gin.Context, *entity.User)
	SecurityAudit        func(*gin.Context, int, string, map[string]any)
	SessionIdentity      func(*gin.Context) (contract.AuthIdentity, bool)
	Audit                func(*gin.Context, int, string, map[string]any)
}

func (h *Handler) ListUsers(c *gin.Context)   { h.listUsers(c, false) }
func (h *Handler) SearchUsers(c *gin.Context) { h.listUsers(c, true) }
func (h *Handler) listUsers(c *gin.Context, search bool) {
	page := common.GetPageQuery(c)
	filter := contract.UserFilter{Search: search, Keyword: c.Query("keyword"), Group: c.Query("group"), SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order"), Offset: page.GetStartIdx(), Limit: page.GetPageSize()}
	if role, err := strconv.Atoi(c.Query("role")); err == nil {
		filter.Role = &role
	}
	if status, err := strconv.Atoi(c.Query("status")); err == nil {
		filter.Status = &status
	}
	users, total, err := h.identity.ListUsers(c.Request.Context(), filter)
	if err != nil {
		userError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(users)
	common.ApiSuccess(c, page)
}
func (h *Handler) GetUser(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	user, err := h.identity.GetUser(c.Request.Context(), userActor(c), id)
	if err != nil {
		userError(c, err)
		return
	}
	common.ApiSuccess(c, user)
}
func (h *Handler) CreateUser(c *gin.Context) {
	var input contract.UserRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	result, err := h.identity.CreateUser(c.Request.Context(), userActor(c), input)
	if err != nil {
		userError(c, err)
		return
	}
	h.userMutation(c, result, "")
}
func (h *Handler) UpdateUser(c *gin.Context) {
	var input contract.UserRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	result, err := h.identity.UpdateUser(c.Request.Context(), userActor(c), input)
	if err != nil {
		userError(c, err)
		return
	}
	h.userMutation(c, result, "")
}
func (h *Handler) ClearUserBinding(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	result, err := h.identity.ClearUserBinding(c.Request.Context(), userActor(c), id, c.Param("binding_type"))
	if err != nil {
		userError(c, err)
		return
	}
	h.userMutation(c, result, "success")
}
func (h *Handler) DeleteUser(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := h.identity.DeleteUser(c.Request.Context(), userActor(c), id)
	if err != nil {
		userError(c, err)
		return
	}
	h.userMutation(c, result, "")
}
func (h *Handler) ManageUser(c *gin.Context) {
	var input contract.ManageUserRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	result, err := h.identity.ManageUser(c.Request.Context(), userActor(c), input)
	if err != nil {
		userError(c, err)
		return
	}
	h.userMutation(c, result, "")
}

func userActor(c *gin.Context) contract.UserActor {
	return contract.UserActor{ID: c.GetInt("id"), Role: c.GetInt("role")}
}
func (h *Handler) userMutation(c *gin.Context, result *contract.UserMutation, message string) {
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, result.Audit.TargetID, result.Audit.Action, result.Audit.Parameters)
	}
	response := gin.H{"success": true, "message": message}
	if result.Data != nil {
		response["data"] = result.Data
	}
	c.JSON(http.StatusOK, response)
}

func userError(c *gin.Context, err error) {
	if errors.Is(err, identity.ErrPasswordUnset) {
		common.ApiErrorI18n(c, i18n.MsgUserPasswordUnset)
		return
	}
	if errors.Is(err, identity.ErrOriginalPassword) {
		common.ApiErrorI18n(c, i18n.MsgUserOriginalPasswordError)
		return
	}
	var validation *identity.UserValidationError
	if !errors.As(err, &validation) {
		common.ApiError(c, err)
		return
	}
	var key string
	switch validation.Code {
	case "invalid_params":
		key = i18n.MsgInvalidParams
	case "input_invalid":
		key = i18n.MsgUserInputInvalid
	case "same_level":
		key = i18n.MsgUserNoPermissionSameLevel
	case "higher_level":
		key = i18n.MsgUserNoPermissionHigherLevel
	case "create_higher_level":
		key = i18n.MsgUserCannotCreateHigherLevel
	case "not_exists":
		key = i18n.MsgUserNotExists
	case "disable_root":
		key = i18n.MsgUserCannotDisableRootUser
	case "delete_root":
		key = i18n.MsgUserCannotDeleteRootUser
	case "demote_root":
		key = i18n.MsgUserCannotDemoteRootUser
	case "cannot_promote":
		key = i18n.MsgUserAdminCannotPromote
	case "already_admin":
		key = i18n.MsgUserAlreadyAdmin
	case "already_common":
		key = i18n.MsgUserAlreadyCommon
	case "quota_change_zero":
		key = i18n.MsgUserQuotaChangeZero
	case "generate_failed":
		key = i18n.MsgGenerateFailed
	case "uuid_duplicate":
		key = i18n.MsgUuidDuplicate
	case "invalid_input":
		key = i18n.MsgInvalidInput
	case "verification_code":
		key = i18n.MsgUserVerificationCodeError
	case "email_taken":
		key = i18n.MsgUserEmailAlreadyTaken
	case "update_failed":
		key = i18n.MsgUpdateFailed
	case "SettingInvalidType":
		key = i18n.MsgSettingInvalidType
	case "QuotaThresholdGtZero":
		key = i18n.MsgQuotaThresholdGtZero
	case "SettingWebhookEmpty":
		key = i18n.MsgSettingWebhookEmpty
	case "SettingWebhookInvalid":
		key = i18n.MsgSettingWebhookInvalid
	case "SettingEmailInvalid":
		key = i18n.MsgSettingEmailInvalid
	case "SettingBarkUrlEmpty":
		key = i18n.MsgSettingBarkUrlEmpty
	case "SettingBarkUrlInvalid":
		key = i18n.MsgSettingBarkUrlInvalid
	case "SettingUrlMustHttp":
		key = i18n.MsgSettingUrlMustHttp
	case "SettingGotifyUrlEmpty":
		key = i18n.MsgSettingGotifyUrlEmpty
	case "SettingGotifyTokenEmpty":
		key = i18n.MsgSettingGotifyTokenEmpty
	case "SettingGotifyUrlInvalid":
		key = i18n.MsgSettingGotifyUrlInvalid
	default:
		common.ApiError(c, err)
		return
	}
	common.ApiErrorI18n(c, key, validation.Details)
}
