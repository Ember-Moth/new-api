package contract

import "github.com/QuantumNous/new-api/internal/module/usage/metadata"

type RequestMetadata struct {
	Username          string
	RequestID         string
	UpstreamRequestID string
	ClientIP          string
}

type RecordConsumeLogParams struct {
	ChannelId        int                `json:"channel_id"`
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	ModelName        string             `json:"model_name"`
	TokenName        string             `json:"token_name"`
	Quota            int                `json:"quota"`
	Content          string             `json:"content"`
	TokenId          int                `json:"token_id"`
	UseTimeSeconds   int                `json:"use_time_seconds"`
	IsStream         bool               `json:"is_stream"`
	Group            string             `json:"group"`
	Other            *metadata.LogOther `json:"other"`
}

type RecordTaskBillingLogParams struct {
	UserId    int
	LogType   int
	Content   string
	ChannelId int
	ModelName string
	Quota     int
	TokenId   int
	Group     string
	Other     *metadata.LogOther
	NodeName  string // 任务发起节点；为空时回退当前节点
}

type QuotaDataLogParams struct {
	UserID    int
	Username  string
	ModelName string
	Quota     int
	CreatedAt int64
	TokenUsed int
	UseGroup  string
	TokenID   int
	ChannelID int
	NodeName  string
}
