package subscriptionhttp

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/gin-gonic/gin"
)

type ManagementHooks struct {
	Audit     func(*gin.Context, int, string, map[string]any)
	ResetLogs func(*gin.Context, *contract.SubscriptionResetResult)
}
type Handler struct {
	subscriptions *subscription.Service
	hooks         ManagementHooks
}

func New(service *subscription.Service, hooks ...ManagementHooks) *Handler {
	h := &Handler{subscriptions: service}
	if len(hooks) > 0 {
		h.hooks = hooks[0]
	}
	return h
}

func (h *Handler) ListPlans(c *gin.Context) {
	plans, err := h.subscriptions.ListPlans(c.Request.Context(), true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, plans)
}

func (h *Handler) AdminListPlans(c *gin.Context) {
	plans, err := h.subscriptions.ListPlans(c.Request.Context(), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, plans)
}

func (h *Handler) CreatePlan(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}
	var request contract.UpsertPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	plan, err := h.subscriptions.CreatePlan(c.Request.Context(), request.Plan)
	if err != nil {
		planError(c, err)
		return
	}
	common.ApiSuccess(c, plan)
}

func (h *Handler) UpdatePlan(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var request contract.UpsertPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if err := h.subscriptions.UpdatePlan(c.Request.Context(), id, request.Plan); err != nil {
		planError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func (h *Handler) UpdatePlanStatus(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var request contract.UpdatePlanStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if err := h.subscriptions.UpdatePlanStatus(c.Request.Context(), id, *request.Enabled); err != nil {
		planError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func planError(c *gin.Context, err error) {
	if errors.Is(err, subscription.ErrPaymentComplianceRequired) {
		common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		return
	}
	common.ApiError(c, err)
}
