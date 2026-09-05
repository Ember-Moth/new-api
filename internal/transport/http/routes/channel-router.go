package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

type permissionRoute struct {
	method     string
	path       string
	permission authz.Permission
	handler    gin.HandlerFunc
}

func registerChannelRoutes(apiRouter *gin.RouterGroup, handler *channelhttp.Handler) {
	channelRoute := apiRouter.Group("/channel")
	channelRoute.Use(middleware.AdminAuth())

	channelRoute.POST("/:id/key",
		middleware.RootAuth(),
		middleware.CriticalRateLimit(),
		middleware.DisableCache(),
		middleware.SecureVerificationRequired(),
		handler.GetChannelKey,
	)

	for _, route := range channelPermissionRoutes(handler) {
		channelRoute.Handle(route.method, route.path,
			middleware.RequirePermission(route.permission),
			route.handler,
		)
	}
}

func channelPermissionRoutes(handler *channelhttp.Handler) []permissionRoute {
	return []permissionRoute{
		{method: http.MethodGet, path: "/", permission: authz.ChannelRead, handler: handler.GetAllChannels},
		{method: http.MethodGet, path: "/search", permission: authz.ChannelRead, handler: handler.SearchChannels},
		{method: http.MethodGet, path: "/models", permission: authz.ChannelRead, handler: controller.ChannelListModels},
		{method: http.MethodGet, path: "/models_enabled", permission: authz.ChannelRead, handler: controller.EnabledListModels},
		{method: http.MethodGet, path: "/ops", permission: authz.ChannelRead, handler: handler.GetChannelOps},
		{method: http.MethodGet, path: "/:id", permission: authz.ChannelRead, handler: handler.GetChannel},
		{method: http.MethodGet, path: "/test", permission: authz.ChannelOperate, handler: controller.TestAllChannels},
		{method: http.MethodGet, path: "/test/:id", permission: authz.ChannelOperate, handler: controller.TestChannel},
		{method: http.MethodGet, path: "/update_balance", permission: authz.ChannelOperate, handler: handler.UpdateAllChannelsBalance},
		{method: http.MethodGet, path: "/update_balance/:id", permission: authz.ChannelOperate, handler: handler.UpdateChannelBalance},
		{method: http.MethodPost, path: "/", permission: authz.ChannelSensitiveWrite, handler: handler.AddChannel},
		{method: http.MethodPut, path: "/", permission: authz.ChannelWrite, handler: handler.UpdateChannel},
		{method: http.MethodPost, path: "/status/batch", permission: authz.ChannelOperate, handler: handler.BatchUpdateChannelStatus},
		{method: http.MethodPost, path: "/:id/status", permission: authz.ChannelOperate, handler: handler.UpdateChannelStatus},
		{method: http.MethodDelete, path: "/disabled", permission: authz.ChannelSensitiveWrite, handler: handler.DeleteDisabledChannel},
		{method: http.MethodPost, path: "/tag/disabled", permission: authz.ChannelOperate, handler: handler.DisableTagChannels},
		{method: http.MethodPost, path: "/tag/enabled", permission: authz.ChannelOperate, handler: handler.EnableTagChannels},
		{method: http.MethodPut, path: "/tag", permission: authz.ChannelWrite, handler: handler.EditTagChannels},
		{method: http.MethodDelete, path: "/:id", permission: authz.ChannelSensitiveWrite, handler: handler.DeleteChannel},
		{method: http.MethodPost, path: "/batch", permission: authz.ChannelSensitiveWrite, handler: handler.DeleteChannelBatch},
		{method: http.MethodPost, path: "/fix", permission: authz.ChannelOperate, handler: handler.FixChannelsAbilities},
		{method: http.MethodGet, path: "/fetch_models/:id", permission: authz.ChannelOperate, handler: handler.FetchUpstreamModels},
		{method: http.MethodPost, path: "/fetch_models", permission: authz.ChannelSensitiveWrite, handler: handler.FetchModels},
		{method: http.MethodPost, path: "/:id/codex/refresh", permission: authz.ChannelSensitiveWrite, handler: handler.RefreshCodexChannelCredential},
		{method: http.MethodGet, path: "/:id/codex/usage", permission: authz.ChannelRead, handler: controller.GetCodexChannelUsage},
		{method: http.MethodGet, path: "/:id/codex/usage/reset-credits", permission: authz.ChannelRead, handler: controller.GetCodexChannelRateLimitResetCredits},
		{method: http.MethodPost, path: "/:id/codex/usage/reset", permission: authz.ChannelOperate, handler: controller.ResetCodexChannelUsage},
		{method: http.MethodPost, path: "/ollama/pull", permission: authz.ChannelSensitiveWrite, handler: handler.OllamaPullModel},
		{method: http.MethodPost, path: "/ollama/pull/stream", permission: authz.ChannelSensitiveWrite, handler: handler.OllamaPullModelStream},
		{method: http.MethodDelete, path: "/ollama/delete", permission: authz.ChannelSensitiveWrite, handler: handler.OllamaDeleteModel},
		{method: http.MethodGet, path: "/ollama/version/:id", permission: authz.ChannelSensitiveWrite, handler: handler.OllamaVersion},
		{method: http.MethodPost, path: "/batch/tag", permission: authz.ChannelWrite, handler: handler.BatchSetChannelTag},
		{method: http.MethodGet, path: "/tag/models", permission: authz.ChannelRead, handler: handler.GetTagModels},
		{method: http.MethodPost, path: "/copy/:id", permission: authz.ChannelSensitiveWrite, handler: handler.CopyChannel},
		{method: http.MethodPost, path: "/multi_key/manage", permission: authz.ChannelOperate, handler: handler.ManageMultiKeys},
		{method: http.MethodPost, path: "/upstream_updates/apply", permission: authz.ChannelWrite, handler: handler.ApplyChannelUpstreamModelUpdates},
		{method: http.MethodPost, path: "/upstream_updates/apply_all", permission: authz.ChannelWrite, handler: handler.ApplyAllChannelUpstreamModelUpdates},
		{method: http.MethodPost, path: "/upstream_updates/detect", permission: authz.ChannelOperate, handler: handler.DetectChannelUpstreamModelUpdates},
		{method: http.MethodPost, path: "/upstream_updates/detect_all", permission: authz.ChannelOperate, handler: handler.DetectAllChannelUpstreamModelUpdates},
	}

}
