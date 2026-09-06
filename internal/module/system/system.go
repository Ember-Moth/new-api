package system

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/internal/module/system/internal/instances"
	"github.com/QuantumNous/new-api/internal/module/system/internal/options"
	implementation "github.com/QuantumNous/new-api/internal/module/system/internal/tasks"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type Service struct {
	*options.Manager
	*implementation.Runtime
	*instances.Registry
	*instances.Reporter
}

type InstanceReportConfig = instances.ReportConfig
type Dependencies struct {
	Cache          *redis.Client
	Options        *Options
	DB             *gorm.DB
	NodeName       string
	Master         bool
	Logs           LogOperations
	InstanceReport InstanceReportConfig
}

type LogOperations = implementation.LogOperations
type SystemTaskHandler = implementation.SystemTaskHandler
type ScheduledSystemTaskHandler = implementation.ScheduledSystemTaskHandler
type LogCleanupPayload = implementation.LogCleanupPayload
type LogCleanupState = implementation.LogCleanupState
type LogCleanupResult = implementation.LogCleanupResult
type SystemTaskProgress = implementation.SystemTaskProgress
type SystemTask = entity.SystemTask
type SystemTaskLock = entity.SystemTaskLock
type SystemTaskStatus = entity.SystemTaskStatus
type SystemTaskResponse = entity.SystemTaskResponse

const (
	SystemTaskStatusPending      = entity.SystemTaskStatusPending
	SystemTaskStatusRunning      = entity.SystemTaskStatusRunning
	SystemTaskStatusSucceeded    = entity.SystemTaskStatusSucceeded
	SystemTaskStatusFailed       = entity.SystemTaskStatusFailed
	SystemTaskTypeLogCleanup     = entity.SystemTaskTypeLogCleanup
	SystemTaskTypeChannelTest    = entity.SystemTaskTypeChannelTest
	SystemTaskTypeModelUpdate    = entity.SystemTaskTypeModelUpdate
	SystemTaskTypeMidjourneyPoll = entity.SystemTaskTypeMidjourneyPoll
	SystemTaskTypeAsyncTaskPoll  = entity.SystemTaskTypeAsyncTaskPoll
)

var ErrSystemTaskLockLost = entity.ErrSystemTaskLockLost

func New(deps Dependencies) *Service {
	optionManager := deps.Options
	if optionManager == nil {
		optionManager = NewOptions(OptionDependencies{DB: deps.DB})
	}
	registry := instances.NewRegistry(deps.Cache)
	return &Service{
		Manager:  optionManager,
		Runtime:  implementation.New(implementation.Dependencies{DB: deps.DB, NodeName: deps.NodeName, Master: deps.Master, Logs: deps.Logs}),
		Registry: registry, Reporter: instances.NewReporter(registry, deps.InstanceReport, deps.Master),
	}
}
func GenerateSystemTaskID() (string, error) { return implementation.GenerateSystemTaskID() }

type contextKey struct{}

func WithService(ctx context.Context, service *Service) context.Context {
	return context.WithValue(ctx, contextKey{}, service)
}
func FromContext(ctx context.Context) *Service {
	service, _ := ctx.Value(contextKey{}).(*Service)
	return service
}

type Options = options.Manager
type OptionDependencies = options.Dependencies

var ErrPaymentComplianceRequired = options.ErrPaymentComplianceRequired

func NewOptions(deps OptionDependencies) *Options { return options.New(deps) }
