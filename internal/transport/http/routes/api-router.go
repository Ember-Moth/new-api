package router

import (
	"github.com/QuantumNous/new-api/controller"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity/authz"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"

	// Import oauth package to register providers via init()
	_ "github.com/QuantumNous/new-api/oauth"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

func SetApiRouter(router *gin.Engine, deps Dependencies) {
	usageHandler := usagehttp.New(deps.Usage)
	systemHandler := systemhttp.New(deps.System, deps.SystemHooks)
	billingHandler := billinghttp.New(deps.Billing, deps.BillingHooks)
	subscriptionHandler := subscriptionhttp.New(deps.Subscription, deps.SubscriptionHooks)
	channelHandler := channelhttp.New(deps.Channel, deps.ChannelHooks)
	identityHandler := identityhttp.New(deps.Identity, deps.IdentityHooks)
	apiRouter := router.Group("/api")
	apiRouter.Use(middleware.Authorization(deps.Authorization))
	apiRouter.Use(middleware.SystemTasks(deps.System))
	apiRouter.Use(middleware.RouteTag("api"))
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.BodyStorageCleanup()) // 清理请求体存储
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	anonymousRequestBodyLimit := middleware.AnonymousRequestBodyLimit()
	{
		apiRouter.GET("/setup", controller.GetSetup)
		apiRouter.POST("/setup", anonymousRequestBodyLimit, controller.PostSetup)
		apiRouter.GET("/status", controller.GetStatus)
		apiRouter.GET("/uptime/status", controller.GetUptimeKumaStatus)
		apiRouter.GET("/models", middleware.UserAuth(), controller.DashboardListModels)
		apiRouter.GET("/status/test", middleware.AdminAuth(), controller.TestStatus)
		apiRouter.GET("/notice", controller.GetNotice)
		apiRouter.GET("/user-agreement", controller.GetUserAgreement)
		apiRouter.GET("/privacy-policy", controller.GetPrivacyPolicy)
		apiRouter.GET("/about", controller.GetAbout)
		//apiRouter.GET("/midjourney", controller.GetMidjourney)
		apiRouter.GET("/home_page_content", controller.GetHomePageContent)
		apiRouter.GET("/pricing", middleware.HeaderNavModuleAuth("pricing"), controller.GetPricing)
		perfMetricsRoute := apiRouter.Group("/perf-metrics")
		perfMetricsRoute.Use(middleware.HeaderNavModulePublicOrUserAuth("pricing"))
		{
			perfMetricsRoute.GET("/summary", usageHandler.GetPerfMetricsSummary)
			perfMetricsRoute.GET("", usageHandler.GetPerfMetrics)
		}
		apiRouter.GET("/rankings", middleware.HeaderNavModuleAuth("rankings"), usageHandler.GetRankings)
		apiRouter.GET("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification)
		apiRouter.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail)
		apiRouter.POST("/user/reset", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.ResetPassword)
		// OAuth routes - specific routes must come before :provider wildcard
		apiRouter.POST("/oauth/state", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), anonymousRequestBodyLimit, controller.GenerateOAuthCode)
		apiRouter.POST("/oauth/email/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), identityHandler.BindEmail)
		// Non-standard OAuth (WeChat, Telegram) - keep original routes
		apiRouter.GET("/oauth/wechat", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.WeChatAuth)
		apiRouter.POST("/oauth/wechat/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), controller.WeChatBind)
		apiRouter.GET("/oauth/telegram/login", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramLogin)
		apiRouter.POST("/oauth/telegram/bind/start", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramBindStart)
		apiRouter.GET("/oauth/telegram/bind/:flow_token", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramBind)
		// Standard OAuth providers (GitHub, Discord, OIDC, LinuxDO) - unified route
		apiRouter.GET("/oauth/:provider", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), controller.HandleOAuth)
		apiRouter.GET("/ratio_config", middleware.CriticalRateLimit(), controller.GetRatioConfig)

		apiRouter.POST("/stripe/webhook", anonymousRequestBodyLimit, controller.StripeWebhook)
		apiRouter.POST("/creem/webhook", anonymousRequestBodyLimit, controller.CreemWebhook)
		apiRouter.POST("/waffo/webhook", anonymousRequestBodyLimit, controller.WaffoWebhook)
		// :env separates test vs prod URLs so the operator can register each
		// in Pancake's matching webhook slot; handler enforces env match.
		apiRouter.POST("/waffo-pancake/webhook/:env", anonymousRequestBodyLimit, controller.WaffoPancakeWebhook)

		// Universal secure verification routes
		apiRouter.POST("/verify", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.UniversalVerify)

		userRoute := apiRouter.Group("/user")
		{
			userRoute.POST("/auth/refresh", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), middleware.DisableCache(), identityHandler.RefreshAuth)
			userRoute.POST("/auth/logout", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), middleware.DisableCache(), identityHandler.AuthLogout)
			userRoute.POST("/register", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, middleware.TurnstileCheck(), controller.Register)
			userRoute.GET("/login/encryption-key", middleware.DisableCache(), controller.GetPasswordEncryptionKey)
			userRoute.POST("/login", middleware.CriticalRateLimit(), middleware.DisableCache(), anonymousRequestBodyLimit, middleware.TurnstileCheck(), controller.Login)
			userRoute.POST("/login/2fa", middleware.CriticalRateLimit(), middleware.DisableCache(), anonymousRequestBodyLimit, controller.Verify2FALogin)
			userRoute.POST("/passkey/login/begin", middleware.CriticalRateLimit(), middleware.DisableCache(), anonymousRequestBodyLimit, identityHandler.PasskeyLoginBegin)
			userRoute.POST("/passkey/login/finish", middleware.CriticalRateLimit(), middleware.DisableCache(), anonymousRequestBodyLimit, identityHandler.PasskeyLoginFinish)
			//userRoute.POST("/tokenlog", middleware.CriticalRateLimit(), controller.TokenLog)
			userRoute.POST("/epay/notify", anonymousRequestBodyLimit, controller.EpayNotify)
			userRoute.GET("/epay/notify", controller.EpayNotify)
			userRoute.GET("/groups", controller.GetUserGroups)

			selfRoute := userRoute.Group("/")
			selfRoute.Use(middleware.UserAuth())
			{
				selfRoute.GET("/sessions", middleware.DisableCache(), identityHandler.GetLoginSessions)
				selfRoute.DELETE("/sessions/:sid", middleware.DisableCache(), identityHandler.DeleteLoginSession)
				selfRoute.POST("/sessions/revoke-others", middleware.DisableCache(), identityHandler.RevokeOtherLoginSessions)
				selfRoute.GET("/self/groups", controller.GetUserGroups)
				selfRoute.GET("/self", identityHandler.Self)
				selfRoute.GET("/models", controller.GetUserModels)
				selfRoute.PUT("/self", middleware.CriticalRateLimit(), middleware.DisableCache(), identityHandler.UpdateSelf)
				selfRoute.DELETE("/self", identityHandler.DeleteSelf)
				selfRoute.GET("/token", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("access-token"), middleware.DisableCache(), identityHandler.RotatePersonalAccessToken)
				selfRoute.GET("/passkey", identityHandler.PasskeyStatus)
				selfRoute.POST("/passkey/register/begin", middleware.DisableCache(), identityHandler.PasskeyRegisterBegin)
				selfRoute.POST("/passkey/register/finish", middleware.DisableCache(), identityHandler.PasskeyRegisterFinish)
				selfRoute.POST("/passkey/verify/begin", middleware.DisableCache(), identityHandler.PasskeyVerifyBegin)
				selfRoute.POST("/passkey/verify/finish", middleware.DisableCache(), identityHandler.PasskeyVerifyFinish)
				selfRoute.DELETE("/passkey", middleware.DisableCache(), identityHandler.PasskeyDelete)
				selfRoute.GET("/aff", identityHandler.AffiliationCode)
				selfRoute.GET("/topup/info", controller.GetTopUpInfo)
				selfRoute.GET("/topup/self", controller.GetUserTopUps)
				selfRoute.POST("/topup", middleware.CriticalRateLimit(), controller.TopUp)
				selfRoute.POST("/pay", middleware.CriticalRateLimit(), controller.RequestEpay)
				selfRoute.POST("/amount", controller.RequestAmount)
				selfRoute.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.RequestStripePay)
				selfRoute.POST("/stripe/amount", controller.RequestStripeAmount)
				selfRoute.POST("/creem/pay", middleware.CriticalRateLimit(), controller.RequestCreemPay)
				selfRoute.POST("/waffo/amount", controller.RequestWaffoAmount)
				selfRoute.POST("/waffo/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPay)
				selfRoute.POST("/waffo-pancake/amount", controller.RequestWaffoPancakeAmount)
				selfRoute.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPancakePay)
				selfRoute.POST("/aff_transfer", middleware.UserCriticalRateLimit("aff-transfer"), controller.TransferAffQuota)
				selfRoute.PUT("/setting", identityHandler.UpdateNotificationSettings)

				// 2FA routes
				selfRoute.GET("/2fa/status", identityHandler.TwoFAStatus)
				selfRoute.POST("/2fa/setup", middleware.DisableCache(), identityHandler.SetupTwoFA)
				selfRoute.POST("/2fa/enable", middleware.DisableCache(), identityHandler.EnableTwoFA)
				selfRoute.POST("/2fa/disable", middleware.DisableCache(), identityHandler.DisableTwoFA)
				selfRoute.POST("/2fa/backup_codes", middleware.DisableCache(), identityHandler.RegenerateTwoFABackupCodes)

				// Check-in routes
				selfRoute.GET("/checkin", controller.GetCheckinStatus)
				selfRoute.POST("/checkin", middleware.TurnstileCheck(), controller.DoCheckin)

				// Custom OAuth bindings
				selfRoute.GET("/oauth/bindings", identityHandler.OAuthBindings)
				selfRoute.DELETE("/oauth/bindings/:provider_id", identityHandler.UnbindOAuth)
			}

			adminRoute := userRoute.Group("/")
			adminRoute.Use(middleware.AdminAuth())
			{
				adminRoute.GET("/", identityHandler.ListUsers)
				adminRoute.GET("/topup", controller.GetAllTopUps)
				adminRoute.POST("/topup/complete", controller.AdminCompleteTopUp)
				adminRoute.GET("/search", identityHandler.SearchUsers)
				adminRoute.GET("/:id/oauth/bindings", identityHandler.AdminOAuthBindings)
				adminRoute.DELETE("/:id/oauth/bindings/:provider_id", identityHandler.AdminUnbindOAuth)
				adminRoute.DELETE("/:id/bindings/:binding_type", identityHandler.ClearUserBinding)
				adminRoute.GET("/:id", identityHandler.GetUser)
				adminRoute.POST("/", identityHandler.CreateUser)
				adminRoute.POST("/manage", identityHandler.ManageUser)
				adminRoute.PUT("/", identityHandler.UpdateUser)
				adminRoute.DELETE("/:id", identityHandler.DeleteUser)
				adminRoute.DELETE("/:id/reset_passkey", identityHandler.AdminResetPasskey)

				// Admin 2FA routes
				adminRoute.GET("/2fa/stats", identityHandler.TwoFAStats)
				adminRoute.DELETE("/:id/2fa", identityHandler.AdminDisableTwoFA)
			}
		}

		// Subscription billing (plans, purchase, admin management)
		subscriptionRoute := apiRouter.Group("/subscription")
		subscriptionRoute.Use(middleware.UserAuth())
		{
			subscriptionRoute.GET("/plans", subscriptionHandler.ListPlans)
			subscriptionRoute.GET("/self", subscriptionHandler.GetSubscriptionSelf)
			subscriptionRoute.PUT("/self/preference", identityHandler.UpdateBillingPreference)
			subscriptionRoute.POST("/balance/pay", middleware.CriticalRateLimit(), subscriptionHandler.SubscriptionRequestBalancePay)
			subscriptionRoute.POST("/epay/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestEpay)
			subscriptionRoute.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestStripePay)
			subscriptionRoute.POST("/creem/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestCreemPay)
			subscriptionRoute.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestWaffoPancakePay)
		}
		subscriptionAdminRoute := apiRouter.Group("/subscription/admin")
		subscriptionAdminRoute.Use(middleware.AdminAuth())
		{
			subscriptionAdminRoute.GET("/plans", subscriptionHandler.AdminListPlans)
			subscriptionAdminRoute.POST("/plans", subscriptionHandler.CreatePlan)
			subscriptionAdminRoute.PUT("/plans/:id", subscriptionHandler.UpdatePlan)
			subscriptionAdminRoute.PATCH("/plans/:id", subscriptionHandler.UpdatePlanStatus)
			subscriptionAdminRoute.POST("/bind", subscriptionHandler.AdminBindSubscription)
			subscriptionAdminRoute.POST("/plans/:id/subscriptions/reset", subscriptionHandler.AdminResetPlanSubscriptions)

			// User subscription management (admin)
			subscriptionAdminRoute.GET("/users/:id/subscriptions", subscriptionHandler.AdminListUserSubscriptions)
			subscriptionAdminRoute.POST("/users/:id/subscriptions", subscriptionHandler.AdminCreateUserSubscription)
			subscriptionAdminRoute.POST("/users/:id/subscriptions/reset", subscriptionHandler.AdminResetUserSubscriptionsByPlan)
			subscriptionAdminRoute.POST("/user_subscriptions/:id/invalidate", subscriptionHandler.AdminInvalidateUserSubscription)
			subscriptionAdminRoute.DELETE("/user_subscriptions/:id", subscriptionHandler.AdminDeleteUserSubscription)
		}

		// Subscription payment callbacks (no auth)
		apiRouter.POST("/subscription/epay/notify", anonymousRequestBodyLimit, controller.SubscriptionEpayNotify)
		apiRouter.GET("/subscription/epay/notify", controller.SubscriptionEpayNotify)
		apiRouter.GET("/subscription/epay/return", controller.SubscriptionEpayReturn)
		apiRouter.POST("/subscription/epay/return", anonymousRequestBodyLimit, controller.SubscriptionEpayReturn)
		optionRoute := apiRouter.Group("/option")
		optionRoute.Use(middleware.RootAuth())
		{
			optionRoute.GET("/", systemHandler.GetOptions)
			optionRoute.PUT("/", systemHandler.UpdateOption)
			optionRoute.POST("/payment_compliance", controller.ConfirmPaymentCompliance)
			optionRoute.GET("/channel_affinity_cache", controller.GetChannelAffinityCacheStats)
			optionRoute.DELETE("/channel_affinity_cache", controller.ClearChannelAffinityCache)
			optionRoute.POST("/rest_model_ratio", controller.ResetModelRatio)
			optionRoute.GET("/waffo-pancake/catalog", controller.ListWaffoPancakeCatalog)
			optionRoute.POST("/waffo-pancake/pair", controller.CreateWaffoPancakePair)
			optionRoute.POST("/waffo-pancake/save", controller.SaveWaffoPancake)
			optionRoute.POST("/waffo-pancake/subscription-product", controller.CreateWaffoPancakeSubscriptionProduct)
			optionRoute.GET("/waffo-pancake/subscription-product-options", controller.ListWaffoPancakeSubscriptionProductOptions)
		}

		// Custom OAuth provider management (root only)
		customOAuthRoute := apiRouter.Group("/custom-oauth-provider")
		customOAuthRoute.Use(middleware.RootAuth())
		{
			customOAuthRoute.POST("/discovery", identityHandler.DiscoverProvider)
			customOAuthRoute.GET("/", identityHandler.ListProviders)
			customOAuthRoute.GET("/:id", identityHandler.GetProvider)
			customOAuthRoute.POST("/", identityHandler.CreateProvider)
			customOAuthRoute.PUT("/:id", identityHandler.UpdateProvider)
			customOAuthRoute.DELETE("/:id", identityHandler.DeleteProvider)
		}
		performanceRoute := apiRouter.Group("/performance")
		performanceRoute.Use(middleware.RootAuth())
		{
			performanceRoute.GET("/stats", controller.GetPerformanceStats)
			performanceRoute.DELETE("/disk_cache", controller.ClearDiskCache)
			performanceRoute.POST("/reset_stats", controller.ResetPerformanceStats)
			performanceRoute.POST("/gc", controller.ForceGC)
			performanceRoute.GET("/logs", controller.GetLogFiles)
			performanceRoute.DELETE("/logs", controller.CleanupLogFiles)
		}
		ratioSyncRoute := apiRouter.Group("/ratio_sync")
		ratioSyncRoute.Use(middleware.RootAuth())
		{
			ratioSyncRoute.GET("/channels", controller.GetSyncableChannels)
			ratioSyncRoute.POST("/fetch", controller.FetchUpstreamRatios)
		}
		taskPluginRoute := apiRouter.Group("/plugin/task")
		taskPluginRoute.Use(middleware.RootAuth())
		{
			taskPluginRoute.GET("", controller.ListTaskPlugins)
			taskPluginRoute.POST("", controller.UploadTaskPlugin)
			taskPluginRoute.PUT("", controller.UploadTaskPlugin)
			taskPluginRoute.GET("/runtime/status", controller.GetTaskPluginRuntime)
			taskPluginRoute.GET("/marketplace/sources", controller.GetTaskPluginMarketplaceSources)
			taskPluginRoute.PUT("/marketplace/sources", controller.UpdateTaskPluginMarketplaceSources)
			taskPluginRoute.GET("/:key", controller.GetTaskPlugin)
			taskPluginRoute.GET("/:key/versions", controller.GetTaskPluginVersions)
			taskPluginRoute.POST("/:key/activate", controller.ActivateTaskPlugin)
			taskPluginRoute.POST("/:key/status", controller.SetTaskPluginStatus)
			taskPluginRoute.POST("/:key/dryrun", controller.DryRunTaskPlugin)
			taskPluginRoute.DELETE("/:key/versions/:version", controller.DeleteTaskPluginVersion)
		}
		apiRouter.GET("/task_plugin_options", middleware.AdminAuth(), middleware.RequirePermission(authz.TaskPluginBind), controller.GetTaskPluginOptions)
		registerChannelRoutes(apiRouter, channelHandler)
		registerAuthzRoutes(apiRouter, identityHandler)
		tokenRoute := apiRouter.Group("/token")
		tokenRoute.Use(middleware.UserAuth())
		{
			tokenRoute.GET("/", identityHandler.ListTokens)
			tokenRoute.GET("/search", middleware.SearchRateLimit(), identityHandler.SearchTokens)
			tokenRoute.GET("/auto-groups", identityHandler.TokenAutoGroups)
			tokenRoute.GET("/:id", identityHandler.GetToken)
			tokenRoute.POST("/:id/key", middleware.CriticalRateLimit(), middleware.DisableCache(), identityHandler.TokenKey)
			tokenRoute.POST("/", identityHandler.CreateToken)
			tokenRoute.PUT("/", identityHandler.UpdateToken)
			tokenRoute.DELETE("/:id", identityHandler.DeleteToken)
			tokenRoute.POST("/batch", identityHandler.DeleteTokens)
			tokenRoute.POST("/batch/keys", middleware.CriticalRateLimit(), middleware.DisableCache(), identityHandler.TokenKeys)
		}

		usageRoute := apiRouter.Group("/usage")
		usageRoute.Use(middleware.CORS(), middleware.CriticalRateLimit())
		{
			tokenUsageRoute := usageRoute.Group("/token")
			tokenUsageRoute.Use(middleware.TokenAuthReadOnly())
			{
				tokenUsageRoute.GET("/", controller.GetTokenUsage)
			}
		}

		redemptionRoute := apiRouter.Group("/redemption")
		redemptionRoute.Use(middleware.AdminAuth())
		{
			redemptionRoute.GET("/", billingHandler.ListRedemptions)
			redemptionRoute.GET("/search", billingHandler.SearchRedemptions)
			redemptionRoute.GET("/:id", billingHandler.GetRedemption)
			redemptionRoute.POST("/", billingHandler.CreateRedemptions)
			redemptionRoute.PUT("/", billingHandler.UpdateRedemption)
			redemptionRoute.DELETE("/invalid", billingHandler.DeleteInvalidRedemptions)
			redemptionRoute.DELETE("/:id", billingHandler.DeleteRedemption)
		}
		logRoute := apiRouter.Group("/log")
		logRoute.GET("/", middleware.AdminAuth(), usageHandler.GetAllLogs)
		logRoute.GET("/stat", middleware.AdminAuth(), usageHandler.GetLogsStat)
		logRoute.GET("/self/stat", middleware.UserAuth(), usageHandler.GetLogsSelfStat)
		logRoute.GET("/channel_affinity_usage_cache", middleware.AdminAuth(), controller.GetChannelAffinityUsageCacheStats)
		logRoute.GET("/search", middleware.AdminAuth(), usageHandler.SearchAllLogs)
		logRoute.GET("/self", middleware.UserAuth(), usageHandler.GetUserLogs)
		logRoute.GET("/self/search", middleware.UserAuth(), middleware.SearchRateLimit(), usageHandler.SearchUserLogs)

		systemTaskRoute := apiRouter.Group("/system-task")
		systemTaskRoute.Use(middleware.RootAuth())
		{
			systemTaskRoute.POST("/log-cleanup", systemHandler.CreateLogCleanupSystemTask)
			systemTaskRoute.GET("/list", systemHandler.ListSystemTasks)
			systemTaskRoute.GET("/current", systemHandler.GetCurrentSystemTask)
			systemTaskRoute.GET("/:task_id", systemHandler.GetSystemTask)
		}
		systemInfoRoute := apiRouter.Group("/system-info")
		systemInfoRoute.Use(middleware.RootAuth())
		{
			systemInfoRoute.GET("/instances", systemHandler.ListSystemInstances)
			systemInfoRoute.DELETE("/stale-instances", systemHandler.DeleteStaleSystemInstances)
			systemInfoRoute.DELETE("/instances/:node_name", systemHandler.DeleteStaleSystemInstance)
		}

		dataRoute := apiRouter.Group("/data")
		dataRoute.GET("/", middleware.AdminAuth(), usageHandler.GetAllQuotaDates)
		dataRoute.GET("/users", middleware.AdminAuth(), usageHandler.GetQuotaDatesByUser)
		dataRoute.GET("/self", middleware.UserAuth(), usageHandler.GetUserQuotaDates)
		dataRoute.GET("/flow", middleware.AdminAuth(), usageHandler.GetAllFlowQuotaDates)
		dataRoute.GET("/flow/self", middleware.UserAuth(), usageHandler.GetUserFlowQuotaDates)

		logRoute.Use(middleware.CORS(), middleware.CriticalRateLimit())
		{
			logRoute.GET("/token", middleware.TokenAuthReadOnly(), usageHandler.GetLogByKey)
		}
		groupRoute := apiRouter.Group("/group")
		groupRoute.Use(middleware.AdminAuth())
		{
			groupRoute.GET("/", controller.GetGroups)
		}

		prefillGroupRoute := apiRouter.Group("/prefill_group")
		prefillGroupRoute.Use(middleware.AdminAuth())
		{
			prefillGroupRoute.GET("/", channelHandler.GetPrefillGroups)
			prefillGroupRoute.POST("/", channelHandler.CreatePrefillGroup)
			prefillGroupRoute.PUT("/", channelHandler.UpdatePrefillGroup)
			prefillGroupRoute.DELETE("/:id", channelHandler.DeletePrefillGroup)
		}

		mjRoute := apiRouter.Group("/mj")
		mjRoute.GET("/self", middleware.UserAuth(), controller.GetUserMidjourney)
		mjRoute.GET("/", middleware.AdminAuth(), controller.GetAllMidjourney)

		taskRoute := apiRouter.Group("/task")
		{
			taskRoute.GET("/self", middleware.UserAuth(), controller.GetUserTask)
			taskRoute.GET("", middleware.AdminAuth(), controller.GetAllTask)
			taskRoute.GET("/:task_id/artifacts", middleware.UserAuth(), controller.GetDashboardTaskArtifacts)
		}

		vendorRoute := apiRouter.Group("/vendors")
		vendorRoute.Use(middleware.AdminAuth())
		{
			vendorRoute.GET("/", channelHandler.GetAllVendors)
			vendorRoute.GET("/search", channelHandler.SearchVendors)
			vendorRoute.GET("/:id", channelHandler.GetVendorMeta)
			vendorRoute.POST("/", channelHandler.CreateVendorMeta)
			vendorRoute.PUT("/", channelHandler.UpdateVendorMeta)
			vendorRoute.DELETE("/:id", channelHandler.DeleteVendorMeta)
		}

		modelsRoute := apiRouter.Group("/models")
		modelsRoute.Use(middleware.AdminAuth())
		{
			modelsRoute.GET("/sync_upstream/preview", channelHandler.SyncUpstreamPreview)
			modelsRoute.POST("/sync_upstream", channelHandler.SyncUpstreamModels)
			modelsRoute.GET("/missing", channelHandler.GetMissingModels)
			modelsRoute.GET("/", channelHandler.GetAllModelsMeta)
			modelsRoute.GET("/search", channelHandler.SearchModelsMeta)
			modelsRoute.GET("/:id", channelHandler.GetModelMeta)
			modelsRoute.POST("/", channelHandler.CreateModelMeta)
			modelsRoute.PUT("/", channelHandler.UpdateModelMeta)
			modelsRoute.DELETE("/:id", channelHandler.DeleteModelMeta)
		}

		// Deployments (model deployment management)
		deploymentsRoute := apiRouter.Group("/deployments")
		deploymentsRoute.Use(middleware.AdminAuth())
		{
			deploymentsRoute.GET("/settings", controller.GetModelDeploymentSettings)
			deploymentsRoute.POST("/settings/test-connection", controller.TestIoNetConnection)
			deploymentsRoute.GET("/", controller.GetAllDeployments)
			deploymentsRoute.GET("/search", controller.SearchDeployments)
			deploymentsRoute.POST("/test-connection", controller.TestIoNetConnection)
			deploymentsRoute.GET("/hardware-types", controller.GetHardwareTypes)
			deploymentsRoute.GET("/locations", controller.GetLocations)
			deploymentsRoute.GET("/available-replicas", controller.GetAvailableReplicas)
			deploymentsRoute.POST("/price-estimation", controller.GetPriceEstimation)
			deploymentsRoute.GET("/check-name", controller.CheckClusterNameAvailability)
			deploymentsRoute.POST("/", controller.CreateDeployment)

			deploymentsRoute.GET("/:id", controller.GetDeployment)
			deploymentsRoute.GET("/:id/logs", controller.GetDeploymentLogs)
			deploymentsRoute.GET("/:id/containers", controller.ListDeploymentContainers)
			deploymentsRoute.GET("/:id/containers/:container_id", controller.GetContainerDetails)
			deploymentsRoute.PUT("/:id", controller.UpdateDeployment)
			deploymentsRoute.PUT("/:id/name", controller.UpdateDeploymentName)
			deploymentsRoute.POST("/:id/extend", controller.ExtendDeployment)
			deploymentsRoute.DELETE("/:id", controller.DeleteDeployment)
		}
	}
}
