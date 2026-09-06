package app

import (
	"strings"

	"github.com/QuantumNous/new-api/internal/app/channelprovider"

	"github.com/QuantumNous/new-api/internal/infra/httpclient"

	"context"

	"github.com/QuantumNous/new-api/i18n"
	_ "github.com/QuantumNous/new-api/internal/config/setting/performance_setting"
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/oauth"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/joho/godotenv"
)

func initResources() error {
	err := godotenv.Load(".env")
	if err != nil {
		if common.DebugEnabled {
			common.SysLog("No .env file found, using default environment variables. If needed, please create a .env file and set the relevant variables.")
		}
	}

	// 加载环境变量
	common.InitEnv()

	logger.SetupLogger()

	// Initialize model settings
	ratio_setting.InitRatioSettings()

	httpclient.InitHttpClient()

	service.InitTokenEncoders()

	// Initialize SQL Database
	err = model.InitDB()
	if err != nil {
		common.FatalLog("failed to initialize database: " + err.Error())
		return err
	}
	channelDeps := model.ChannelDependencies()
	channelDeps.Pricing = channelPricing{}
	channelDeps.Providers = channelprovider.Adapter{}
	channelDeps.DisableChannel = service.DisableChannel
	channelDeps.NotifyModelUpdate = service.NotifyUpstreamModelUpdateWatchers
	model.ConfigureChannelService(channel.New(channelDeps))
	if common.IsControlPlane && common.PasswordLoginEncryptionEnabled {
		if err = model.InitPasswordEncryption(); err != nil {
			common.FatalLog("failed to initialize password encryption: " + err.Error())
			return err
		}
	}

	if common.IsControlPlane {
		model.CheckSetup()
	}

	// Initialize options, should after model.InitDB()

	options := system.NewOptions(system.OptionDependencies{DB: model.DB, InvalidatePricing: model.InvalidatePricingCache,
		ValidateTaskURL: service.ValidateTaskArtifactBaseURL,
		AliasPlugin: func(generation *jsplugin.RoutingGeneration, name string) (string, bool) {
			target, ok := model.ChannelService().ResolveTaskModelAlias(generation, name)
			return target.PluginKey, ok
		},
	})
	model.ConfigureOptions(options)
	if err := options.Initialize(context.Background()); err != nil {
		return err
	}

	// 清理旧的磁盘缓存文件
	common.CleanupOldCacheFiles()

	// Initialize SQL Database
	err = model.InitLogDB()
	if err != nil {
		return err
	}

	// Initialize Redis
	err = common.InitRedisClient()
	if err != nil {
		return err
	}

	// 启动系统监控
	common.StartSystemMonitor()

	// Initialize i18n
	err = i18n.Init()
	if err != nil {
		common.SysError("failed to initialize i18n: " + err.Error())
		// Don't return error, i18n is not critical
	} else {
		common.SysLog("i18n initialized with languages: " + strings.Join(i18n.SupportedLanguages(), ", "))
	}
	// Register user language loader for lazy loading
	i18n.SetUserLangLoader(model.GetUserLanguage)

	// Load custom OAuth providers from database
	err = oauth.LoadCustomProviders()
	if err != nil {
		common.SysError("failed to load custom OAuth providers: " + err.Error())
		// Don't return error, custom OAuth is not critical
	}

	service.StartAuthArtifactCleanup()

	return nil
}
