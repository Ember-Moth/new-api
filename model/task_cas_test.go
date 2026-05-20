package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	db, cleanup, err := testutil.OpenPostgresTestDBFromEnv("model_task_cas")
	if errors.Is(err, testutil.ErrMissingPostgresDSN) {
		fmt.Fprintln(os.Stderr, "skipping model task CAS tests: set TEST_POSTGRES_DSN to run PostgreSQL database tests")
		os.Exit(0)
	}
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	DB = db
	LOG_DB = db

	testutil.ConfigurePostgresTestGlobals()
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true

	if err := db.AutoMigrate(
		&Task{},
		&User{},
		&Token{},
		&Log{},
		&Channel{},
		&Midjourney{},
		&TopUp{},
		&QuotaData{},
		&QuotaDataDaily{},
		&QuotaDataMonthly{},
		&SubscriptionPlan{},
		&SubscriptionOrder{},
		&UserSubscription{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_quota_data_hour ON quota_data (user_id, username, model_name, created_at)`).Error; err != nil {
		panic("failed to create quota data unique index: " + err.Error())
	}
	if err := ensurePostgresQuotaDataRollups(db); err != nil {
		panic("failed to create quota data rollups: " + err.Error())
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func truncateTables(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		testutil.TruncateTables(t, DB,
			"tasks",
			"users",
			"tokens",
			"logs",
			"channels",
			"midjourneys",
			"top_ups",
			"quota_data",
			"quota_data_daily",
			"quota_data_monthly",
			"subscription_orders",
			"subscription_plans",
			"user_subscriptions",
		)
	})
}

func TestClaimUnfinishedSyncTasksUsesPollingLease(t *testing.T) {
	truncateTables(t)
	t.Setenv(taskPollingLeaseSecondsEnv, "300")

	for i := 0; i < 3; i++ {
		insertTask(t, &Task{
			TaskID:     fmt.Sprintf("task_claim_%d", i),
			Status:     TaskStatusInProgress,
			Progress:   "50%",
			SubmitTime: int64(100 + i),
			Data:       json.RawMessage(`{}`),
		})
	}

	firstBatch := GetAllUnFinishSyncTasks(2)
	require.Len(t, firstBatch, 2)
	assert.NotZero(t, firstBatch[0].PollingAt)
	assert.NotEqual(t, firstBatch[0].ID, firstBatch[1].ID)

	secondBatch := GetAllUnFinishSyncTasks(2)
	require.Len(t, secondBatch, 1)
	assert.NotContains(t, []int64{firstBatch[0].ID, firstBatch[1].ID}, secondBatch[0].ID)

	require.NoError(t, ReleaseTaskPolling(firstBatch[0].ID, firstBatch[0].PollingAt))
	thirdBatch := GetAllUnFinishSyncTasks(2)
	require.Len(t, thirdBatch, 1)
	assert.Equal(t, firstBatch[0].ID, thirdBatch[0].ID)
}

func TestClaimTimedOutUnfinishedTasksUsesPollingLease(t *testing.T) {
	truncateTables(t)
	t.Setenv(taskPollingLeaseSecondsEnv, "300")

	insertTask(t, &Task{
		TaskID:     "task_timeout_claimed",
		Status:     TaskStatusInProgress,
		Progress:   "50%",
		SubmitTime: 100,
		Data:       json.RawMessage(`{}`),
	})
	insertTask(t, &Task{
		TaskID:     "task_timeout_fresh",
		Status:     TaskStatusInProgress,
		Progress:   "50%",
		SubmitTime: 1000,
		Data:       json.RawMessage(`{}`),
	})

	firstBatch := GetTimedOutUnfinishedTasks(500, 10)
	require.Len(t, firstBatch, 1)
	assert.Equal(t, "task_timeout_claimed", firstBatch[0].TaskID)

	secondBatch := GetTimedOutUnfinishedTasks(500, 10)
	assert.Empty(t, secondBatch)
}

func TestClaimMidjourneyTasksUsesPollingLease(t *testing.T) {
	truncateTables(t)
	t.Setenv(taskPollingLeaseSecondsEnv, "300")

	require.NoError(t, DB.Create(&Midjourney{MjId: "mj_claim_1", Progress: "50%", Status: "IN_PROGRESS"}).Error)
	require.NoError(t, DB.Create(&Midjourney{MjId: "mj_claim_2", Progress: "50%", Status: "IN_PROGRESS"}).Error)

	firstBatch := GetAllUnFinishTasks()
	require.Len(t, firstBatch, 2)

	secondBatch := GetAllUnFinishTasks()
	assert.Empty(t, secondBatch)

	require.NoError(t, ReleaseMidjourneyPolling(firstBatch[0].Id, firstBatch[0].PollingAt))
	thirdBatch := GetAllUnFinishTasks()
	require.Len(t, thirdBatch, 1)
	assert.Equal(t, firstBatch[0].Id, thirdBatch[0].Id)
}

func TestExpireDueSubscriptionsProcessesAllUsersInBatch(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()

	require.NoError(t, DB.Create(&User{Id: 601, Username: "expire_keep_user", Password: "password123", Status: common.UserStatusEnabled, Group: "vip", AffCode: "expire_keep"}).Error)
	require.NoError(t, DB.Create(&User{Id: 602, Username: "expire_downgrade_user", Password: "password123", Status: common.UserStatusEnabled, Group: "vip", AffCode: "expire_down"}).Error)
	require.NoError(t, DB.Create(&UserSubscription{
		UserId:        601,
		PlanId:        901,
		AmountTotal:   1000,
		EndTime:       now - 10,
		Status:        "active",
		UpgradeGroup:  "vip",
		PrevUserGroup: "default",
	}).Error)
	require.NoError(t, DB.Create(&UserSubscription{
		UserId:        601,
		PlanId:        901,
		AmountTotal:   1000,
		EndTime:       now + 3600,
		Status:        "active",
		UpgradeGroup:  "vip",
		PrevUserGroup: "default",
	}).Error)
	require.NoError(t, DB.Create(&UserSubscription{
		UserId:        602,
		PlanId:        901,
		AmountTotal:   1000,
		EndTime:       now - 5,
		Status:        "active",
		UpgradeGroup:  "vip",
		PrevUserGroup: "default",
	}).Error)

	expired, err := ExpireDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 2, expired)

	var expiredCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("status = ? AND end_time <= ?", "expired", now).
		Count(&expiredCount).Error)
	assert.Equal(t, int64(2), expiredCount)
	assert.Equal(t, "vip", getUserGroupForTaskCASTest(t, 601))
	assert.Equal(t, "default", getUserGroupForTaskCASTest(t, 602))
}

func TestResetDueSubscriptionsHonorsLimit(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:                      902,
		Title:                   "Reset Plan",
		PriceAmount:             1,
		Currency:                "USD",
		DurationUnit:            SubscriptionDurationMonth,
		DurationValue:           1,
		Enabled:                 true,
		TotalAmount:             1000,
		QuotaResetPeriod:        SubscriptionResetCustom,
		QuotaResetCustomSeconds: 60,
	}
	require.NoError(t, DB.Create(plan).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, DB.Create(&UserSubscription{
			UserId:        701 + i,
			PlanId:        plan.Id,
			AmountTotal:   1000,
			AmountUsed:    int64(100 + i),
			StartTime:     now - 3600,
			EndTime:       now + 3600,
			Status:        "active",
			LastResetTime: now - 120,
			NextResetTime: now - 60,
		}).Error)
	}

	reset, err := ResetDueSubscriptions(1)
	require.NoError(t, err)
	assert.Equal(t, 1, reset)
	assert.Equal(t, int64(1), countResetSubscriptionsForTaskCASTest(t))

	reset, err = ResetDueSubscriptions(1)
	require.NoError(t, err)
	assert.Equal(t, 1, reset)
	assert.Equal(t, int64(2), countResetSubscriptionsForTaskCASTest(t))
}

func getUserGroupForTaskCASTest(t *testing.T, userID int) string {
	t.Helper()
	var group string
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Select(commonGroupCol).Scan(&group).Error)
	return group
}

func countResetSubscriptionsForTaskCASTest(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("amount_used = 0").Count(&count).Error)
	return count
}

func insertTask(t *testing.T, task *Task) {
	t.Helper()
	task.CreatedAt = time.Now().Unix()
	task.UpdatedAt = time.Now().Unix()
	require.NoError(t, DB.Create(task).Error)
}

// ---------------------------------------------------------------------------
// Snapshot / Equal — pure logic tests (no DB)
// ---------------------------------------------------------------------------

func TestSnapshotEqual_Same(t *testing.T) {
	s := taskSnapshot{
		Status:     TaskStatusInProgress,
		Progress:   "50%",
		StartTime:  1000,
		FinishTime: 0,
		FailReason: "",
		ResultURL:  "",
		Data:       json.RawMessage(`{"key":"value"}`),
	}
	assert.True(t, s.Equal(s))
}

func TestSnapshotEqual_DifferentStatus(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusSuccess, Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentProgress(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Progress: "30%", Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Progress: "60%", Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentData(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":1}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":2}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_NilVsEmpty(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: nil}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage{}}
	// bytes.Equal(nil, []byte{}) == true
	assert.True(t, a.Equal(b))
}

func TestSnapshot_Roundtrip(t *testing.T) {
	task := &Task{
		Status:     TaskStatusInProgress,
		Progress:   "42%",
		StartTime:  1234,
		FinishTime: 5678,
		FailReason: "timeout",
		PrivateData: TaskPrivateData{
			ResultURL: "https://example.com/result.mp4",
		},
		Data: json.RawMessage(`{"model":"test-model"}`),
	}
	snap := task.Snapshot()
	assert.Equal(t, task.Status, snap.Status)
	assert.Equal(t, task.Progress, snap.Progress)
	assert.Equal(t, task.StartTime, snap.StartTime)
	assert.Equal(t, task.FinishTime, snap.FinishTime)
	assert.Equal(t, task.FailReason, snap.FailReason)
	assert.Equal(t, task.PrivateData.ResultURL, snap.ResultURL)
	assert.JSONEq(t, string(task.Data), string(snap.Data))
}

// ---------------------------------------------------------------------------
// UpdateWithStatus CAS — DB integration tests
// ---------------------------------------------------------------------------

func TestUpdateWithStatus_Win(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_win",
		Status:   TaskStatusInProgress,
		Progress: "50%",
		Data:     json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.True(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, "100%", reloaded.Progress)
}

func TestUpdateWithStatus_Lose(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_lose",
		Status: TaskStatusFailure,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	won, err := task.UpdateWithStatus(TaskStatusInProgress) // wrong fromStatus
	require.NoError(t, err)
	assert.False(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusFailure, reloaded.Status) // unchanged
}

func TestUpdateWithStatus_ConcurrentWinner(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_race",
		Status: TaskStatusInProgress,
		Quota:  1000,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	const goroutines = 5
	wins := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			t := &Task{}
			*t = Task{
				ID:       task.ID,
				TaskID:   task.TaskID,
				Status:   TaskStatusSuccess,
				Progress: "100%",
				Quota:    task.Quota,
				Data:     json.RawMessage(`{}`),
			}
			t.CreatedAt = task.CreatedAt
			t.UpdatedAt = time.Now().Unix()
			won, err := t.UpdateWithStatus(TaskStatusInProgress)
			if err == nil {
				wins[idx] = won
			}
		}(i)
	}
	wg.Wait()

	winCount := 0
	for _, w := range wins {
		if w {
			winCount++
		}
	}
	assert.Equal(t, 1, winCount, "exactly one goroutine should win the CAS")
}
