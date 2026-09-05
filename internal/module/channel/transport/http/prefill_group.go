package channelhttp

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	channel *channel.Service
	hooks   ManagementHooks
}

// ManagementHooks supplies request-scoped authorization and audit integration.
// Catalog handlers do not need these hooks; sensitive-field permission checks fail closed
// when no authorization evaluator has been supplied.
type ManagementHooks struct {
	Can                func(userID, role int, resource, action string) bool
	Audit              func(*gin.Context, string, map[string]any)
	EnqueueModelUpdate func() (TaskSubmission, error)
}

type TaskSubmission struct {
	TaskID  string
	Status  string
	Type    string
	Created bool
}

func New(service *channel.Service, hooks ManagementHooks) *Handler {
	return &Handler{channel: service, hooks: hooks}
}

func (h *Handler) can(userID, role int, resource, action string) bool {
	return h.hooks.Can != nil && h.hooks.Can(userID, role, resource, action)
}

func (h *Handler) recordAudit(c *gin.Context, action string, details map[string]any) {
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, action, details)
	}
}

func (h *Handler) GetPrefillGroups(c *gin.Context) {
	groups, err := h.channel.ListPrefillGroups(c.Request.Context(), c.Query("type"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, groups)
}

func (h *Handler) CreatePrefillGroup(c *gin.Context) {
	var group contract.PrefillGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.CreatePrefillGroup(c.Request.Context(), &group); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &group)
}

func (h *Handler) UpdatePrefillGroup(c *gin.Context) {
	var group contract.PrefillGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.UpdatePrefillGroup(c.Request.Context(), &group); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &group)
}

func (h *Handler) DeletePrefillGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.DeletePrefillGroup(c.Request.Context(), id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
