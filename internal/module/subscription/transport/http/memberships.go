package subscriptionhttp

import (
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/gin-gonic/gin"
)

// ---- Admin APIs ----

func (h *Handler) AdminBindSubscription(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}

	var req contract.AdminBindSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	msg, err := h.subscriptions.Members.AdminBindSubscription(c.Request.Context(), req.UserId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// ---- Admin: user subscription management ----

func (h *Handler) AdminListUserSubscriptions(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	subs, err := h.subscriptions.Members.GetAllUserSubscriptions(c.Request.Context(), userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, subs)
}

func resolveAdvanceResetTime(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

// AdminCreateUserSubscription creates a new user subscription from a plan (no payment).
func (h *Handler) AdminCreateUserSubscription(c *gin.Context) {
	if err := h.subscriptions.RequirePaymentCompliance(); err != nil {
		planError(c, err)
		return
	}

	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req contract.AdminCreateUserSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	msg, err := h.subscriptions.Members.AdminBindSubscription(c.Request.Context(), userId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

func (h *Handler) AdminResetUserSubscriptionsByPlan(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req contract.AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	result, err := h.subscriptions.Members.AdminResetUserSubscriptionsByPlan(c.Request.Context(), userId, req.PlanId, advanceResetTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if h.hooks.ResetLogs != nil {
		h.hooks.ResetLogs(c, result)
	}
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, userId, "subscription.user_plan_reset", map[string]interface{}{
			"target_user_id":     userId,
			"plan_id":            result.PlanId,
			"plan_title":         result.PlanTitle,
			"reset_count":        result.ResetCount,
			"user_count":         result.UserCount,
			"advance_reset_time": result.AdvanceResetTime,
		})
	}
	common.ApiSuccess(c, result)
}

func (h *Handler) AdminResetPlanSubscriptions(c *gin.Context) {
	planId, _ := strconv.Atoi(c.Param("id"))
	if planId <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req contract.AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	result, err := h.subscriptions.Members.AdminResetPlanSubscriptions(c.Request.Context(), planId, advanceResetTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if h.hooks.ResetLogs != nil {
		h.hooks.ResetLogs(c, result)
	}
	common.SysLog(fmt.Sprintf("admin reset subscription plan %d quota: reset_count=%d user_count=%d advance_reset_time=%t",
		result.PlanId, result.ResetCount, result.UserCount, result.AdvanceResetTime))
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, c.GetInt("id"), "subscription.plan_reset", map[string]interface{}{
			"plan_id":            result.PlanId,
			"plan_title":         result.PlanTitle,
			"reset_count":        result.ResetCount,
			"user_count":         result.UserCount,
			"advance_reset_time": result.AdvanceResetTime,
		})
	}
	common.ApiSuccess(c, result)
}

// AdminInvalidateUserSubscription cancels a user subscription immediately.
func (h *Handler) AdminInvalidateUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	msg, err := h.subscriptions.Members.AdminInvalidateUserSubscription(c.Request.Context(), subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func (h *Handler) AdminDeleteUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	msg, err := h.subscriptions.Members.AdminDeleteUserSubscription(c.Request.Context(), subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}
