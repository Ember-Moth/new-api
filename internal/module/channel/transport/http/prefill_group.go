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
}

func New(service *channel.Service) *Handler {
	return &Handler{channel: service}
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
