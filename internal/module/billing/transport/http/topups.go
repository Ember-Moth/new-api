package billinghttp

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetUserTopUps(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	topups, total, err := h.billing.TopUps.List(c.Request.Context(), contract.TopUpQuery{UserID: userId, Keyword: keyword, Offset: pageInfo.GetStartIdx(), Limit: pageInfo.GetPageSize()})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

// GetAllTopUps 管理员获取全平台充值记录
func (h *Handler) GetAllTopUps(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	topups, total, err := h.billing.TopUps.List(c.Request.Context(), contract.TopUpQuery{Admin: true, Keyword: keyword, Offset: pageInfo.GetStartIdx(), Limit: pageInfo.GetPageSize()})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

// AdminCompleteTopUp 管理员补单接口
func (h *Handler) AdminCompleteTopUp(c *gin.Context) {
	var req contract.AdminCompleteTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TradeNo == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	if _, err := h.billing.TopUps.Complete(c.Request.Context(), contract.TopUpCompletion{TradeNo: req.TradeNo, CallerIP: c.ClientIP(), Manual: true}); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func (h *Handler) Redeem(c *gin.Context) {
	if err := h.billing.RequirePaymentCompliance(); err != nil {
		common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		return
	}
	id := c.GetInt("id")
	h.redeemMu.Lock()
	_, busy := h.redeeming[id]
	if !busy {
		h.redeeming[id] = struct{}{}
	}
	h.redeemMu.Unlock()
	if busy {
		common.ApiErrorI18n(c, i18n.MsgUserTopUpProcessing)
		return
	}
	defer func() { h.redeemMu.Lock(); delete(h.redeeming, id); h.redeemMu.Unlock() }()
	var input contract.RedeemRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		common.ApiError(c, err)
		return
	}
	if input.Key != "" {
		common.RandomSleep()
	}
	quota, err := h.billing.TopUps.Redeem(c.Request.Context(), input.Key, id)
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgRedeemFailed)
		return
	}
	common.ApiSuccess(c, quota)
}
