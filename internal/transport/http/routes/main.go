package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/channel"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"

	"github.com/gin-gonic/gin"
)

type Dependencies struct {
	IdentityHooks identityhttp.ManagementHooks
	Billing       *billing.Service
	BillingHooks  billinghttp.ManagementHooks
	Subscription  *subscription.Service
	Identity      *identity.Service
	Channel       *channel.Service
	ChannelHooks  channelhttp.ManagementHooks
}

func SetRouter(router *gin.Engine, assets WebAssets, deps Dependencies) {
	SetApiRouter(router, deps)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetTaskPluginProtocolRouter(router)
	SetVideoRouter(router)
	SetTaskRouter(router)
	pluginDispatcher := SetPluginRouter(router)
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
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
