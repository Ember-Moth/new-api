package channelhttp

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

func (h *Handler) UpdateChannelBalance(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	channel, err := h.channel.CacheGetChannel(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if channel.Type == constant.ChannelTypeTaskPlugin {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Task Plugin channels do not support balance queries"})
		return
	}
	if channel.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "多密钥渠道不支持余额查询",
		})
		return
	}
	result, err := h.channel.RefreshBalance(c.Request.Context(), channel)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	response := gin.H{
		"success": true,
		"message": "",
	}
	if result.RawResponse == "" {
		response["balance"] = result.Balance
	} else {
		response["raw_response"] = result.RawResponse
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateAllChannelsBalance(c *gin.Context) {
	// TODO: make it async
	err := h.channel.RefreshAllBalances(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}
