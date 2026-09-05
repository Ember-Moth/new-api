package task

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"

	"github.com/QuantumNous/new-api/internal/module/system"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// channelTestHandler runs the scheduled "test all channels" job. Enablement and
// cadence still come from the monitor settings; only the execution path moved
// into the system task runner.
type channelTestHandler struct {
	tasks *system.Service
	run   func(context.Context, string, bool, func(int, int)) (any, error)
}

func (channelTestHandler) Type() string { return system.SystemTaskTypeChannelTest }

func (channelTestHandler) Enabled() bool {
	return operation_setting.GetMonitorSetting().AutoTestChannelEnabled
}

func (channelTestHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	if minutes <= 0 {
		minutes = 10
	}
	return time.Duration(minutes * float64(time.Minute))
}

func (channelTestHandler) NewPayload() any { return nil }

func (h channelTestHandler) Run(ctx context.Context, task *system.SystemTask, runnerID string) {
	payload := contract.ChannelTestTask{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := h.run(ctx, payload.Mode, payload.Notify, h.tasks.NewSystemTaskProgressReporter(ctx, task, runnerID))
	if err != nil {
		finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusSucceeded, summary, nil)
}

// midjourneyPollHandler runs one Midjourney polling pass per scheduled run.
// Enabled() folds the "are there unfinished tasks?" check into enablement so the
// scheduler creates no row when the system is idle; only when at least one
// Midjourney task is in progress does a row get scheduled.
type midjourneyPollHandler struct {
	tasks *system.Service
	run   func(context.Context, func(int, int)) any
}

func (midjourneyPollHandler) Type() string { return system.SystemTaskTypeMidjourneyPoll }

func (midjourneyPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedMidjourneyTasks()
}

func (midjourneyPollHandler) Interval() time.Duration { return 15 * time.Second }

func (midjourneyPollHandler) NewPayload() any { return nil }

func (h midjourneyPollHandler) Run(ctx context.Context, task *system.SystemTask, runnerID string) {
	summary := h.run(ctx, h.tasks.NewSystemTaskProgressReporter(ctx, task, runnerID))
	finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusSucceeded, summary, nil)
}

// asyncTaskPollHandler runs one async-task (Suno/video) polling pass per
// scheduled run. Like midjourneyPollHandler, Enabled() folds in the unfinished
// task existence check so an idle system schedules no rows.
type asyncTaskPollHandler struct{ tasks *system.Service }

func (asyncTaskPollHandler) Type() string { return system.SystemTaskTypeAsyncTaskPoll }

func (asyncTaskPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedSyncTasks()
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (h asyncTaskPollHandler) Run(ctx context.Context, task *system.SystemTask, runnerID string) {
	summary := service.RunTaskPollingOnce(ctx, h.tasks.NewSystemTaskProgressReporter(ctx, task, runnerID))
	finishSystemTaskHandler(h.tasks, task, runnerID, system.SystemTaskStatusSucceeded, summary, nil)
}

func finishSystemTaskHandler(tasks *system.Service, task *system.SystemTask, runnerID string, status system.SystemTaskStatus, result any, runErr error) {
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	if err := tasks.FinishSystemTask(context.Background(), task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}

// RegisterScheduledSystemTasks wires the periodic channel test, upstream model
// update, and async task polling (Midjourney / Suno / video) jobs into the
// system task framework so a DB lease dedups execution across multiple master
// instances and each run is recorded as one task row. Call this before
// the task service starts its runner.
func RegisterScheduledSystemTasks(tasks *system.Service, workloads ScheduledWorkloads) {
	tasks.RegisterSystemTaskHandler(channelTestHandler{tasks: tasks, run: workloads.ChannelTest})
	tasks.RegisterSystemTaskHandler(midjourneyPollHandler{tasks: tasks, run: workloads.MidjourneyPoll})
	tasks.RegisterSystemTaskHandler(asyncTaskPollHandler{tasks: tasks})
}

// ScheduledWorkloads connects the remaining execution paths during module migration.
type ScheduledWorkloads struct {
	ChannelTest    func(context.Context, string, bool, func(int, int)) (any, error)
	MidjourneyPoll func(context.Context, func(int, int)) any
}
