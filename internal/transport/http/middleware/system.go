package middleware

import (
	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/gin-gonic/gin"
)

func SystemTasks(service *system.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(system.WithService(c.Request.Context(), service))
		c.Next()
	}
}
