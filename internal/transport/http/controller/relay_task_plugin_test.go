package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/relay"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/shared/dto"
	"github.com/QuantumNous/new-api/internal/shared/types"
	"github.com/QuantumNous/new-api/internal/testdb"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPresentTaskSubmissionUsesNativePresenterAfterPersistence(t *testing.T) {
	plugin, err := pluginruntime.CompilePlugin(`
export const meta = {apiVersion:1,key:"presenter-test",name:"Presenter",version:"1.0.0",author:{name:"Test"},models:["model"],fetchMode:"per_task",routes:[{method:"POST",path:"/vendor/jobs",type:"submit",decode:"decode",render:"created"}]};
export const native = {decode:function(ctx){return {kind:"submit",model:"model",requestBody:ctx.body.value};},created:function(ctx,task){return {data:{task_id:task.task_id},upstream:task.data};}};
export function buildSubmitRequest(){return {}} export function parseSubmitResponse(){return {taskId:"upstream"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`, pluginruntime.Options{})
	require.NoError(t, err)
	priceData := types.PriceData{}
	priceData.AddOtherRatio("seconds", 5)
	task := &model.Task{TaskID: "task_public", SubmitTime: 123}
	task.SetData(map[string]any{"task_id": "upstream_private"})
	outcome := &taskSubmissionOutcome{
		Result:    &relay.TaskSubmitResult{},
		Task:      task,
		RelayInfo: &relaycommon.RelayInfo{PriceData: priceData},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{"model":"model"}`))
	c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Plugin: plugin, Route: plugin.Meta.Routes[0]})
	c.Set(pluginruntime.ContextKeyRouteRequest, pluginruntime.RouteRequestContext{Path: "/vendor/jobs", Method: http.MethodPost, Body: map[string]any{"kind": "json", "value": map[string]any{"model": "model"}}})

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{
		"data":{"task_id":"task_public"},
		"upstream":{"task_id":"upstream_private"}
	}`, recorder.Body.String())
	assert.JSONEq(t, `{"seconds":5}`, recorder.Header().Get("X-New-Api-Other-Ratios"))
}

func TestPresentTaskSubmissionFallbackUsesPersistedPublicID(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	outcome := &taskSubmissionOutcome{
		Result:    &relay.TaskSubmitResult{},
		Task:      &model.Task{TaskID: "task_persisted", SubmitTime: 456},
		RelayInfo: &relaycommon.RelayInfo{OriginModelName: "video-model"},
	}

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{
		"id":"task_persisted",
		"task_id":"task_persisted",
		"status":"queued",
		"model":"video-model",
		"created_at":456
	}`, recorder.Body.String())
}

func TestPresentTaskSubmissionUsesHostOpenAIVideoCreateReceipt(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Protocol:  "openai_video",
		Operation: pluginruntime.HostProtocolOperation{Name: "create"},
	})
	task := &model.Task{
		TaskID:     "task_public",
		Status:     model.TaskStatusSubmitted,
		Progress:   "0%",
		CreatedAt:  456,
		Properties: model.Properties{OriginModelName: "video-model"},
	}
	outcome := &taskSubmissionOutcome{Result: &relay.TaskSubmitResult{}, Task: task, RelayInfo: &relaycommon.RelayInfo{}}

	presentTaskSubmission(c, outcome)

	assert.JSONEq(t, `{"id":"task_public","object":"video","model":"video-model","status":"queued","progress":0,"created_at":456}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "task_id")
}

func TestExecuteTaskSubmissionDoesNotRefundWhenInsertFails(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, true)
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(t, 10, "request-task-insert-fail")
	require.NoError(t, service.MarkBillingDispatch(info))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
			Quota:          10,
		}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_insert_failed", taskErr.Code)
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 0, 0, 0)
	record := taskSubmissionBillingRecord(t, database, info.RequestId)
	assert.Equal(t, "active", record.Status)
	assert.Equal(t, "reconcile", record.PendingAction)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionMissingUpstreamIDLeavesReconciliation(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(t, 10, "request-task-missing-upstream")
	require.NoError(t, service.MarkBillingDispatch(info))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{Platform: constant.TaskPlatform("plugin"), Quota: 10}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_submission_outcome_unknown", taskErr.Code)
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 0, 0, 0)
	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&persisted).Error)
	assert.True(t, persisted.BillingPending)
	assert.Equal(t, "unknown", persisted.BillingAction)
	assert.Equal(t, 10, persisted.BillingTargetQuota)
	record := taskSubmissionBillingRecord(t, database, info.RequestId)
	assert.Equal(t, "active", record.Status)
	assert.Equal(t, "reconcile", record.PendingAction)
}

func TestExecuteTaskSubmissionSettlementFailureStaysDurableAndWritesNothing(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	failTaskUpdate := true
	require.NoError(t, database.Callback().Update().Before("gorm:update").Register("test:task-billing-fail-update", func(tx *gorm.DB) {
		if failTaskUpdate && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Task" {
			tx.AddError(errors.New("settlement failed"))
		}
	}))
	c := taskSubmissionTestContext()
	info := taskSubmissionRelayInfo(t, 10, "request-task-settlement-fail")
	require.NoError(t, service.MarkBillingDispatch(info))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
			Quota:          10,
		}, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_billing_settlement_failed", taskErr.Code)
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 0, 0, 0)
	var count int64
	require.NoError(t, database.Model(&model.Task{}).Where("task_id = ?", "task_public").Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&persisted).Error)
	assert.True(t, persisted.BillingPending)
	assert.Equal(t, "submission", persisted.BillingAction)
	assert.Equal(t, 10, persisted.BillingTargetQuota)
	record := taskSubmissionBillingRecord(t, database, info.RequestId)
	assert.Equal(t, "active", record.Status)
	assert.Equal(t, "settle", record.PendingAction)
	assert.True(t, record.IntentRequiresCommit)
	assert.False(t, c.Writer.Written())

	failTaskUpdate = false
	require.NoError(t, service.RunPendingTaskBilling(context.Background()))
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 10, 1, 10)
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&persisted).Error)
	assert.False(t, persisted.BillingPending)
}

func TestExecuteTaskSubmissionPersistsPinnedPluginProvenance(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })

	c := taskSubmissionTestContext()
	c.Set(common.RequestIdKey, "request-public")
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{
		Generation: &pluginruntime.RoutingGeneration{Number: 42},
		Plugin: &pluginruntime.LoadedPlugin{Meta: pluginruntime.Meta{
			Key:        "document-parser",
			Name:       "Document Parser",
			Version:    "1.2.3",
			APIVersion: 1,
			Author: pluginruntime.AuthorMeta{
				Name: "Community Author",
				URL:  "https://plugins.example/author",
			},
		}},
	})
	info := taskSubmissionRelayInfo(t, 0, "request-public")
	require.NoError(t, service.MarkBillingDispatch(info))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream-private",
			Platform:       constant.TaskPlatform("document-parser"),
		}, nil
	})

	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	require.NotNil(t, outcome.Task.PrivateData.Execution)
	require.NotNil(t, outcome.Task.PrivateData.Execution.TaskPlugin)
	assert.Equal(t, "request-public", outcome.Task.PrivateData.Execution.RequestID)
	assert.Equal(t, "/plugin/submit", outcome.Task.PrivateData.Execution.RequestPath)
	assert.Equal(t, "1.2.3", outcome.Task.PrivateData.Execution.TaskPlugin.Version)
	assert.Equal(t, uint64(42), outcome.Task.PrivateData.Execution.TaskPlugin.Generation)
	require.NotNil(t, outcome.Task.PrivateData.Execution.TaskPlugin.Author)
	assert.Equal(t, "Community Author", outcome.Task.PrivateData.Execution.TaskPlugin.Author.Name)
	assert.Equal(t, "https://plugins.example/author", outcome.Task.PrivateData.Execution.TaskPlugin.Author.URL)

	var stored model.Task
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&stored).Error)
	require.NotNil(t, stored.PrivateData.Execution)
	require.NotNil(t, stored.PrivateData.Execution.TaskPlugin)
	assert.Equal(t, "document-parser", stored.PrivateData.Execution.TaskPlugin.Key)
	require.NotNil(t, stored.PrivateData.Execution.TaskPlugin.Author)
	assert.Equal(t, "Community Author", stored.PrivateData.Execution.TaskPlugin.Author.Name)
	assert.Equal(t, "upstream-private", stored.PrivateData.UpstreamTaskID)
	assertTaskBillingBalances(t, database, 1, 1, 1, 100_000, 100_000, 0, 0, 1, 0)
}

func TestExecuteTaskSubmissionAcceptedResultAfterCancellationPersistsAndSettles(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(t, 10, "request-task-cancel-after-submit")
	require.NoError(t, service.MarkBillingDispatch(info))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		cancel()
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
			Quota:          10,
		}, nil
	})

	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 1, 10, 10)
	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_public").First(&persisted).Error)
	assert.False(t, persisted.BillingPending)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionDisconnectBeforeUpstreamAcceptanceSkipsSubmitAndRefunds(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(t, 10, "request-task-rejected-before-send")
	cancel()
	submitted := false

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		submitted = true
		return nil, nil
	})

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "request_cancelled", taskErr.Code)
	assert.False(t, submitted)
	assertTaskBillingBalances(t, database, 1, 1, 1, 100_000, 100_000, 0, 0, 0, 0)
	record := taskSubmissionBillingRecord(t, database, info.RequestId)
	assert.Equal(t, "refunded", record.Status)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionCallerCancellationDuringSubmitRefundsBeforeDurableBarrier(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(t, 10, "request-task-cancel-during-submit")
	submitStarted := make(chan struct{})
	done := make(chan struct{})
	var outcome *taskSubmissionOutcome
	var taskErr *dto.TaskError

	go func() {
		defer close(done)
		outcome, taskErr = executeTaskSubmissionWith(c, info, func(c *gin.Context, _ *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
			close(submitStarted)
			<-c.Request.Context().Done()
			return nil, service.TaskErrorWrapperLocal(c.Request.Context().Err(), "do_request_failed", http.StatusInternalServerError)
		})
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not stop after disconnect")
	}

	assert.Nil(t, outcome)
	require.NotNil(t, taskErr)
	assert.Equal(t, "request_cancelled", taskErr.Code)
	assertTaskBillingBalances(t, database, 1, 1, 1, 100_000, 100_000, 0, 0, 0, 0)
	record := taskSubmissionBillingRecord(t, database, info.RequestId)
	assert.Equal(t, "refunded", record.Status)
	assert.False(t, c.Writer.Written())
}

func TestExecuteTaskSubmissionCancellationAfterDurableInsertStillSettles(t *testing.T) {
	database := setupTaskSubmissionDatabase(t, false)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })
	c := taskSubmissionTestContext()
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	info := taskSubmissionRelayInfo(t, 10, "request-task-cancel-after-insert")
	require.NoError(t, service.MarkBillingDispatch(info))
	require.NoError(t, database.Callback().Create().After("gorm:create").Register("test:task-submit-cancel-after-insert", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Task" {
			cancel()
		}
	}))

	outcome, taskErr := executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
		return &relay.TaskSubmitResult{
			UpstreamTaskID: "upstream_private",
			Platform:       constant.TaskPlatform("plugin"),
			Quota:          10,
		}, nil
	})

	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	assert.Equal(t, "task_public", outcome.Task.TaskID)
	assertTaskBillingBalances(t, database, 1, 1, 1, 99_990, 99_990, 10, 1, 10, 10)
	var count int64
	require.NoError(t, database.Model(&model.Task{}).Where("task_id = ?", "task_public").Count(&count).Error)
	assert.Equal(t, int64(1), count)
	assert.False(t, c.Writer.Written())
}

func setupControllerBillingDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousDatabaseType := common.MainDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	previousBatchUpdate := common.BatchUpdateEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousRedisEnabled := common.RedisEnabled
	database, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(sqlDB))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousDatabaseType)
		common.MemoryCacheEnabled = previousMemoryCache
		common.BatchUpdateEnabled = previousBatchUpdate
		common.LogConsumeEnabled = previousLogConsume
		common.RedisEnabled = previousRedisEnabled
	})
	return database
}

func setupTaskSubmissionDatabase(t *testing.T, failInsert bool) *gorm.DB {
	t.Helper()
	database := setupControllerBillingDatabase(t)
	seedTaskBillingIdentity(t, database, 1, 1, 1, "sk-task-test", 100_000)
	if failInsert {
		require.NoError(t, database.Callback().Create().Before("gorm:create").Register("test:task-submit-fail-insert", func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Task" {
				tx.AddError(errors.New("task insert failed"))
			}
		}))
	}
	return database
}

func seedTaskBillingIdentity(t *testing.T, database *gorm.DB, userID, tokenID, channelID int, tokenKey string, quota int) {
	t.Helper()
	require.NoError(t, database.Create(&model.User{
		Id:       userID,
		Username: "task-billing-user",
		Group:    "default",
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, database.Create(&model.Token{
		Id:             tokenID,
		UserId:         userID,
		Key:            tokenKey,
		Name:           "task-billing-token",
		Status:         common.TokenStatusEnabled,
		RemainQuota:    quota,
		UnlimitedQuota: false,
	}).Error)
	if channelID > 0 {
		require.NoError(t, database.Create(&model.Channel{
			Id:     channelID,
			Type:   constant.ChannelTypeTaskPlugin,
			Name:   "task-billing-channel",
			Key:    "channel-key",
			Status: common.ChannelStatusEnabled,
		}).Error)
	}
}

func taskSubmissionTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/plugin/submit", strings.NewReader(`{}`))
	return c
}

func taskSubmissionRelayInfo(t *testing.T, preConsumed int, requestID string) *relaycommon.RelayInfo {
	t.Helper()
	info := &relaycommon.RelayInfo{
		UserId:          1,
		TokenId:         1,
		TokenKey:        "sk-task-test",
		TokenUnlimited:  false,
		UsingGroup:      "default",
		UserGroup:       "default",
		TokenGroup:      "default",
		UserQuota:       100_000,
		OriginModelName: "plugin-model",
		RequestId:       requestID,
		ForcePreConsume: true,
		ChannelId:       1,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		LockedChannel:   &model.Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Name: "plugin", Key: "channel-key"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			PublicTaskID:  "task_public",
			LockedChannel: &model.Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Name: "plugin"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ChannelType: constant.ChannelTypeTaskPlugin},
	}
	billing, apiErr := service.NewBillingSession(nil, info, preConsumed)
	require.Nil(t, apiErr)
	require.NotNil(t, billing)
	info.Billing = billing
	return info
}

type taskSubmissionBillingState struct {
	Status               string `gorm:"column:status"`
	PendingAction        string `gorm:"column:pending_action"`
	IntentRequiresCommit bool   `gorm:"column:intent_requires_commit"`
}

func taskSubmissionBillingRecord(t *testing.T, database *gorm.DB, requestID string) taskSubmissionBillingState {
	t.Helper()
	var record taskSubmissionBillingState
	require.NoError(t, database.Table("billing_sessions").Select("status", "pending_action", "intent_requires_commit").Where("request_id = ?", requestID).First(&record).Error)
	return record
}

func assertTaskBillingBalances(t *testing.T, database *gorm.DB, userID, tokenID, channelID, userQuota, tokenRemain, tokenUsed, userUsed, requestCount int, channelUsed int64) {
	t.Helper()
	var user model.User
	require.NoError(t, database.Select("quota", "used_quota", "request_count").Where("id = ?", userID).First(&user).Error)
	assert.Equal(t, userQuota, user.Quota)
	assert.Equal(t, userUsed, user.UsedQuota)
	assert.Equal(t, requestCount, user.RequestCount)
	var token model.Token
	require.NoError(t, database.Select("remain_quota", "used_quota").Where("id = ?", tokenID).First(&token).Error)
	assert.Equal(t, tokenRemain, token.RemainQuota)
	assert.Equal(t, tokenUsed, token.UsedQuota)
	var channel model.Channel
	require.NoError(t, database.Select("used_quota").Where("id = ?", channelID).First(&channel).Error)
	assert.Equal(t, channelUsed, channel.UsedQuota)
	return
}
