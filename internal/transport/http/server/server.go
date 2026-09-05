package httpserver

import (
	"fmt"
	"net/http"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	router "github.com/QuantumNous/new-api/internal/transport/http/routes"
	"github.com/gin-gonic/gin"
)

// New builds the HTTP adapter with its routes, middleware, and frontend assets.
func New(assets router.WebAssets, deps router.Dependencies) (*gin.Engine, error) {
	if os.Getenv("GIN_MODE") != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}
	server := gin.New()
	if err := middleware.ConfigureTrustedProxies(server); err != nil {
		return nil, err
	}
	server.Use(gin.CustomRecovery(func(c *gin.Context, err any) {
		common.SysLog(fmt.Sprintf("panic detected: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Panic detected, error: %v. Please submit a issue here: https://github.com/Calcium-Ion/new-api", err),
				"type":    "new_api_panic",
			},
		})
	}))
	// This will cause SSE not to work!!!
	//server.Use(gzip.Gzip(gzip.DefaultCompression))
	server.Use(middleware.RequestId())
	server.Use(middleware.Version())
	server.Use(middleware.I18n())
	middleware.SetUpLogger(server)
	assets.IndexPage = injectUmamiAnalytics(assets.IndexPage)
	assets.IndexPage = injectGoogleAnalytics(assets.IndexPage)

	// 设置路由
	router.SetRouter(server, assets, deps)
	return server, nil
}
