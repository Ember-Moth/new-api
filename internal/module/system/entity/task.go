package entity

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type SystemTaskStatus string

const (
	SystemTaskStatusPending   SystemTaskStatus = "pending"
	SystemTaskStatusRunning   SystemTaskStatus = "running"
	SystemTaskStatusSucceeded SystemTaskStatus = "succeeded"
	SystemTaskStatusFailed    SystemTaskStatus = "failed"

	SystemTaskTypeLogCleanup     = "log_cleanup"
	SystemTaskTypeChannelTest    = "channel_test"
	SystemTaskTypeModelUpdate    = "model_update"
	SystemTaskTypeMidjourneyPoll = "midjourney_poll"
	SystemTaskTypeAsyncTaskPoll  = "async_task_poll"
)

var ErrSystemTaskLockLost = errors.New("system task lock lost")

type SystemTask struct {
	ID        int64            `json:"id" gorm:"primary_key"`
	TaskID    string           `json:"task_id" gorm:"type:varchar(64);uniqueIndex"`
	Type      string           `json:"type" gorm:"type:varchar(64);index"`
	Status    SystemTaskStatus `json:"status" gorm:"type:varchar(32);index"`
	ActiveKey *string          `json:"active_key,omitempty" gorm:"type:varchar(64);uniqueIndex"`
	Payload   string           `json:"payload" gorm:"type:text"`
	State     string           `json:"state" gorm:"type:text"`
	Result    string           `json:"result" gorm:"type:text"`
	Error     string           `json:"error" gorm:"type:text"`
	LockedBy  string           `json:"locked_by" gorm:"type:varchar(128);index"`
	CreatedAt int64            `json:"created_at" gorm:"bigint;index"`
	UpdatedAt int64            `json:"updated_at" gorm:"bigint;index"`
}

type SystemTaskLock struct {
	Type        string `json:"type" gorm:"type:varchar(64);primaryKey"`
	TaskID      string `json:"task_id" gorm:"type:varchar(64);index"`
	LockedBy    string `json:"locked_by" gorm:"type:varchar(128);index"`
	LockedUntil int64  `json:"locked_until" gorm:"bigint;index"`
	UpdatedAt   int64  `json:"updated_at" gorm:"bigint;index"`
}

type SystemTaskResponse struct {
	ID        int64            `json:"id"`
	TaskID    string           `json:"task_id"`
	Type      string           `json:"type"`
	Status    SystemTaskStatus `json:"status"`
	ActiveKey *string          `json:"active_key,omitempty"`
	Payload   any              `json:"payload"`
	State     any              `json:"state"`
	Result    any              `json:"result"`
	Error     string           `json:"error"`
	LockedBy  string           `json:"locked_by"`
	CreatedAt int64            `json:"created_at"`
	UpdatedAt int64            `json:"updated_at"`
}

func (task *SystemTask) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if task.CreatedAt == 0 {
		task.CreatedAt = now
	}
	if task.UpdatedAt == 0 {
		task.UpdatedAt = now
	}
	return nil
}

func (lock *SystemTaskLock) BeforeCreate(_ *gorm.DB) error {
	if lock.UpdatedAt == 0 {
		lock.UpdatedAt = common.GetTimestamp()
	}
	return nil
}

func (task *SystemTask) DecodePayload(v any) error {
	return decodeSystemTaskJSONString(task.Payload, v)
}

func (task *SystemTask) DecodeState(v any) error {
	return decodeSystemTaskJSONString(task.State, v)
}

func (task *SystemTask) ToResponse() SystemTaskResponse {
	return SystemTaskResponse{
		ID:        task.ID,
		TaskID:    task.TaskID,
		Type:      task.Type,
		Status:    task.Status,
		ActiveKey: task.ActiveKey,
		Payload:   decodeSystemTaskJSONValue(task.Payload),
		State:     decodeSystemTaskJSONValue(task.State),
		Result:    decodeSystemTaskJSONValue(task.Result),
		Error:     task.Error,
		LockedBy:  task.LockedBy,
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
}

func decodeSystemTaskJSONString(data string, v any) error {
	if data == "" {
		return nil
	}
	return common.UnmarshalJsonStr(data, v)
}

func decodeSystemTaskJSONValue(data string) any {
	if data == "" {
		return nil
	}
	var value any
	if err := common.UnmarshalJsonStr(data, &value); err != nil {
		return data
	}
	return value
}
