package billinghttp

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetTopUpInfo(c *gin.Context) { common.ApiSuccess(c, h.billing.Purchases.Info()) }
func (h *Handler) RequestAmount(c *gin.Context) {
	var request contract.WalletPayRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	quote, err := h.billing.Purchases.Quote(c.Request.Context(), c.GetInt("id"), request.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	if quote.Money <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(quote.Money, 'f', 2, 64)})
}
func (h *Handler) RequestEpay(c *gin.Context) {
	var request contract.WalletPayRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	result, err := h.billing.Purchases.StartEpay(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": result.EpayParams, "url": result.EpayURL})
}

func (h *Handler) EpayNotify(c *gin.Context) {
	if !h.billing.Webhooks.Enabled("epay") {
		c.String(http.StatusOK, "fail")
		return
	}
	values := c.Request.URL.Query()
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err != nil {
			c.String(http.StatusOK, "fail")
			return
		}
		values = c.Request.PostForm
	}
	params := make(map[string]string, len(values))
	for key := range values {
		params[key] = values.Get(key)
	}
	if err := h.billing.Webhooks.Epay(c.Request.Context(), params, c.ClientIP()); err != nil {
		c.String(http.StatusOK, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}
