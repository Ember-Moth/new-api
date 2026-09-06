package usage_test

import (
	"testing"

	"net/http"
	"net/http/httptest"

	"context"

	"github.com/QuantumNous/new-api/internal/module/usage"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/metadata"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLogCursorPaginationPreservesTiesAndViewerIsolation(t *testing.T) {
	primary, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	logsDB := testdb.Logs(t, primary)
	store := usage.New(usage.Dependencies{DB: logsDB})
	for _, content := range []string{"first", "second", "third", "fourth", "fifth"} {
		require.NoError(t, logsDB.Create(&usage.Log{UserId: 7, CreatedAt: 100, RequestId: "same-request", Content: content, Type: 2, Other: `{"admin_info":{"secret":"hidden"}}`}).Error)
	}
	require.NoError(t, logsDB.Create(&usage.Log{UserId: 8, CreatedAt: 100, RequestId: "same-request", Content: "other-user", Type: 2}).Error)
	var contents []string
	cursor := ""
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page, err := store.NewLogCursorPage(cursor, "self:7")
		require.NoError(t, err)
		rows, total, err := store.GetUserLogs(t.Context(), 7, 0, 0, 0, "", "", pageNumber*2, 2, "", "", "", page)
		require.NoError(t, err)
		assert.Zero(t, total, "cursor requests do not compute a total count")
		for _, row := range rows {
			contents = append(contents, row.Content)
			assert.NotContains(t, row.Other, "hidden")
		}
		if pageNumber == 0 {
			require.True(t, page.HasMore)
			_, err := store.NewLogCursorPage(page.NextCursor, "self:8")
			assert.ErrorIs(t, err, usage.ErrInvalidLogCursor)
			_, err = store.NewLogCursorPage(page.NextCursor+"tampered", "self:7")
			assert.ErrorIs(t, err, usage.ErrInvalidLogCursor)
			require.NoError(t, logsDB.Create(&usage.Log{UserId: 7, CreatedAt: 101, Content: "newer", Type: 2}).Error)
		}
		cursor = page.NextCursor
		assert.Equal(t, pageNumber < 2, page.HasMore)
	}
	assert.ElementsMatch(t, []string{"first", "second", "third", "fourth", "fifth"}, contents)
	cutoff := common.GetTimestamp()
	own := &usage.Log{UserId: 70, Username: "shared%", CreatedAt: cutoff, Type: 2, Quota: 50, PromptTokens: 3, CompletionTokens: 2, ModelName: `gpt_4\mini`, RequestId: "preserved-request", Other: `{"admin_info":{"secret":"private"}}`}
	foreign := &usage.Log{UserId: 80, Username: "shared-other", CreatedAt: cutoff, Type: 2, Quota: 99999, PromptTokens: 100, ModelName: `gptX4\mini`}
	require.NoError(t, store.Create(t.Context(), own))
	require.NoError(t, store.Create(t.Context(), foreign))
	assert.Equal(t, "preserved-request", own.RequestId)
	assert.NotEmpty(t, foreign.RequestId)
	filtered, total, err := store.GetAllLogs(t.Context(), 0, cutoff, 0, `gpt_4\mini%`, "", "", 0, 10, 0, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, filtered, 1)
	assert.Equal(t, own.RequestId, filtered[0].RequestId)
	handler := usagehttp.New(store)
	router := gin.New()
	router.GET("/self-stat", func(c *gin.Context) { c.Set("id", 70); c.Set("username", "shared%"); handler.GetLogsSelfStat(c) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/self-stat", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var statistic struct {
		Success bool       `json:"success"`
		Data    usage.Stat `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &statistic))
	require.True(t, statistic.Success, response.Body.String())
	assert.Equal(t, 50, statistic.Data.Quota)
	assert.Equal(t, 1, statistic.Data.Rpm)
	assert.Equal(t, 5, statistic.Data.Tpm)
	userLogs, _, err := store.GetUserLogs(t.Context(), 70, 0, 0, 0, "", "", 20, 10, "", "", "")
	require.NoError(t, err)
	assert.Empty(t, userLogs)
	userLogs, _, err = store.GetUserLogs(t.Context(), 70, 0, 0, 0, "", "", 0, 10, "", "", "")
	require.NoError(t, err)
	require.Len(t, userLogs, 1)
	assert.Equal(t, 1, userLogs[0].Id)
	assert.NotContains(t, userLogs[0].Other, "private")
	before, err := store.CountOldLog(t.Context(), cutoff)
	require.NoError(t, err)
	require.Positive(t, before)
	deleted, err := store.DeleteOldLogBatch(t.Context(), cutoff, 1)
	require.NoError(t, err)
	assert.Equal(t, before, deleted)
	after, err := store.CountOldLog(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, before-deleted, after)
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, true
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport })
	var exports []contract.QuotaDataLogParams
	writer := usage.New(usage.Dependencies{DB: logsDB, Writer: usage.WriterPolicy{
		Username:  func(context.Context, int) (string, error) { return "resolved-user", nil },
		TokenName: func(context.Context, int) (string, error) { return "resolved-token", nil },
		RecordIP:  func(ctx context.Context, id int) (bool, error) { return id == 90, nil },
		Export:    func(event contract.QuotaDataLogParams) { exports = append(exports, event) },
	}})
	request := contract.RequestMetadata{Username: "request-user", RequestID: "event-request", UpstreamRequestID: "upstream-request", ClientIP: "203.0.113.7"}
	other := metadata.NewLogOther()
	other.SetPublic("visible", "yes")
	other.SetAdmin("private", "admin-only")
	other.SetRoot("private", "root-only")
	writer.RecordConsumeLog(t.Context(), request, 90, contract.RecordConsumeLogParams{Quota: 123, PromptTokens: 4, CompletionTokens: 5, ModelName: "event-model", TokenName: "request-token", TokenId: 47, ChannelId: 9, Group: "gold", Content: "consume-event", Other: other})
	writer.RecordErrorLog(t.Context(), request, 91, 9, "event-model", "request-token", "error-event", 47, 2, false, "gold", other)
	writer.RecordTaskBillingLog(t.Context(), contract.RecordTaskBillingLogParams{UserId: 90, LogType: 2, Quota: 22, ModelName: "task-model", TokenId: 47, ChannelId: 9, Group: "gold", Content: "task-event", NodeName: "origin-node", Other: other})
	writer.RecordOperationAuditLog(t.Context(), 92, "audit-event", "203.0.113.9", "test.action", map[string]any{"target_user_id": 90}, map[string]any{"private": "operator"}, map[string]any{"private": "audit"})
	var storedEvents []usage.Log
	require.NoError(t, logsDB.Where("content IN ?", []string{"consume-event", "error-event", "task-event", "audit-event"}).Find(&storedEvents).Error)
	require.Len(t, storedEvents, 4)
	events := make(map[string]usage.Log)
	for _, event := range storedEvents {
		events[event.Content] = event
	}
	assert.Equal(t, 123, events["consume-event"].Quota)
	assert.Equal(t, "request-user", events["consume-event"].Username)
	assert.Equal(t, "event-request", events["consume-event"].RequestId)
	assert.Equal(t, "upstream-request", events["consume-event"].UpstreamRequestId)
	assert.Equal(t, "203.0.113.7", events["consume-event"].Ip)
	assert.Empty(t, events["error-event"].Ip)
	assert.Equal(t, "resolved-token", events["task-event"].TokenName)
	assert.Equal(t, "resolved-user", events["task-event"].Username)
	assert.NotEmpty(t, events["task-event"].RequestId)
	assert.Equal(t, 92, events["audit-event"].UserId)
	require.Len(t, exports, 2)
	assert.Equal(t, 9, exports[0].TokenUsed)
	assert.Equal(t, "origin-node", exports[1].NodeName)
	assert.Equal(t, "gold", exports[1].UseGroup)
	assert.Equal(t, 47, exports[1].TokenID)
	auditView, _, err := writer.GetUserLogs(t.Context(), 92, 0, 0, 0, "", "", 0, 10, "", "", "")
	require.NoError(t, err)
	require.Len(t, auditView, 1)
	assert.Contains(t, auditView[0].Other, "test.action")
	assert.NotContains(t, auditView[0].Other, "operator")
	assert.NotContains(t, auditView[0].Other, `"audit_info"`)
	common.LogConsumeEnabled = false
	writer.RecordConsumeLog(t.Context(), request, 90, contract.RecordConsumeLogParams{Content: "disabled-event"})
	var disabledCount int64
	require.NoError(t, logsDB.Model(&usage.Log{}).Where("content = ?", "disabled-event").Count(&disabledCount).Error)
	assert.Zero(t, disabledCount)
	assert.Len(t, exports, 2)

}
