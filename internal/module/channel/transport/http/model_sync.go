package channelhttp

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SyncUpstreamModels(c *gin.Context) {
	var request contract.ModelSyncRequest
	// The synchronization endpoint accepts an empty body.
	_ = c.ShouldBindJSON(&request)
	c.JSON(http.StatusOK, h.channel.SyncUpstreamModels(c.Request.Context(), request))
}

func (h *Handler) SyncUpstreamPreview(c *gin.Context) {
	c.JSON(http.StatusOK, h.channel.SyncUpstreamPreview(c.Request.Context(), c.Query("locale")))
}

func (h *Handler) GetMissingModels(c *gin.Context) {
	missing, err := h.channel.MissingModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": missing})
}
