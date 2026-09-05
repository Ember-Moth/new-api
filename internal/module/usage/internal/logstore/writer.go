package logs

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/metadata"
	"github.com/QuantumNous/new-api/logger"
)

type WriterPolicy struct {
	Username  func(context.Context, int) (string, error)
	TokenName func(context.Context, int) (string, error)
	RecordIP  func(context.Context, int) (bool, error)
	Export    func(contract.QuotaDataLogParams)
}

type Writer struct {
	store  *Store
	policy WriterPolicy
}

func NewWriter(store *Store, policy WriterPolicy) *Writer {
	return &Writer{store: store, policy: policy}
}

func (r *Writer) username(ctx context.Context, id int) (string, error) {
	if r.policy.Username == nil {
		return "", nil
	}
	return r.policy.Username(ctx, id)
}

func (r *Writer) RecordLog(ctx context.Context, userId int, logType int, content string) {
	if logType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := r.username(ctx, userId)
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   content,
	}
	err := r.store.Create(ctx, log)
	if err != nil {
		common.SysLog("failed to record log: " + err.Error())
	}
}

// RecordLogWithAdminInfo 记录操作日志，并将管理员相关信息存入 Other.admin_info，
func (r *Writer) RecordLogWithAdminInfo(ctx context.Context, userId int, logType int, content string, adminInfo map[string]interface{}) {
	if logType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := r.username(ctx, userId)
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   content,
	}
	if len(adminInfo) > 0 {
		other := metadata.NewLogOther()
		other.MergeAdmin(adminInfo)
		log.Other = other.JSONString()
	}
	if err := r.store.Create(ctx, log); err != nil {
		common.SysLog("failed to record log: " + err.Error())
	}
}

// RecordLoginLog 记录用户登录成功的审计日志（type=LogTypeLogin）。
// username 由调用方传入（登录流程已持有用户对象），避免额外的数据库查询。
// content 为英文兜底文本（用于导出）；action+params 供前端本地化渲染。
// extra 可携带 login_method、user_agent 等附加信息（普通用户可见）。
func (r *Writer) RecordLoginLog(ctx context.Context, userId int, username string, content string, ip string, action string, params map[string]interface{}, extra map[string]interface{}) {
	other := metadata.NewLogOther()
	other.MergePublic(extra)
	other.SetPublic("op", buildOpField(action, params))
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeLogin,
		Content:   content,
		Ip:        ip,
		Other:     other.JSONString(),
	}
	if err := r.store.Create(ctx, log); err != nil {
		common.SysLog("failed to record login log: " + err.Error())
	}
}

// RecordOperationAuditLog 记录管理/高危操作审计日志（type=LogTypeManage）。
// logUserId 为日志归属者，管理审计日志应归属实际操作者；目标资源/用户放入
// action params。username 内部按 logUserId 查询。content 为英文兜底文本（供导出使用）。
// action+params 写入 Other.op，供前端本地化渲染（普通用户可见，不含敏感信息）。
// adminInfo 存放操作者身份（写入 Other.admin_info，普通用户查询时剥离）；
// auditInfo 存放路由/方法/结果等中间件兜底信息（写入 Other.audit_info，普通用户查询时剥离）。
func (r *Writer) RecordOperationAuditLog(ctx context.Context, logUserId int, content string, ip string, action string, params map[string]interface{}, adminInfo map[string]interface{}, auditInfo map[string]interface{}) {
	username, _ := r.username(ctx, logUserId)
	other := metadata.NewLogOther()
	other.SetPublic("op", buildOpField(action, params))
	other.MergeAdmin(adminInfo)
	other.MergeAudit(auditInfo)
	log := &Log{
		UserId:    logUserId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeManage,
		Content:   content,
		Ip:        ip,
		Other:     other.JSONString(),
	}
	if err := r.store.Create(ctx, log); err != nil {
		common.SysLog("failed to record operation audit log: " + err.Error())
	}
}

func (r *Writer) RecordTopupLog(ctx context.Context, userId int, content string, callerIp string, paymentMethod string, callbackPaymentMethod string) {
	username, _ := r.username(ctx, userId)
	other := metadata.NewLogOther()
	other.MergeAdmin(map[string]interface{}{
		"server_ip":               common.GetIp(),
		"node_name":               common.NodeName,
		"caller_ip":               callerIp,
		"payment_method":          paymentMethod,
		"callback_payment_method": callbackPaymentMethod,
		"version":                 common.Version,
	})
	log := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeTopup,
		Content:   content,
		Ip:        callerIp,
		Other:     other.JSONString(),
	}
	err := r.store.Create(ctx, log)
	if err != nil {
		common.SysLog("failed to record topup log: " + err.Error())
	}
}

func (r *Writer) RecordErrorLog(ctx context.Context, request contract.RequestMetadata, userId int, channelId int, modelName string, tokenName string, content string, tokenId int, useTimeSeconds int,
	isStream bool, group string, other *metadata.LogOther) {
	logger.LogInfo(ctx, fmt.Sprintf("record error log: userId=%d, channelId=%d, modelName=%s, tokenName=%s, content=%s", userId, channelId, modelName, tokenName, common.LocalLogPreview(content)))
	username := request.Username
	requestId := request.RequestID
	upstreamRequestId := request.UpstreamRequestID
	otherStr := other.JSONString()
	// 判断是否需要记录 IP
	needRecordIp := false
	if r.policy.RecordIP != nil {
		needRecordIp, _ = r.policy.RecordIP(ctx, userId)
	}
	log := &Log{
		UserId:           userId,
		Username:         username,
		CreatedAt:        common.GetTimestamp(),
		Type:             LogTypeError,
		Content:          content,
		PromptTokens:     0,
		CompletionTokens: 0,
		TokenName:        tokenName,
		ModelName:        modelName,
		Quota:            0,
		ChannelId:        channelId,
		TokenId:          tokenId,
		UseTime:          useTimeSeconds,
		IsStream:         isStream,
		Group:            group,
		Ip: func() string {
			if needRecordIp {
				return request.ClientIP
			}
			return ""
		}(),
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherStr,
	}
	err := r.store.Create(ctx, log)
	if err != nil {
		logger.LogError(ctx, "failed to record log: "+err.Error())
	}
}

func (r *Writer) RecordConsumeLog(ctx context.Context, request contract.RequestMetadata, userId int, params contract.RecordConsumeLogParams) {
	if !common.LogConsumeEnabled {
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("record consume log: userId=%d, params=%s", userId, common.GetJsonString(params)))
	username := request.Username
	requestId := request.RequestID
	upstreamRequestId := request.UpstreamRequestID
	createdAt := common.GetTimestamp()
	otherStr := params.Other.JSONString()
	// 判断是否需要记录 IP
	needRecordIp := false
	if r.policy.RecordIP != nil {
		needRecordIp, _ = r.policy.RecordIP(ctx, userId)
	}
	log := &Log{
		UserId:           userId,
		Username:         username,
		CreatedAt:        createdAt,
		Type:             LogTypeConsume,
		Content:          params.Content,
		PromptTokens:     params.PromptTokens,
		CompletionTokens: params.CompletionTokens,
		TokenName:        params.TokenName,
		ModelName:        params.ModelName,
		Quota:            params.Quota,
		ChannelId:        params.ChannelId,
		TokenId:          params.TokenId,
		UseTime:          params.UseTimeSeconds,
		IsStream:         params.IsStream,
		Group:            params.Group,
		Ip: func() string {
			if needRecordIp {
				return request.ClientIP
			}
			return ""
		}(),
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherStr,
	}
	err := r.store.Create(ctx, log)
	if err != nil {
		logger.LogError(ctx, "failed to record log: "+err.Error())
	}
	if common.DataExportEnabled && r.policy.Export != nil {
		r.policy.Export(contract.QuotaDataLogParams{
			UserID:    userId,
			Username:  username,
			ModelName: params.ModelName,
			Quota:     params.Quota,
			CreatedAt: createdAt,
			TokenUsed: params.PromptTokens + params.CompletionTokens,
			UseGroup:  params.Group,
			TokenID:   params.TokenId,
			ChannelID: params.ChannelId,
			NodeName:  common.NodeName,
		})
	}
}

func (r *Writer) RecordTaskBillingLog(ctx context.Context, params contract.RecordTaskBillingLogParams) {
	if params.LogType == LogTypeConsume && !common.LogConsumeEnabled {
		return
	}
	username, _ := r.username(ctx, params.UserId)
	tokenName := ""
	if params.TokenId > 0 && r.policy.TokenName != nil {
		tokenName, _ = r.policy.TokenName(ctx, params.TokenId)
	}
	createdAt := common.GetTimestamp()
	log := &Log{
		UserId:    params.UserId,
		Username:  username,
		CreatedAt: createdAt,
		Type:      params.LogType,
		Content:   params.Content,
		TokenName: tokenName,
		ModelName: params.ModelName,
		Quota:     params.Quota,
		ChannelId: params.ChannelId,
		TokenId:   params.TokenId,
		Group:     params.Group,
		Other:     params.Other.JSONString(),
	}
	err := r.store.Create(ctx, log)
	if err != nil {
		common.SysLog("failed to record task billing log: " + err.Error())
	}
	if params.LogType == LogTypeConsume && common.DataExportEnabled && r.policy.Export != nil {
		nodeName := params.NodeName
		if nodeName == "" {
			nodeName = common.NodeName
		}
		r.policy.Export(contract.QuotaDataLogParams{
			UserID:    params.UserId,
			Username:  username,
			ModelName: params.ModelName,
			Quota:     params.Quota,
			CreatedAt: createdAt,
			UseGroup:  params.Group,
			TokenID:   params.TokenId,
			ChannelID: params.ChannelId,
			NodeName:  nodeName,
		})
	}
}

// buildOpField 构建语言无关的操作描述（写入 Other.op）。
// 前端依据 action(稳定操作标识) + params(结构化参数) 在渲染期用 i18n 本地化展示，
// 因此不在数据库中存储自然语言句子。
func buildOpField(action string, params map[string]interface{}) map[string]interface{} {
	op := map[string]interface{}{
		"action": action,
	}
	if len(params) > 0 {
		op["params"] = params
	}
	return op
}
