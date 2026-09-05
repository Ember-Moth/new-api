package system

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/system/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/system/internal/tasks"
)

type Service = implementation.Runtime
type Dependencies = implementation.Dependencies
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

func New(deps Dependencies) *Service        { return implementation.New(deps) }
func GenerateSystemTaskID() (string, error) { return implementation.GenerateSystemTaskID() }

type contextKey struct{}

func WithService(ctx context.Context, service *Service) context.Context {
	return context.WithValue(ctx, contextKey{}, service)
}
func FromContext(ctx context.Context) *Service {
	service, _ := ctx.Value(contextKey{}).(*Service)
	return service
}
