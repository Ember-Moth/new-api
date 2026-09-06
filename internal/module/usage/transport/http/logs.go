package usagehttp

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/internal/module/usage"
	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	cursor, err := h.service.NewLogCursorPage(c.Query("cursor"), fmt.Sprintf("admin:%d:%d", c.GetInt("id"), c.GetInt("role")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, _, err := h.service.GetAllLogs(c.Request.Context(), logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId, cursor)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if c.GetInt("role") < common.RoleRootUser {
		usage.FormatAdminLogs(logs)
	} else {
		usage.FormatRootLogs(logs)
	}
	common.ApiSuccess(c, gin.H{"items": logs, "page_size": pageInfo.GetPageSize(), "next_cursor": cursor.NextCursor, "has_more": cursor.HasMore})
	return
}

func (h *Handler) GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	cursor, err := h.service.NewLogCursorPage(c.Query("cursor"), fmt.Sprintf("self:%d:%d", c.GetInt("id"), c.GetInt("role")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, _, err := h.service.GetUserLogs(c.Request.Context(), userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId, cursor)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": logs, "page_size": pageInfo.GetPageSize(), "next_cursor": cursor.NextCursor, "has_more": cursor.HasMore})
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func (h *Handler) SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func (h *Handler) SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func (h *Handler) GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := h.service.GetLogByTokenId(c.Request.Context(), tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func (h *Handler) GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := h.service.SumUsedQuota(c.Request.Context(), logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := h.service.SumUsedToken(c.Request.Context(), logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func (h *Handler) GetLogsSelfStat(c *gin.Context) {
	userID := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	quotaNum, err := h.service.SumUsedQuota(c.Request.Context(), logType, startTimestamp, endTimestamp, modelName, "", tokenName, channel, group, userID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := h.service.SumUsedToken(c.Request.Context(), logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}

type Handler struct{ service *usage.Service }

func New(service *usage.Service) *Handler { return &Handler{service: service} }
