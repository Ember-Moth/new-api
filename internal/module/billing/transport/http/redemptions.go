package billinghttp

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/gin-gonic/gin"
)

type ManagementHooks struct {
	Audit func(*gin.Context, string, map[string]any)
}

type Handler struct {
	redeemMu  sync.Mutex
	redeeming map[int]struct{}
	billing   *billing.Service
	hooks     ManagementHooks
}

func New(service *billing.Service, hooks ManagementHooks) *Handler {
	return &Handler{billing: service, hooks: hooks, redeeming: make(map[int]struct{})}
}

func (h *Handler) ListRedemptions(c *gin.Context) { h.listRedemptions(c, "", "") }

func (h *Handler) SearchRedemptions(c *gin.Context) {
	h.listRedemptions(c, c.Query("keyword"), c.Query("status"))
}

func (h *Handler) listRedemptions(c *gin.Context, keyword, status string) {
	page := common.GetPageQuery(c)
	rows, total, err := h.billing.ListRedemptions(c.Request.Context(), keyword, status, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		redemptionError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(rows)
	common.ApiSuccess(c, page)
}

func (h *Handler) GetRedemption(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	row, err := h.billing.GetRedemption(c.Request.Context(), id)
	if err != nil {
		redemptionError(c, err)
		return
	}
	common.ApiSuccess(c, row)
}

func (h *Handler) CreateRedemptions(c *gin.Context) {
	if err := h.billing.RequirePaymentCompliance(); err != nil {
		redemptionError(c, err)
		return
	}
	var request contract.CreateRedemptionsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	keys, err := h.billing.CreateRedemptions(c.Request.Context(), c.GetInt("id"), request)
	if errors.Is(err, billing.ErrRedemptionCreateFailed) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": i18n.T(c, i18n.MsgRedemptionCreateFailed), "data": keys})
		return
	}
	if err != nil {
		redemptionError(c, err)
		return
	}
	if h.hooks.Audit != nil {
		h.hooks.Audit(c, "redemption.create", map[string]any{"name": request.Name, "count": request.Count, "quota": logger.LogQuota(request.Quota)})
	}
	common.ApiSuccess(c, keys)
}

func (h *Handler) UpdateRedemption(c *gin.Context) {
	var request contract.UpdateRedemptionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	row, err := h.billing.UpdateRedemption(c.Request.Context(), request, c.Query("status_only") != "")
	if err != nil {
		redemptionError(c, err)
		return
	}
	common.ApiSuccess(c, row)
}

func (h *Handler) DeleteRedemption(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := h.billing.DeleteRedemption(c.Request.Context(), id); err != nil {
		redemptionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) DeleteInvalidRedemptions(c *gin.Context) {
	count, err := h.billing.DeleteInvalidRedemptions(c.Request.Context())
	if err != nil {
		redemptionError(c, err)
		return
	}
	common.ApiSuccess(c, count)
}

func redemptionError(c *gin.Context, err error) {
	var key string
	switch {
	case errors.Is(err, billing.ErrPaymentComplianceRequired):
		key = i18n.MsgPaymentComplianceRequired
	case errors.Is(err, billing.ErrRedemptionNameLength):
		key = i18n.MsgRedemptionNameLength
	case errors.Is(err, billing.ErrRedemptionCountPositive):
		key = i18n.MsgRedemptionCountPositive
	case errors.Is(err, billing.ErrRedemptionCountMax):
		key = i18n.MsgRedemptionCountMax
	case errors.Is(err, billing.ErrRedemptionExpired):
		key = i18n.MsgRedemptionExpireTimeInvalid
	}
	if key != "" {
		common.ApiErrorI18n(c, key)
		return
	}
	common.ApiError(c, err)
}
