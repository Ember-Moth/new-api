package model

import (
	"context"

	usagecontract "github.com/QuantumNous/new-api/internal/module/usage/contract"

	"github.com/QuantumNous/new-api/internal/module/usage"

	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/gin-gonic/gin"
)

// don't use iota, avoid change log type value
const (
	LogTypeUnknown = 0
	LogTypeTopup   = 1
	LogTypeConsume = 2
	LogTypeManage  = 3
	LogTypeSystem  = 4
	LogTypeError   = 5
	LogTypeRefund  = 6
	LogTypeLogin   = 7
)

type Log = usage.Log
type Stat = usage.Stat

func FormatAdminLogs(logs []*Log) { usage.FormatAdminLogs(logs) }

func FormatRootLogs(logs []*Log) { usage.FormatRootLogs(logs) }

func formatUserLogs(logs []*Log, offset int) { usage.FormatUserLogs(logs, offset) }

func LogService() *usage.Service {
	return usage.New(usage.Dependencies{DB: LOG_DB, ChannelNames: ChannelService().ChannelNames, Writer: LogWriterPolicy()})
}

func GetLogByTokenId(tokenId int) (logs []*Log, err error) {
	return LogService().GetLogByTokenId(context.Background(), tokenId)
}

func GetAllLogs(logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string, startIdx int, num int, channel int, group string, requestId string, upstreamRequestId string, cursorPages ...*LogCursorPage) (logs []*Log, total int64, err error) {
	return LogService().GetAllLogs(context.Background(), logType, startTimestamp, endTimestamp, modelName, username, tokenName, startIdx, num, channel, group, requestId, upstreamRequestId, cursorPages...)
}

func GetUserLogs(userId int, logType int, startTimestamp int64, endTimestamp int64, modelName string, tokenName string, startIdx int, num int, group string, requestId string, upstreamRequestId string, cursorPages ...*LogCursorPage) (logs []*Log, total int64, err error) {
	return LogService().GetUserLogs(context.Background(), userId, logType, startTimestamp, endTimestamp, modelName, tokenName, startIdx, num, group, requestId, upstreamRequestId, cursorPages...)
}

func SumUsedQuota(logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string, channel int, group string) (stat Stat, err error) {
	return LogService().SumUsedQuota(context.Background(), logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
}

func SumUsedToken(logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string) (token int) {
	return LogService().SumUsedToken(context.Background(), logType, startTimestamp, endTimestamp, modelName, username, tokenName)
}

func CountOldLog(ctx context.Context, targetTimestamp int64) (int64, error) {
	return LogService().CountOldLog(ctx, targetTimestamp)
}

func DeleteOldLogBatch(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	return LogService().DeleteOldLogBatch(ctx, targetTimestamp, limit)
}

type RecordConsumeLogParams = usagecontract.RecordConsumeLogParams
type RecordTaskBillingLogParams = usagecontract.RecordTaskBillingLogParams

func LogWriterPolicy() usage.WriterPolicy {
	return usage.WriterPolicy{
		Username: func(ctx context.Context, id int) (string, error) { return GetUsernameById(id, false) },
		TokenName: func(ctx context.Context, id int) (string, error) {
			token, err := GetTokenById(id)
			if err != nil {
				return "", err
			}
			return token.Name, nil
		},
		RecordIP: func(ctx context.Context, id int) (bool, error) {
			settings, err := GetUserSetting(id, false)
			return settings.RecordIpLog, err
		},
		Export: QuotaDataStore().Record,
	}
}

func logRequestMetadata(c *gin.Context) usagecontract.RequestMetadata {
	if c == nil {
		return usagecontract.RequestMetadata{}
	}
	request := usagecontract.RequestMetadata{Username: c.GetString("username"), RequestID: c.GetString(common.RequestIdKey), UpstreamRequestID: c.GetString(common.UpstreamRequestIdKey)}
	if c.Request != nil {
		request.ClientIP = c.ClientIP()
	}
	return request
}

func RecordLog(userId int, logType int, content string) {
	LogService().RecordLog(context.Background(), userId, logType, content)
}

func RecordLogWithAdminInfo(userId int, logType int, content string, adminInfo map[string]interface{}) {
	LogService().RecordLogWithAdminInfo(context.Background(), userId, logType, content, adminInfo)
}

func RecordLoginLog(userId int, username string, content string, ip string, action string, params map[string]interface{}, extra map[string]interface{}) {
	LogService().RecordLoginLog(context.Background(), userId, username, content, ip, action, params, extra)
}

func RecordOperationAuditLog(logUserId int, content string, ip string, action string, params map[string]interface{}, adminInfo map[string]interface{}, auditInfo map[string]interface{}) {
	LogService().RecordOperationAuditLog(context.Background(), logUserId, content, ip, action, params, adminInfo, auditInfo)
}

func RecordTopupLog(userId int, content string, callerIp string, paymentMethod string, callbackPaymentMethod string) {
	LogService().RecordTopupLog(context.Background(), userId, content, callerIp, paymentMethod, callbackPaymentMethod)
}

func RecordErrorLog(c *gin.Context, userId int, channelId int, modelName string, tokenName string, content string, tokenId int, useTimeSeconds int,
	isStream bool, group string, other *LogOther) {
	LogService().RecordErrorLog(c, logRequestMetadata(c), userId, channelId, modelName, tokenName, content, tokenId, useTimeSeconds, isStream, group, other)
}

func RecordConsumeLog(c *gin.Context, userId int, params RecordConsumeLogParams) {
	LogService().RecordConsumeLog(c, logRequestMetadata(c), userId, params)
}

func RecordTaskBillingLog(params RecordTaskBillingLogParams) {
	LogService().RecordTaskBillingLog(context.Background(), params)
}
