package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SystemTask = entity.SystemTask
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

type Store struct {
	db    *gorm.DB
	cache *redis.Client
}

func (r *Store) CreateSystemTask(ctx context.Context, taskType string, payload any, state any) (*SystemTask, error) {
	taskID, err := GenerateSystemTaskID()
	if err != nil {
		return nil, err
	}
	payloadText, err := marshalSystemTaskJSON(payload)
	if err != nil {
		return nil, err
	}
	stateText, err := marshalSystemTaskJSON(state)
	if err != nil {
		return nil, err
	}

	task := &SystemTask{
		TaskID:    taskID,
		Type:      taskType,
		Status:    SystemTaskStatusPending,
		ActiveKey: &taskType,
		Payload:   payloadText,
		State:     stateText,
	}

	if err := r.db.WithContext(ctx).Create(task).Error; err != nil {
		return nil, err
	}
	return task, nil
}

func (r *Store) GetSystemTaskByTaskID(ctx context.Context, taskID string) (*SystemTask, error) {
	var task SystemTask
	if err := r.db.WithContext(ctx).Where("task_id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

func (r *Store) GetActiveSystemTask(ctx context.Context, taskType string) (*SystemTask, error) {
	var task SystemTask
	err := r.db.WithContext(ctx).Where("type = ? AND status IN ?", taskType, activeSystemTaskStatuses()).
		Order("id desc").
		First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

func (r *Store) FindPendingSystemTasks(ctx context.Context, taskType string, limit int) ([]*SystemTask, error) {
	var tasks []*SystemTask
	if limit <= 0 {
		limit = 1
	}
	err := r.db.WithContext(ctx).Where("type = ? AND status = ?", taskType, SystemTaskStatusPending).
		Order("id asc").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func (r *Store) FindEarliestPendingSystemTasks(ctx context.Context, taskTypes []string) (map[string]*SystemTask, error) {
	tasksByType := map[string]*SystemTask{}
	if len(taskTypes) == 0 {
		return tasksByType, nil
	}

	subQuery := r.db.WithContext(ctx).Model(&SystemTask{}).
		Select("MIN(id)").
		Where("type IN ? AND status = ?", taskTypes, SystemTaskStatusPending).
		Group("type")
	var tasks []*SystemTask
	if err := r.db.WithContext(ctx).Where("id IN (?)", subQuery).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		tasksByType[task.Type] = task
	}
	return tasksByType, nil
}

func (r *Store) ListSystemTasks(ctx context.Context, limit int) ([]*SystemTask, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var tasks []*SystemTask
	err := r.db.WithContext(ctx).Order("id desc").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// GetLatestSystemTask returns the most recent task row of the given type
// (any status) so the scheduler can decide whether enough time has elapsed
// since the last run. Returns (nil, nil) when no row exists.
func (r *Store) GetLatestSystemTask(ctx context.Context, taskType string) (*SystemTask, error) {
	var task SystemTask
	err := r.db.WithContext(ctx).Where("type = ?", taskType).Order("id desc").First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

func (r *Store) GetLatestSystemTasks(ctx context.Context, taskTypes []string) (map[string]*SystemTask, error) {
	tasksByType := map[string]*SystemTask{}
	if len(taskTypes) == 0 {
		return tasksByType, nil
	}

	subQuery := r.db.WithContext(ctx).Model(&SystemTask{}).
		Select("MAX(id)").
		Where("type IN ?", taskTypes).
		Group("type")
	var tasks []*SystemTask
	if err := r.db.WithContext(ctx).Where("id IN (?)", subQuery).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		tasksByType[task.Type] = task
	}
	return tasksByType, nil
}

func (r *Store) ClaimSystemTask(ctx context.Context, id int64, taskType string, runnerID string, lockUntil int64) (*SystemTask, bool, error) {
	if r.cache == nil {
		return nil, false, errors.New("DragonflyDB is required for system task leases")
	}
	var task SystemTask
	if err := r.db.WithContext(ctx).Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	ttl := time.Until(time.Unix(lockUntil, 0))
	if ttl < time.Millisecond {
		return nil, false, ErrSystemTaskLockLost
	}
	acquired, err := r.cache.SetNX(ctx, taskLeasePrefix+taskType, task.TaskID+":"+runnerID, ttl).Result()
	if err != nil || !acquired {
		return nil, false, err
	}
	claimed := false
	defer func() {
		if !claimed {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.releaseTaskLease(cleanupCtx, &task, runnerID)
		}
	}()
	result := r.db.WithContext(ctx).Model(&task).Clauses(clause.Returning{}).
		Where("id = ? AND status = ?", id, SystemTaskStatusPending).
		Updates(map[string]any{"status": SystemTaskStatusRunning, "locked_by": runnerID, "updated_at": common.GetTimestamp()})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	owned, err := r.ownsTaskLease(ctx, &task, runnerID)
	if err != nil {
		return nil, false, err
	}
	if !owned {
		return nil, false, ErrSystemTaskLockLost
	}
	claimed = true
	return &task, true, nil
}

// The durable execution owner fences updates against recovery and completion.
// Hold the task row lock while checking the cache lease and committing a result;
// recovery must acquire the same row lock before retiring an execution.
func (r *Store) ownedTask(ctx context.Context, tx *gorm.DB, taskID, owner string) (*SystemTask, error) {
	var task SystemTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND status = ? AND locked_by = ?", taskID, SystemTaskStatusRunning, owner).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSystemTaskLockLost
		}
		return nil, err
	}
	owned, err := r.ownsTaskLease(ctx, &task, owner)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrSystemTaskLockLost
	}
	return &task, nil
}

func (r *Store) UpdateSystemTaskState(ctx context.Context, taskID, owner string, state any) error {
	stateText, err := marshalSystemTaskJSON(state)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := r.ownedTask(ctx, tx, taskID, owner)
		if err != nil {
			return err
		}
		return tx.Model(task).Updates(map[string]any{"state": stateText, "updated_at": common.GetTimestamp()}).Error
	})
}

func (r *Store) ExpireStaleSystemTaskLocks(ctx context.Context, now int64) error {
	var running []*SystemTask
	if err := r.db.WithContext(ctx).Where("status = ?", SystemTaskStatusRunning).Find(&running).Error; err != nil {
		return err
	}
	for _, candidate := range running {
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var task SystemTask
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND status = ? AND locked_by = ?", candidate.TaskID, SystemTaskStatusRunning, candidate.LockedBy).First(&task).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			owned, err := r.ownsTaskLease(ctx, &task, task.LockedBy)
			if err != nil {
				return err
			}
			if owned {
				return nil
			}
			return tx.Model(&task).Updates(map[string]any{"status": SystemTaskStatusFailed, "active_key": nil, "error": "task lease expired", "updated_at": now}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Store) FinishSystemTask(ctx context.Context, taskID, owner string, status SystemTaskStatus, resultPayload any, errorMessage string) error {
	if status != SystemTaskStatusSucceeded && status != SystemTaskStatusFailed {
		return errors.New("task completion requires a terminal status")
	}
	resultText, err := marshalSystemTaskJSON(resultPayload)
	if err != nil {
		return err
	}
	var task *SystemTask
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		task, err = r.ownedTask(ctx, tx, taskID, owner)
		if err != nil {
			return err
		}
		return tx.Model(task).Updates(map[string]any{"status": status, "active_key": nil, "result": resultText, "error": errorMessage, "updated_at": common.GetTimestamp()}).Error
	})
	if err != nil {
		return err
	}
	return r.releaseTaskLease(ctx, task, owner)
}

func GenerateSystemTaskID() (string, error) {
	key, err := common.GenerateRandomCharsKey(32)
	if err != nil {
		return "", err
	}
	return "systask_" + key, nil
}

func marshalSystemTaskJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := common.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func activeSystemTaskStatuses() []string {
	return []string{string(SystemTaskStatusPending), string(SystemTaskStatusRunning)}
}
