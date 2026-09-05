package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/internal/module/system"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"
	"github.com/QuantumNous/new-api/internal/module/usage"

	_ "net/http/pprof"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/module/billing"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/authz"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	router "github.com/QuantumNous/new-api/internal/transport/http/routes"
	httpserver "github.com/QuantumNous/new-api/internal/transport/http/server"
	tasktransport "github.com/QuantumNous/new-api/internal/transport/task"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Run assembles application services and owns their process lifecycle.
func Run(assets router.WebAssets) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	startTime := time.Now()
	kitutil.SetLogging(common.SysLog, func(message string) {
		logger.LogError(nil, message)
	})
	kitutil.SetSystemErrorLogging(common.SysError)

	err := initResources()
	if err != nil {
		common.FatalLog("failed to initialize resources: " + err.Error())
		return
	}

	authorization, err := authz.New(model.DB, common.IsMasterNode)
	if err != nil {
		common.FatalLog("failed to initialize authorization: " + err.Error())
		return
	}

	common.SysLog("New API " + common.Version + " started")
	if common.DebugEnabled {
		common.SysLog("running in debug mode")
	}

	kitutil.Debug.Store(common.DebugEnabled)

	defer func() {
		cancelRun()
		err := model.CloseDB()
		if err != nil {
			common.FatalLog("failed to close database: " + err.Error())
		}
	}()

	if common.RedisEnabled {
		// for compatibility with old versions
		common.MemoryCacheEnabled = true
	}
	if common.MemoryCacheEnabled {
		common.SysLog("memory cache enabled")
		common.SysLog(fmt.Sprintf("sync frequency: %d seconds", common.SyncFrequency))

		// Add panic recovery and retry for InitChannelCache
		func() {
			defer func() {
				if r := recover(); r != nil {
					common.SysLog(fmt.Sprintf("InitChannelCache panic: %v, retrying once", r))
					// Retry once
					_, _, fixErr := model.FixAbility()
					if fixErr != nil {
						common.FatalLog(fmt.Sprintf("InitChannelCache failed: %s", fixErr.Error()))
					}
				}
			}()
			model.InitChannelCache()
		}()

		go model.SyncChannelCache(common.SyncFrequency)
	}

	// Warm pricing after channel cache initialization so Advanced Custom
	// endpoint inference can read cached route settings on first request.
	model.GetPricing()

	// 热更新配置
	go model.OptionManager().SyncOptions(runCtx, common.SyncFrequency)
	go controller.SyncTaskPlugins()

	// 周期性重载授权策略，保证多节点/多 master 部署下权限变更能传播到每个实例
	go authorization.StartPolicySync(runCtx, common.SyncFrequency)

	// 数据看板
	aggregates := model.QuotaDataStore()
	quotaDone := aggregates.Start(runCtx, func() time.Duration { return time.Duration(common.DataExportInterval) * time.Minute })
	defer func() { cancelRun(); <-quotaDone }()
	performance := model.PerformanceStore()
	performanceDone := performance.Start(runCtx)
	defer func() { cancelRun(); <-performanceDone }()

	if os.Getenv("CHANNEL_UPDATE_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_UPDATE_FREQUENCY"))
		if err != nil {
			common.FatalLog("failed to parse CHANNEL_UPDATE_FREQUENCY: " + err.Error())
		}
		go model.ChannelService().SyncBalances(runCtx, frequency)
	}

	// Codex credential auto-refresh check every 10 minutes, refresh when expires within 1 day
	service.StartCodexCredentialAutoRefreshTask()

	// Subscription quota reset task (daily/weekly/monthly/custom)
	subscriptionService := subscription.New(subscription.Dependencies{
		Payments:       model.SubscriptionPayments(),
		Members:        model.SubscriptionMemberships(),
		Quota:          model.SubscriptionQuota(),
		DB:             model.DB,
		PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed,
		GroupExists: func(group string) bool {
			_, ok := ratio_setting.GetGroupRatioCopy()[group]
			return ok
		},
		InvalidatePlan: model.InvalidateSubscriptionPlanCache,
	})
	subscriptionDone := subscriptionService.StartMaintenance(runCtx, common.IsMasterNode)
	defer func() { cancelRun(); <-subscriptionDone }()

	// Report this process as a system instance so the System Info page can show
	// all currently alive nodes in multi-instance deployments.

	// Wire task polling adaptor factory (breaks service -> relay import cycle).
	// Must run before the system task runner starts: the async_task_poll handler
	// calls service.RunTaskPollingOnce, which needs this factory set.
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		a := relay.GetTaskAdaptor(platform)
		if a == nil {
			return nil
		}
		return a
	}

	// Register the periodic channel test, upstream model update, and async task
	// polling (Midjourney / Suno / video) jobs as scheduled system tasks
	// (DB-lease dedup across masters + run history), then start the runner that
	// schedules and executes them. Master-only execution and the UpdateTask
	// switch are enforced inside the runner and each handler's Enabled().
	logService := usage.New(usage.Dependencies{Performance: performance, RankingMetadata: rankingModelMetadata, Aggregates: aggregates, DB: model.LOG_DB, Kind: common.LogDatabaseType(), ChannelNames: model.ChannelService().ChannelNames, Writer: model.LogWriterPolicy()})
	systemService := system.New(system.Dependencies{Options: model.OptionManager(), DB: model.DB, NodeName: common.NodeName, Master: common.IsMasterNode,
		Logs:           system.LogOperations{Count: logService.CountOldLog, DeleteBatch: logService.DeleteOldLogBatch},
		InstanceReport: system.InstanceReportConfig{Node: common.GetNodeIdentity(), Version: common.Version, StartedAt: common.StartTime, Resources: systemInstanceResources},
	})
	instanceReporterDone := systemService.StartSystemInstanceReporter(runCtx)
	defer func() { cancelRun(); <-instanceReporterDone }()
	tasktransport.RegisterScheduledSystemTasks(systemService, tasktransport.ScheduledWorkloads{
		ChannelTest: func(ctx context.Context, mode string, notify bool, progress func(int, int)) (any, error) {
			return controller.RunChannelTestTask(ctx, mode, notify, progress)
		},
		MidjourneyPoll: func(ctx context.Context, progress func(int, int)) any {
			return controller.RunMidjourneyTaskUpdateOnce(ctx, progress)
		},
	})
	tasktransport.RegisterChannelUpdates(systemService, model.ChannelService())
	systemService.StartSystemTaskRunner(runCtx)

	if os.Getenv("BATCH_UPDATE_ENABLED") == "true" {
		common.BatchUpdateEnabled = true
		common.SysLog("batch update enabled with interval " + strconv.Itoa(common.BatchUpdateInterval) + "s")
		model.InitBatchUpdater()
	}

	if os.Getenv("ENABLE_PPROF") == "true" {
		gopool.Go(func() {
			log.Println(http.ListenAndServe("0.0.0.0:8005", nil))
		})
		go common.Monitor()
		common.SysLog("pprof enabled")
	}

	err = common.StartPyroScope()
	if err != nil {
		common.SysError(fmt.Sprintf("start pyroscope error : %v", err))
	}

	billingService := billing.New(billing.Dependencies{DB: model.DB, PaymentAllowed: operation_setting.IsPaymentComplianceConfirmed,
		WalletRuntime: billing.WalletRuntime{
			Credit: func(id, amount int) error { return model.IncreaseUserQuota(id, amount, true) },
			Debit:  func(id, amount int) error { return model.DecreaseUserQuota(id, amount, true) },
		},
	})
	server, err := httpserver.New(assets, router.Dependencies{
		Usage:         logService,
		SystemHooks:   systemhttp.ManagementHooks{Audit: controller.RecordManageAudit},
		System:        systemService,
		Authorization: authorization,
		IdentityHooks: identityhttp.ManagementHooks{WriteRefreshCookie: service.WriteRefreshCookie, ClearRefreshCookie: service.ClearRefreshCookie, Audit: controller.RecordManageAuditFor, SessionIdentity: middleware.GetSessionAuthIdentity,
			RequireSecurityProof: middleware.RequireSecurityProof, SecurityAudit: controller.RecordUserSecurityAudit,
			PasskeyLogin: func(c *gin.Context, user *identityentity.User) {
				controller.CompletePasskeyLogin(c, (*model.User)(user))
			},
		},
		Billing:           billingService,
		BillingHooks:      billinghttp.ManagementHooks{Audit: controller.RecordManageAudit},
		SubscriptionHooks: subscriptionhttp.ManagementHooks{Audit: controller.RecordManageAuditFor, ResetLogs: controller.RecordSubscriptionResetUserLogs},
		Subscription:      subscriptionService,
		Identity: identity.New(identity.Dependencies{Authentication: service.AuthenticationRuntime(), TwoFAEvent: func(id int, message string) { model.RecordLog(id, model.LogTypeSystem, message) }, VerifyEmail: func(email, code string) bool {
			return common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose)
		}, DB: model.DB, Providers: providerRegistry{}, TokenPolicy: tokenPolicy(), InvalidateTokenCache: model.InvalidateTokenCacheForMutation, UserSecurity: userSecurity(), UserAuthorization: authorization, UserWallet: billingService, WelcomeQuota: func() int { return common.QuotaForNewUser }, WelcomeGrant: recordWelcomeGrant}),
		Channel: model.ChannelService(),
		ChannelHooks: channelhttp.ManagementHooks{
			Can: func(userID, role int, resource, action string) bool {
				return authorization.Can(userID, role, authz.Permission{Resource: resource, Action: action})
			},
			Audit: controller.RecordManageAudit,
			EnqueueModelUpdate: func() (channelhttp.TaskSubmission, error) {
				task, created, err := systemService.EnqueueSystemTask(runCtx, system.SystemTaskTypeModelUpdate, contract.UpstreamUpdateTask{Manual: true})
				if err != nil {
					return channelhttp.TaskSubmission{}, err
				}
				return channelhttp.TaskSubmission{TaskID: task.TaskID, Status: string(task.Status), Type: task.Type, Created: created}, nil
			},
		},
	})
	if err != nil {
		common.FatalLog("failed to configure HTTP server: " + err.Error())
		return
	}
	var port = os.Getenv("PORT")
	if port == "" {
		port = strconv.Itoa(*common.Port)
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: server,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			common.FatalLog("failed to start HTTP server: " + err.Error())
		}
	}()

	time.Sleep(100 * time.Millisecond)

	common.LogStartupSuccess(startTime, port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	cancelRun()
	common.SysLog(fmt.Sprintf("received signal: %v, shutting down...", sig))

	// SSE streams may run for minutes; give them time to finish before forced exit
	shutdownTimeout := time.Duration(common.GetEnvOrDefault("SHUTDOWN_TIMEOUT_SECONDS", 120)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		common.SysError(fmt.Sprintf("server forced to shutdown: %v", err))
	}
	if err := model.FlushQuotaUpdates(); err != nil {
		common.SysError("failed to flush quota updates during shutdown: " + err.Error())
	}
	// 内存中的看板数据保存入库，避免重启丢失未落库数据 (issue #5679)
	<-quotaDone
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFlush()
	if err := aggregates.Flush(flushCtx); err != nil {
		common.SysError("failed to flush dashboard usage during shutdown: " + err.Error())
	}
	<-performanceDone
	performanceCtx, cancelPerformance := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelPerformance()
	if err := performance.Flush(performanceCtx, true); err != nil {
		common.SysError("failed to flush performance metrics during shutdown: " + err.Error())
	}
	common.SysLog("server exited")
}
