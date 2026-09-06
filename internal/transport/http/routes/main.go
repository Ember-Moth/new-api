package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/usage"

	"github.com/QuantumNous/new-api/internal/module/system"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"

	"github.com/QuantumNous/new-api/internal/module/identity/authz"

	"github.com/QuantumNous/new-api/internal/module/billing"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/channel"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	"github.com/QuantumNous/new-api/internal/transport/http/controller"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"

	"github.com/gin-gonic/gin"
)

type Dependencies struct {
	ControlPlane      bool
	Usage             *usage.Service
	SystemHooks       systemhttp.ManagementHooks
	System            *system.Service
	Authorization     *authz.Engine
	IdentityHooks     identityhttp.ManagementHooks
	Billing           *billing.Service
	BillingHooks      billinghttp.ManagementHooks
	Subscription      *subscription.Service
	SubscriptionHooks subscriptionhttp.ManagementHooks
	Identity          *identity.Service
	Channel           *channel.Service
	ChannelHooks      channelhttp.ManagementHooks
}

func SetRouter(router *gin.Engine, assets WebAssets, deps Dependencies) {
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	if !deps.ControlPlane {
		SetRelayRouter(router)
		SetTaskPluginProtocolRouter(router)
		SetVideoRouter(router)
		SetTaskRouter(router)
		router.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if path == "/api" || strings.HasPrefix(path, "/api/") || path == "/dashboard" || strings.HasPrefix(path, "/dashboard/") || strings.HasPrefix(path, "/v1/dashboard/") {
				controller.RelayNotFound(c)
				c.Abort()
				return
			}
			c.Next()
		}, SetPluginRouter(router), controller.RelayNotFound)
		return
	}

	SetApiRouter(router, deps)
	SetDashboardRouter(router, deps)
	// Compile plugin routing on the management node for validation and preview,
	// but never expose its forwarding handlers on this HTTP listener.
	SetPluginRouter(router)
	pluginDispatcher := func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			controller.RelayNotFound(c)
			c.Abort()
			return
		}
		c.Next()
	}
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets, pluginDispatcher)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(
			pluginDispatcher,
			middleware.RouteTag("web"),
			func(c *gin.Context) {
				c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
			},
		)
	}
}
