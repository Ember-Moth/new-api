package identityhttp

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/gin-gonic/gin"
)

type Handler struct{ identity *identity.Service }

func New(service *identity.Service) *Handler { return &Handler{identity: service} }

func (h *Handler) DiscoverProvider(c *gin.Context) {
	var request contract.FetchCustomOAuthDiscoveryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}
	discovery, err := h.identity.DiscoverProvider(c.Request.Context(), request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": discovery})
}

func (h *Handler) ListProviders(c *gin.Context) {
	providers, err := h.identity.ListProviders(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": providers})
}

func (h *Handler) GetProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}
	provider, err := h.identity.GetProvider(c.Request.Context(), id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": provider})
}

func (h *Handler) CreateProvider(c *gin.Context) {
	var request contract.CreateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}
	provider, err := h.identity.CreateProvider(c.Request.Context(), request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "创建成功", "data": provider})
}

func (h *Handler) UpdateProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}
	var request contract.UpdateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}
	provider, err := h.identity.UpdateProvider(c.Request.Context(), id, request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "更新成功", "data": provider})
}

func (h *Handler) DeleteProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}
	if err := h.identity.DeleteProvider(c.Request.Context(), id); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "删除成功"})
}
