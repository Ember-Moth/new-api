package task

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/system"
)

const (
	SystemTaskTypeAuthArtifactCleanup     = "auth_artifact_cleanup"
	SystemTaskTypeCodexCredentialRefresh  = "codex_credential_refresh"
	SystemTaskTypeSubscriptionMaintenance = "subscription_maintenance"
	authArtifactCleanupTaskInterval       = time.Hour
	codexCredentialRefreshTaskInterval    = 10 * time.Minute
	subscriptionMaintenanceTaskInterval   = time.Minute
)

// MaintenanceWorkloads supplies one cancellable pass for each control-plane
// maintenance action. The system task runner owns scheduling, cross-instance
// leasing, and durable success/failure history.
type MaintenanceWorkloads struct {
	AuthArtifactCleanup     func(context.Context) error
	CodexCredentialRefresh  func(context.Context) error
	SubscriptionMaintenance func(context.Context) error
}

type maintenanceHandler struct {
	tasks    *system.Service
	taskType string
	interval time.Duration
	run      func(context.Context) error
}

func (h maintenanceHandler) Type() string { return h.taskType }

// The system Runtime only starts its scheduler when it is the control-plane
// master. Checking callback availability here also lets tests and embedders
// register a workload selectively without consulting process-global role state.
func (h maintenanceHandler) Enabled() bool { return h.run != nil }

func (h maintenanceHandler) Interval() time.Duration { return h.interval }

func (h maintenanceHandler) NewPayload() any { return nil }

func (h maintenanceHandler) Run(ctx context.Context, task *system.SystemTask, runnerID string) {
	if h.run == nil {
		finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusFailed, nil, errors.New("maintenance workload is not configured"))
		return
	}
	if err := h.run(ctx); err != nil {
		finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusSucceeded, nil, nil)
}

// RegisterMaintenanceTasks registers the three periodic maintenance actions
// with the existing system task scheduler. The scheduler's control-plane
// runtime gate prevents data-plane processes from creating or claiming them.
func RegisterMaintenanceTasks(tasks *system.Service, workloads MaintenanceWorkloads) {
	tasks.RegisterSystemTaskHandler(maintenanceHandler{
		tasks:    tasks,
		taskType: SystemTaskTypeAuthArtifactCleanup,
		interval: authArtifactCleanupTaskInterval,
		run:      workloads.AuthArtifactCleanup,
	})
	tasks.RegisterSystemTaskHandler(maintenanceHandler{
		tasks:    tasks,
		taskType: SystemTaskTypeCodexCredentialRefresh,
		interval: codexCredentialRefreshTaskInterval,
		run:      workloads.CodexCredentialRefresh,
	})
	tasks.RegisterSystemTaskHandler(maintenanceHandler{
		tasks:    tasks,
		taskType: SystemTaskTypeSubscriptionMaintenance,
		interval: subscriptionMaintenanceTaskInterval,
		run:      workloads.SubscriptionMaintenance,
	})
}
