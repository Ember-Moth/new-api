package channelhttp

import (
	"strconv"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"

	"github.com/gin-gonic/gin"
)

// GetAllVendors 获取供应商列表（分页）
func (h *Handler) GetAllVendors(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	vendors, total, err := h.channel.ListVendors(c.Request.Context(), pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(vendors)
	common.ApiSuccess(c, pageInfo)
}

// SearchVendors 搜索供应商
func (h *Handler) SearchVendors(c *gin.Context) {
	keyword := c.Query("keyword")
	pageInfo := common.GetPageQuery(c)
	vendors, total, err := h.channel.SearchVendors(c.Request.Context(), keyword, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(vendors)
	common.ApiSuccess(c, pageInfo)
}

// GetVendorMeta 根据 ID 获取供应商
func (h *Handler) GetVendorMeta(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	v, err := h.channel.Vendor(c.Request.Context(), id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, v)
}

// CreateVendorMeta 新建供应商
func (h *Handler) CreateVendorMeta(c *gin.Context) {
	var v contract.Vendor
	if err := c.ShouldBindJSON(&v); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.CreateVendor(c.Request.Context(), &v); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &v)
}

// UpdateVendorMeta 更新供应商
func (h *Handler) UpdateVendorMeta(c *gin.Context) {
	var v contract.Vendor
	if err := c.ShouldBindJSON(&v); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.UpdateVendor(c.Request.Context(), &v); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &v)
}

// DeleteVendorMeta 删除供应商
func (h *Handler) DeleteVendorMeta(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := h.channel.DeleteVendor(c.Request.Context(), id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
