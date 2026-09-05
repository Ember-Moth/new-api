package tasks

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

type Store struct{ db *gorm.DB }

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
	now := common.GetTimestamp()
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, false, tx.Error
	}
	defer tx.Rollback()

	var lease struct {
		PreviousTaskID string
		TaskID         string
	}
	result := tx.Raw(`
INSERT INTO system_task_locks (type, task_id, locked_by, locked_until, updated_at)
SELECT type, task_id, ?, ?, ? FROM system_tasks
WHERE id = ? AND type = ? AND status = ?
ON CONFLICT (type) DO UPDATE SET
    task_id = EXCLUDED.task_id,
    locked_by = EXCLUDED.locked_by,
    locked_until = EXCLUDED.locked_until,
    updated_at = EXCLUDED.updated_at
WHERE system_task_locks.locked_until < EXCLUDED.updated_at
RETURNING COALESCE(OLD.task_id, '') AS previous_task_id, NEW.task_id AS task_id`,
		runnerID, lockUntil, now, id, taskType, SystemTaskStatusPending).Scan(&lease)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	if lease.PreviousTaskID != "" && lease.PreviousTaskID != lease.TaskID {
		if err := tx.Model(&SystemTask{}).
			Where("task_id = ? AND status = ?", lease.PreviousTaskID, SystemTaskStatusRunning).
			Updates(map[string]any{
				"status": SystemTaskStatusFailed, "active_key": nil,
				"error": "task lease expired", "updated_at": now,
			}).Error; err != nil {
			return nil, false, err
		}
	}

	var task SystemTask
	result = tx.Model(&task).Clauses(clause.Returning{}).
		Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).
		Updates(map[string]any{
			"status": SystemTaskStatusRunning, "locked_by": runnerID, "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	if err := tx.Commit().Error; err != nil {
		return nil, false, err
	}
	return &task, true, nil
}

func (r *Store) UpdateSystemTaskState(ctx context.Context, taskID string, lockedBy string, state any) error {
	stateText, err := marshalSystemTaskJSON(state)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	result := r.db.WithContext(ctx).Model(&SystemTask{}).
		Where("task_id = ? AND status = ? AND locked_by = ?", taskID, SystemTaskStatusRunning, lockedBy).
		Where("EXISTS (SELECT 1 FROM system_task_locks WHERE system_task_locks.task_id = system_tasks.task_id AND system_task_locks.locked_by = ? AND system_task_locks.locked_until >= ?)", lockedBy, now).
		Updates(map[string]any{
			"state":      stateText,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return ErrSystemTaskLockLost
}

func (r *Store) RenewSystemTaskLock(ctx context.Context, taskID string, lockedBy string, lockUntil int64) error {
	now := common.GetTimestamp()
	result := r.db.WithContext(ctx).Model(&SystemTaskLock{}).
		Where("task_id = ? AND locked_by = ? AND locked_until >= ?", taskID, lockedBy, now).
		Updates(map[string]any{
			"locked_until": lockUntil,
			"updated_at":   now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSystemTaskLockLost
	}
	return nil
}

func (r *Store) MarkSystemTaskLeaseExpired(ctx context.Context, taskID string) error {
	result := r.db.WithContext(ctx).Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{
			"status":     SystemTaskStatusFailed,
			"active_key": nil,
			"error":      "task lease expired",
			"updated_at": common.GetTimestamp(),
		})
	return result.Error
}

func (r *Store) ExpireStaleSystemTaskLocks(ctx context.Context, now int64) error {
	var locks []*SystemTaskLock
	if err := r.db.WithContext(ctx).Where("locked_until < ?", now).Find(&locks).Error; err != nil {
		return err
	}
	for _, lock := range locks {
		if err := r.MarkSystemTaskLeaseExpired(ctx, lock.TaskID); err != nil {
			return err
		}
		result := r.db.WithContext(ctx).Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until < ?", lock.Type, lock.TaskID, lock.LockedBy, now).
			Delete(&SystemTaskLock{})
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func (r *Store) ReleaseSystemTaskLock(ctx context.Context, taskID string, lockedBy string) error {
	result := r.db.WithContext(ctx).Where("task_id = ? AND locked_by = ?", taskID, lockedBy).Delete(&SystemTaskLock{})
	return result.Error
}

func (r *Store) FinishSystemTask(ctx context.Context, taskID string, lockedBy string, status SystemTaskStatus, resultPayload any, errorMessage string) error {
	resultText, err := marshalSystemTaskJSON(resultPayload)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	result := r.db.WithContext(ctx).Model(&SystemTask{}).
		Where("task_id = ? AND status = ? AND locked_by = ?", taskID, SystemTaskStatusRunning, lockedBy).
		Where("EXISTS (SELECT 1 FROM system_task_locks WHERE system_task_locks.task_id = system_tasks.task_id AND system_task_locks.locked_by = ? AND system_task_locks.locked_until >= ?)", lockedBy, now).
		Updates(map[string]any{
			"status":     status,
			"active_key": nil,
			"result":     resultText,
			"error":      errorMessage,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSystemTaskLockLost
	}
	return r.ReleaseSystemTaskLock(ctx, taskID, lockedBy)
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
