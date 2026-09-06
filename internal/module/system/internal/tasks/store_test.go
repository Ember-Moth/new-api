package tasks

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"
	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testSystemTaskPayload struct {
	TargetTimestamp int64 `json:"target_timestamp"`
	BatchSize       int   `json:"batch_size"`
}

type testSystemTaskState struct {
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Progress  int   `json:"progress"`
	Remaining int64 `json:"remaining"`
}

func createPendingSystemTaskWithoutActiveKey(t *testing.T, r *Runtime, taskType string) *SystemTask {
	t.Helper()
	taskID, err := GenerateSystemTaskID()
	require.NoError(t, err)
	task := &SystemTask{
		TaskID: taskID,
		Type:   taskType,
		Status: SystemTaskStatusPending,
	}
	require.NoError(t, r.db.Create(task).Error)
	return task
}

func TestSystemTaskCreateAndActiveLifecycle(t *testing.T) {
	r := newTaskFixture(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	state := testSystemTaskState{}
	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
	require.NotNil(t, task.ActiveKey)
	assert.Equal(t, SystemTaskTypeLogCleanup, *task.ActiveKey)

	var decodedPayload testSystemTaskPayload
	require.NoError(t, task.DecodePayload(&decodedPayload))
	assert.Equal(t, payload, decodedPayload)

	activeTask, err := r.GetActiveSystemTask(t.Context(), SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)

	runnerID := "runner-a"
	claimedTask, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	err = r.FinishSystemTask(t.Context(), claimedTask.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 0}, "")
	require.NoError(t, err)

	finishedTask, err := r.GetSystemTaskByTaskID(t.Context(), task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finishedTask)
	assert.Nil(t, finishedTask.ActiveKey)

	activeTask, err = r.GetActiveSystemTask(t.Context(), SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.Nil(t, activeTask)

	_, err = r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
}

func TestSystemTaskActiveKeyPreventsDuplicateActiveRun(t *testing.T) {
	r := newTaskFixture(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	_, err = r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.Error(t, err)

	activeTask, err := r.GetActiveSystemTask(t.Context(), SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)
}

func TestSystemTaskLockPreventsConcurrentClaim(t *testing.T) {
	r := newTaskFixture(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	secondTask := createPendingSystemTaskWithoutActiveKey(t, r, SystemTaskTypeLogCleanup)

	claimedTask, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	_, claimed, err = r.ClaimSystemTask(t.Context(), secondTask.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.False(t, claimed)

	assert.Equal(t, "runner-a", claimedTask.LockedBy)

	reloadedSecond, err := r.GetSystemTaskByTaskID(t.Context(), secondTask.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedSecond)
	assert.Equal(t, SystemTaskStatusPending, reloadedSecond.Status)
}

func TestSystemTaskClaimFailureReleasesLeaseAndKeepsTaskPending(t *testing.T) {
	r := newTaskFixture(t)
	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	require.NoError(t, r.db.Exec(`
CREATE FUNCTION test_fail_task_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.locked_by = 'runner-b' AND NEW.status = 'running' THEN
        RAISE EXCEPTION 'injected task update failure';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER test_fail_task_claim BEFORE UPDATE ON system_tasks
FOR EACH ROW EXECUTE FUNCTION test_fail_task_claim();`).Error)
	t.Cleanup(func() { require.NoError(t, r.db.Exec("DROP FUNCTION test_fail_task_claim() CASCADE").Error) })
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, task.Type, "runner-b", common.GetTimestamp()+60)
	require.Error(t, err)
	assert.False(t, claimed)
	pending, err := r.GetSystemTaskByTaskID(t.Context(), task.TaskID)
	require.NoError(t, err)
	assert.Equal(t, SystemTaskStatusPending, pending.Status)
	_, claimed, err = r.ClaimSystemTask(t.Context(), task.ID, task.Type, "runner-c", common.GetTimestamp()+60)
	require.NoError(t, err)
	assert.True(t, claimed, "failed SQL claim must release its cache lease")
}

func TestSystemTaskParallelClaimHasOneOwner(t *testing.T) {
	r := newTaskFixture(t)
	pool, err := r.db.DB()
	require.NoError(t, err)
	previousMax := pool.Stats().MaxOpenConnections
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { pool.SetMaxOpenConns(previousMax) })
	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	type outcome struct {
		claimed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, runner := range []string{"parallel-a", "parallel-b"} {
		go func(runner string) {
			<-start
			_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, task.Type, runner, common.GetTimestamp()+60)
			results <- outcome{claimed, err}
		}(runner)
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		if result.claimed {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
}

func TestExpiredSystemTaskLockRecoveryAllowsPendingRun(t *testing.T) {
	r := newTaskFixture(t)

	first, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := r.ClaimSystemTask(t.Context(), first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, r.cache.PExpireAt(t.Context(), taskLeasePrefix+first.Type, time.Unix(1, 0)).Err())

	require.NoError(t, r.ExpireStaleSystemTaskLocks(t.Context(), common.GetTimestamp()))
	second := createPendingSystemTaskWithoutActiveKey(t, r, SystemTaskTypeLogCleanup)
	claimedTask, claimed, err := r.ClaimSystemTask(t.Context(), second.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, second.TaskID, claimedTask.TaskID)
	assert.Equal(t, "runner-b", claimedTask.LockedBy)

	reloadedFirst, err := r.GetSystemTaskByTaskID(t.Context(), first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)
}

func TestExpireStaleSystemTaskLockFailsOldRunAndAllowsNewRun(t *testing.T) {
	r := newTaskFixture(t)

	first, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := r.ClaimSystemTask(t.Context(), first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, r.cache.PExpireAt(t.Context(), taskLeasePrefix+first.Type, time.Unix(1, 0)).Err())

	require.NoError(t, r.ExpireStaleSystemTaskLocks(t.Context(), common.GetTimestamp()))

	reloadedFirst, err := r.GetSystemTaskByTaskID(t.Context(), first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)

	count, err := r.cache.Exists(t.Context(), taskLeasePrefix+SystemTaskTypeLogCleanup).Result()
	require.NoError(t, err)
	assert.Zero(t, count)

	second, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.TaskID, second.TaskID)
}

func TestFindEarliestPendingSystemTasks(t *testing.T) {
	r := newTaskFixture(t)

	empty, err := r.FindEarliestPendingSystemTasks(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := r.CreateSystemTask(t.Context(), "type_a", nil, nil)
	require.NoError(t, err)
	ignoredB, err := r.CreateSystemTask(t.Context(), "type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := r.ClaimSystemTask(t.Context(), ignoredB.ID, "type_b", "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), ignoredB.TaskID, "runner-b", SystemTaskStatusFailed, nil, "failed"))
	firstB, err := r.CreateSystemTask(t.Context(), "type_b", nil, nil)
	require.NoError(t, err)
	ignoredC, err := r.CreateSystemTask(t.Context(), "type_c", nil, nil)
	require.NoError(t, err)
	_, claimed, err = r.ClaimSystemTask(t.Context(), ignoredC.ID, "type_c", "runner-c", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), ignoredC.TaskID, "runner-c", SystemTaskStatusFailed, nil, "failed"))

	tasks, err := r.FindEarliestPendingSystemTasks(t.Context(), []string{"type_a", "type_b", "type_c", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["type_c"])
	assert.Nil(t, tasks["missing"])
}

func TestGetLatestSystemTask(t *testing.T) {
	r := newTaskFixture(t)

	latest, err := r.GetLatestSystemTask(t.Context(), SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.Nil(t, latest)

	first, err := r.CreateSystemTask(t.Context(), SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := r.ClaimSystemTask(t.Context(), first.ID, SystemTaskTypeChannelTest, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), first.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	second, err := r.CreateSystemTask(t.Context(), SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	latest, err = r.GetLatestSystemTask(t.Context(), SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, second.TaskID, latest.TaskID)
}

func TestGetLatestSystemTasks(t *testing.T) {
	r := newTaskFixture(t)

	empty, err := r.GetLatestSystemTasks(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := r.CreateSystemTask(t.Context(), "type_a", nil, nil)
	require.NoError(t, err)
	firstB, err := r.CreateSystemTask(t.Context(), "type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := r.ClaimSystemTask(t.Context(), firstA.ID, "type_a", "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), firstA.TaskID, "runner-a", SystemTaskStatusSucceeded, nil, ""))
	secondA, err := r.CreateSystemTask(t.Context(), "type_a", nil, nil)
	require.NoError(t, err)

	tasks, err := r.GetLatestSystemTasks(t.Context(), []string{"type_a", "type_b", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, secondA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["missing"])
}

func TestRenewSystemTaskLock(t *testing.T) {
	r := newTaskFixture(t)

	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	newLockUntil := common.GetTimestamp() + 600
	require.NoError(t, r.RenewSystemTaskLock(t.Context(), task.TaskID, runnerID, newLockUntil))

	ttl, err := r.cache.PTTL(t.Context(), taskLeasePrefix+task.Type).Result()
	require.NoError(t, err)
	assert.InDelta(t, 600, ttl.Seconds(), 2)

	// A different runner cannot renew a lease it does not hold.
	assert.ErrorIs(t, r.RenewSystemTaskLock(t.Context(), task.TaskID, "runner-b", common.GetTimestamp()+600), ErrSystemTaskLockLost)

	// After the task finishes it is no longer running, so renew fails.
	require.NoError(t, r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))
	assert.ErrorIs(t, r.RenewSystemTaskLock(t.Context(), task.TaskID, runnerID, common.GetTimestamp()+600), ErrSystemTaskLockLost)
}

func TestFinishSystemTaskRetainsExecutor(t *testing.T) {
	r := newTaskFixture(t)

	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "node-1-abc123"
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	reloaded, err := r.GetSystemTaskByTaskID(t.Context(), task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusSucceeded, reloaded.Status)
	assert.Equal(t, runnerID, reloaded.LockedBy, "executor-of-record must be retained for history")

	count, err := r.cache.Exists(t.Context(), taskLeasePrefix+SystemTaskTypeLogCleanup).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSystemTaskUpdatesRequireCurrentLock(t *testing.T) {
	r := newTaskFixture(t)

	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, r.cache.Set(t.Context(), taskLeasePrefix+task.Type, task.TaskID+":runner-b", time.Minute).Err())

	assert.ErrorIs(t, r.UpdateSystemTaskState(t.Context(), task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)
}

func TestSystemTaskUpdatesRequireUnexpiredLock(t *testing.T) {
	r := newTaskFixture(t)

	task, err := r.CreateSystemTask(t.Context(), SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, r.cache.PExpireAt(t.Context(), taskLeasePrefix+task.Type, time.Unix(1, 0)).Err())

	assert.ErrorIs(t, r.UpdateSystemTaskState(t.Context(), task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)

	reloaded, err := r.GetSystemTaskByTaskID(t.Context(), task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusRunning, reloaded.Status)
	assert.Empty(t, reloaded.State)
}

func TestUpdateSystemTaskStateIdenticalPayloadDoesNotLoseLock(t *testing.T) {
	r := newTaskFixture(t)
	runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t, r, SystemTaskTypeLogCleanup)
}

func runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t *testing.T, r *Runtime, taskType string) {
	t.Helper()
	// Repeating an identical state must preserve the active lease.

	task, err := r.CreateSystemTask(t.Context(), taskType, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := r.ClaimSystemTask(t.Context(), task.ID, taskType, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	state := testSystemTaskState{Total: 10, Processed: 10, Progress: 100, Remaining: 0}
	require.NoError(t, r.UpdateSystemTaskState(t.Context(), task.TaskID, runnerID, state))
	require.NoError(t, r.UpdateSystemTaskState(t.Context(), task.TaskID, runnerID, state), "identical state persist must not be treated as lock loss")

	require.NoError(t, r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 10}, ""))
	finished, err := r.GetSystemTaskByTaskID(t.Context(), task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finished)
	assert.Equal(t, SystemTaskStatusSucceeded, finished.Status)
}

func newTaskFixture(t *testing.T) *Runtime {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	require.NoError(t, schema.UpPostgres(pool))
	cache := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	return New(Dependencies{Cache: cache, DB: db, Master: true, NodeName: "test-runner"})
}
