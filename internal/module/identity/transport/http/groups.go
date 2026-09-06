package identityhttp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": h.identity.GroupNames()})
}
func (h *Handler) GetUserGroups(c *gin.Context) {
	groups, err := h.identity.UserGroupChoices(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": groups})
}
