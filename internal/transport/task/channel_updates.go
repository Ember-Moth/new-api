package task

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func RegisterChannelUpdates(channels *channel.Service) {
	service.RegisterSystemTaskHandler(channelModelUpdate{channels: channels})
}

type channelModelUpdate struct {
	channels *channel.Service
}

func (channelModelUpdate) Type() string { return model.SystemTaskTypeModelUpdate }
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

func (h channelModelUpdate) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	var payload contract.UpstreamUpdateTask
	status := model.SystemTaskStatusSucceeded
	var result any
	err := task.DecodePayload(&payload)
	if err != nil {
		status = model.SystemTaskStatusFailed
	} else {
		result = h.channels.RunUpstreamModelUpdate(ctx, payload.Manual, !payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, message); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
