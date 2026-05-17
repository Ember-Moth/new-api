package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine) {
	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetVideoRouter(router)

	setFrontendFallback(router)
}

func setFrontendFallback(router *gin.Engine) {
	frontendBaseUrl := strings.TrimSuffix(os.Getenv("FRONTEND_BASE_URL"), "/")
	router.NoRoute(func(c *gin.Context) {
		if frontendBaseUrl != "" && !isBackendPath(c.Request.URL.Path) {
			c.Set(middleware.RouteTagKey, "frontend_redirect")
			c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
			return
		}

		c.Set(middleware.RouteTagKey, "not_found")
		controller.RelayNotFound(c)
	})
}

func isBackendPath(path string) bool {
	switch {
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		return true
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/v1beta" || strings.HasPrefix(path, "/v1beta/"):
		return true
	case path == "/pg" || strings.HasPrefix(path, "/pg/"):
		return true
	case path == "/mj" || strings.HasPrefix(path, "/mj/"):
		return true
	case path == "/suno" || strings.HasPrefix(path, "/suno/"):
		return true
	case path == "/kling" || strings.HasPrefix(path, "/kling/"):
		return true
	case path == "/jimeng" || strings.HasPrefix(path, "/jimeng/"):
		return true
	case path == "/dashboard/billing" || strings.HasPrefix(path, "/dashboard/billing/"):
		return true
	}
	return false
}
