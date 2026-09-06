package task

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/module/system"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
)

func RegisterChannelUpdates(tasks *system.Service, channels *channel.Service) {
	tasks.RegisterSystemTaskHandler(channelModelUpdate{channels: channels, tasks: tasks})
}

type channelModelUpdate struct {
	tasks    *system.Service
	channels *channel.Service
}

func (channelModelUpdate) Type() string { return system.SystemTaskTypeModelUpdate }
func (channelModelUpdate) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
}
func (channelModelUpdate) Interval() time.Duration {
	minutes := common.GetEnvOrDefault("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", channel.UpstreamModelUpdateDefaultIntervalMinutes)
	if minutes < 1 {
		minutes = channel.UpstreamModelUpdateDefaultIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}
func (channelModelUpdate) NewPayload() any { return nil }

func (h channelModelUpdate) Run(ctx context.Context, task *system.SystemTask, runnerID string) {
	var payload contract.UpstreamUpdateTask
	status := system.SystemTaskStatusSucceeded
	var result any
	err := task.DecodePayload(&payload)
	if err != nil {
		status = system.SystemTaskStatusFailed
	} else {
		result = h.channels.RunUpstreamModelUpdate(ctx, payload.Manual, !payload.Manual, h.tasks.NewSystemTaskProgressReporter(ctx, task, runnerID))
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if err := h.tasks.FinishSystemTask(context.Background(), task.TaskID, runnerID, status, result, message); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
