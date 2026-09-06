package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withSystemTaskRegistry(r *Runtime, handlers ...SystemTaskHandler) {
	r.handlers = make(map[string]SystemTaskHandler)
	for _, handler := range handlers {
		r.RegisterSystemTaskHandler(handler)
	}
}

type stubScheduledHandler struct {
	taskType string
	enabled  bool
	interval time.Duration
	onRun    func(ctx context.Context, task *SystemTask, runnerID string)
}

type stubSystemTaskRunResult struct {
	taskID   string
	taskType string
	err      error
}

func (h *stubScheduledHandler) Type() string { return h.taskType }

func (h *stubScheduledHandler) Run(ctx context.Context, task *SystemTask, runnerID string) {
	if h.onRun != nil {
		h.onRun(ctx, task, runnerID)
	}
}

func (h *stubScheduledHandler) Enabled() bool           { return h.enabled }
func (h *stubScheduledHandler) Interval() time.Duration { return h.interval }
func (h *stubScheduledHandler) NewPayload() any         { return nil }

func countSystemTasks(t *testing.T, r *Runtime, taskType string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, r.db.Model(&SystemTask{}).Where("type = ?", taskType).Count(&count).Error)
	return count
}

func TestSystemTaskSchedulerCreatesWhenDueAndDedups(t *testing.T) {
	r := newTaskFixture(t)

	handler := &stubScheduledHandler{taskType: "test_scheduled", enabled: true, interval: time.Minute}
	withSystemTaskRegistry(r, handler)

	r.runSystemTaskScheduler(t.Context())
	require.Equal(t, int64(1), countSystemTasks(t, r, handler.taskType))

	// An active (pending) row already exists, so a second pass must not create
	// another row.
	r.runSystemTaskScheduler(t.Context())
	require.Equal(t, int64(1), countSystemTasks(t, r, handler.taskType))

	// Finish the run; with a fresh updated_at the next run is not due yet.
	latest, err := r.GetLatestSystemTask(t.Context(), handler.taskType)
	require.NoError(t, err)
	require.NotNil(t, latest)
	_, claimed, err := r.ClaimSystemTask(t.Context(), latest.ID, handler.taskType, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), latest.TaskID, "runner-a", SystemTaskStatusSucceeded, nil, ""))

	r.runSystemTaskScheduler(t.Context())
	require.Equal(t, int64(1), countSystemTasks(t, r, handler.taskType))

	// Backdate the finished row beyond the interval -> the job becomes due again.
	require.NoError(t, r.db.Model(&SystemTask{}).
		Where("task_id = ?", latest.TaskID).
		Update("updated_at", common.GetTimestamp()-120).Error)

	r.runSystemTaskScheduler(t.Context())
	require.Equal(t, int64(2), countSystemTasks(t, r, handler.taskType))
}

func TestSystemTaskSchedulerSkipsDisabled(t *testing.T) {
	r := newTaskFixture(t)

	handler := &stubScheduledHandler{taskType: "test_disabled", enabled: false, interval: time.Minute}
	withSystemTaskRegistry(r, handler)

	r.runSystemTaskScheduler(t.Context())
	assert.Equal(t, int64(0), countSystemTasks(t, r, handler.taskType))
}

func TestSystemTaskClaimPassDispatchesByType(t *testing.T) {
	r := newTaskFixture(t)

	ran := make(chan stubSystemTaskRunResult, 1)
	handler := &stubScheduledHandler{
		taskType: "test_dispatch",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskType: task.Type,
				err:      r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	withSystemTaskRegistry(r, handler)

	_, err := r.CreateSystemTask(t.Context(), handler.taskType, nil, nil)
	require.NoError(t, err)

	r.runSystemTaskClaimPass(t.Context(), "runner-dispatch")

	select {
	case got := <-ran:
		require.NoError(t, got.err)
		assert.Equal(t, handler.taskType, got.taskType)
	case <-time.After(2 * time.Second):
		t.Fatal("claimed task was not dispatched to its handler")
	}

	require.Eventually(t, func() bool {
		latest, err := r.GetLatestSystemTask(t.Context(), handler.taskType)
		return err == nil && latest != nil && latest.Status == SystemTaskStatusSucceeded
	}, 2*time.Second, 20*time.Millisecond)
}

func TestSystemTaskClaimPassDispatchesEarliestPendingByType(t *testing.T) {
	r := newTaskFixture(t)

	ran := make(chan stubSystemTaskRunResult, 2)
	handlerA := &stubScheduledHandler{
		taskType: "test_dispatch_a",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskID: task.TaskID,
				err:    r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	handlerB := &stubScheduledHandler{
		taskType: "test_dispatch_b",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskID: task.TaskID,
				err:    r.FinishSystemTask(t.Context(), task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	withSystemTaskRegistry(r, handlerA, handlerB)

	firstA, err := r.CreateSystemTask(t.Context(), handlerA.taskType, nil, nil)
	require.NoError(t, err)
	secondTaskID, err := GenerateSystemTaskID()
	require.NoError(t, err)
	secondA := &SystemTask{
		TaskID: secondTaskID,
		Type:   handlerA.taskType,
		Status: SystemTaskStatusPending,
	}
	require.NoError(t, r.db.Create(secondA).Error)
	firstB, err := r.CreateSystemTask(t.Context(), handlerB.taskType, nil, nil)
	require.NoError(t, err)

	r.runSystemTaskClaimPass(t.Context(), "runner-dispatch")

	got := map[string]bool{}
	for range 2 {
		select {
		case result := <-ran:
			require.NoError(t, result.err)
			got[result.taskID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("claimed tasks were not dispatched to their handlers")
		}
	}

	assert.True(t, got[firstA.TaskID])
	assert.True(t, got[firstB.TaskID])
	assert.False(t, got[secondA.TaskID])

	require.Eventually(t, func() bool {
		reloaded, err := r.GetSystemTaskByTaskID(t.Context(), secondA.TaskID)
		return err == nil && reloaded != nil && reloaded.Status == SystemTaskStatusPending
	}, 2*time.Second, 20*time.Millisecond)
}

func TestEnqueueSystemTaskReportsCreatedAndExistingActive(t *testing.T) {
	r := newTaskFixture(t)

	first, created, err := r.EnqueueSystemTask(t.Context(), "test_enqueue", map[string]bool{"manual": true})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first)

	existing, created, err := r.EnqueueSystemTask(t.Context(), "test_enqueue", nil)
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, existing)
	assert.Equal(t, first.TaskID, existing.TaskID)

	_, claimed, err := r.ClaimSystemTask(t.Context(), first.ID, first.Type, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.FinishSystemTask(t.Context(), first.TaskID, "runner-a", SystemTaskStatusSucceeded, nil, ""))

	second, created, err := r.EnqueueSystemTask(t.Context(), "test_enqueue", nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, second)
	assert.NotEqual(t, first.TaskID, second.TaskID)
}

func TestRunnerRegistryIsInstanceScoped(t *testing.T) {
	first := newTaskFixture(t)
	second := newTaskFixture(t)
	first.RegisterSystemTaskHandler(&stubScheduledHandler{taskType: "instance-only", enabled: true, interval: time.Minute})
	first.runSystemTaskScheduler(t.Context())
	second.runSystemTaskScheduler(t.Context())
	assert.Equal(t, int64(1), countSystemTasks(t, first, "instance-only"))
	assert.Equal(t, int64(0), countSystemTasks(t, second, "instance-only"))
}

func TestTaskExecutionReceivesApplicationCancellation(t *testing.T) {
	r := newTaskFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		r.runWithLeaseHeartbeat(ctx, &SystemTask{TaskID: "cancelled-task"}, "runner", func(ctx context.Context) { close(started); <-ctx.Done() })
		close(finished)
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not receive application cancellation")
	}
}
