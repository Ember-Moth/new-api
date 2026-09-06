package router

import (
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

func SetDashboardRouter(router *gin.Engine, deps Dependencies) {
	handler := billinghttp.New(deps.Billing, deps.BillingHooks)
	apiRouter := router.Group("/")
	apiRouter.Use(middleware.RouteTag("old_api"))
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	apiRouter.Use(middleware.CORS())
	apiRouter.Use(middleware.TokenAuth())
	{
		apiRouter.GET("/dashboard/billing/subscription", handler.GetSubscription)
		apiRouter.GET("/v1/dashboard/billing/subscription", handler.GetSubscription)
		apiRouter.GET("/dashboard/billing/usage", handler.GetUsage)
		apiRouter.GET("/v1/dashboard/billing/usage", handler.GetUsage)
	}
}
