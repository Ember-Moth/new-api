package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/go-redis/redis/v8"
)

const taskLeasePrefix = "system:task-lease:"

var taskLeaseRenew = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('PEXPIRE', KEYS[1], ARGV[2])
`)
var taskLeaseRelease = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])
`)

func (r *Store) ownsTaskLease(ctx context.Context, task *SystemTask, owner string) (bool, error) {
	if r.cache == nil {
		return false, errors.New("DragonflyDB is required for system task leases")
	}
	value, err := r.cache.Get(ctx, taskLeasePrefix+task.Type).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return value == task.TaskID+":"+owner, err
}

func (r *Store) releaseTaskLease(ctx context.Context, task *SystemTask, owner string) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for system task leases")
	}
	return taskLeaseRelease.Run(ctx, r.cache, []string{taskLeasePrefix + task.Type}, task.TaskID+":"+owner).Err()
}

func (r *Store) RenewSystemTaskLock(ctx context.Context, taskID, owner string, lockUntil int64) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for system task leases")
	}
	task, err := r.GetSystemTaskByTaskID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.Status != SystemTaskStatusRunning || task.LockedBy != owner {
		return ErrSystemTaskLockLost
	}
	ttl := time.Until(time.Unix(lockUntil, 0))
	if ttl < time.Millisecond {
		return ErrSystemTaskLockLost
	}
	renewed, err := taskLeaseRenew.Run(ctx, r.cache, []string{taskLeasePrefix + task.Type}, taskID+":"+owner, ttl.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if renewed == 0 {
		return ErrSystemTaskLockLost
	}
	return nil
}

func (r *Store) ReleaseSystemTaskLock(ctx context.Context, taskID, owner string) error {
	task, err := r.GetSystemTaskByTaskID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	return r.releaseTaskLease(ctx, task, owner)
}
