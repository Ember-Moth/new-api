package common

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	settingconfig "github.com/QuantumNous/new-api/internal/config/setting/config"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoKeepsPublishedRequestConfigAcrossGenerationChanges(t *testing.T) {
	previous := settingconfig.CurrentRequestConfigSnapshot()
	t.Cleanup(func() { settingconfig.PublishRequestConfigSnapshot(previous) })

	first := &settingconfig.RequestConfigSnapshot{
		Pricing: settingconfig.RequestPricingSnapshot{
			ModelRatio:      map[string]float64{"snapshot-model": 1},
			GroupRatio:      map[string]float64{"default": 1},
			QuotaPerUnit:    100,
			CompletionRatio: map[string]float64{"snapshot-model": 2},
			ToolPrices:      map[string]float64{"web_search": 10},
		},
		Billing: settingconfig.RequestBillingSnapshot{
			BillingMode: map[string]string{"snapshot-model": "tiered_expr"},
			BillingExpr: map[string]string{"snapshot-model": `tier("old", p * 1)`},
		},
		Conversion: &settingconfig.RequestConversionSnapshot{
			GlobalPassThroughRequestEnabled: true,
			Claude: &settingconfig.ClaudeConversionSnapshot{
				DefaultMaxTokens: map[string]int{"default": 10},
			},
			Gemini: &settingconfig.GeminiConversionSnapshot{
				SafetySettings: map[string]string{"default": "BLOCK_NONE"},
			},
		},
	}
	settingconfig.PublishRequestConfigSnapshot(first)
	old := &RelayInfo{ConfigSnapshot: settingconfig.CurrentRequestConfigSnapshot()}
	oldOptions := old.ConvOptions()

	second := &settingconfig.RequestConfigSnapshot{
		Pricing: settingconfig.RequestPricingSnapshot{
			ModelRatio:   map[string]float64{"snapshot-model": 3},
			GroupRatio:   map[string]float64{"default": 4},
			QuotaPerUnit: 200,
		},
		Billing: settingconfig.RequestBillingSnapshot{
			BillingMode: map[string]string{"snapshot-model": "ratio"},
			BillingExpr: map[string]string{"snapshot-model": `tier("new", p * 3)`},
		},
		Conversion: &settingconfig.RequestConversionSnapshot{
			GlobalPassThroughRequestEnabled: false,
			Claude: &settingconfig.ClaudeConversionSnapshot{
				DefaultMaxTokens: map[string]int{"default": 20},
			},
			Gemini: &settingconfig.GeminiConversionSnapshot{
				SafetySettings: map[string]string{"default": "OFF"},
			},
		},
	}
	settingconfig.PublishRequestConfigSnapshot(second)
	newInfo := &RelayInfo{ConfigSnapshot: settingconfig.CurrentRequestConfigSnapshot()}

	assert.Equal(t, 1.0, old.ConfigSnapshot.Pricing.ModelRatio["snapshot-model"])
	assert.Equal(t, 3.0, newInfo.ConfigSnapshot.Pricing.ModelRatio["snapshot-model"])
	oldModelRatio, _, _ := old.ModelRatio("snapshot-model")
	newModelRatio, _, _ := newInfo.ModelRatio("snapshot-model")
	assert.Equal(t, 1.0, oldModelRatio)
	assert.Equal(t, 3.0, newModelRatio)
	assert.Equal(t, 1.0, old.GroupRatio("default"))
	assert.Equal(t, 4.0, newInfo.GroupRatio("default"))
	oldExpr, oldExprOK := old.BillingExpr("snapshot-model")
	newExpr, newExprOK := newInfo.BillingExpr("snapshot-model")
	assert.True(t, oldExprOK)
	assert.True(t, newExprOK)
	assert.Equal(t, `tier("old", p * 1)`, oldExpr)
	assert.Equal(t, `tier("new", p * 3)`, newExpr)
	assert.Equal(t, 100.0, old.QuotaPerUnit())
	assert.Equal(t, 200.0, newInfo.QuotaPerUnit())
	assert.True(t, old.GlobalPassThroughRequestEnabled())
	assert.False(t, newInfo.GlobalPassThroughRequestEnabled())
	oldMax, _ := oldOptions.Claude.DefaultMaxTokensFor("other")
	newMax, _ := newInfo.ConvOptions().Claude.DefaultMaxTokensFor("other")
	assert.Equal(t, 10, oldMax)
	assert.Equal(t, 20, newMax)
	assert.Equal(t, "BLOCK_NONE", oldOptions.Gemini.SafetySettingFor("unknown"))
	assert.Equal(t, "OFF", newInfo.ConvOptions().Gemini.SafetySettingFor("unknown"))

	oldSnapshot := old.ConfigSnapshot
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	old.RelayFormat = types.RelayFormatOpenAI
	old.OriginModelName = "snapshot-model"
	old.InitChannelMeta(ctx)
	assert.Same(t, oldSnapshot, old.ConfigSnapshot)
	assert.Equal(t, 100.0, old.QuotaPerUnit())
	oldAfterRetryMax, _ := old.ConvOptions().Claude.DefaultMaxTokensFor("other")
	assert.Equal(t, 10, oldAfterRetryMax)
}

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoMetaTypedNilReceiver(t *testing.T) {
	var info *RelayInfo
	var meta convmeta.Meta = info

	assert.Empty(t, meta.GetOriginModelName())
	assert.Empty(t, meta.GetUpstreamModelName())
	assert.False(t, meta.HasChannelMeta())
	assert.Zero(t, meta.GetChannelID())
	assert.Zero(t, meta.GetChannelType())
	assert.False(t, meta.GetIsStream())
	assert.Empty(t, meta.GetReasoningEffort())
	assert.Nil(t, meta.ReasoningState())
	assert.Zero(t, meta.GetEstimatePromptTokens())
	assert.Zero(t, meta.GetSendResponseCount())

	assert.NotPanics(t, func() {
		meta.SetReasoningEffort("high")
		meta.IncrSendResponseCount()
		meta.AppendRequestConversion(types.RelayFormatClaude)
	})

	firstState := meta.EnsureClaudeConvertInfo()
	secondState := meta.EnsureClaudeConvertInfo()
	require.NotNil(t, firstState)
	require.NotNil(t, secondState)
	assert.Equal(t, convmeta.LastMessageTypeNone, firstState.LastMessagesType)
	assert.NotSame(t, firstState, secondState)

	firstOptions := meta.ConvOptions()
	secondOptions := meta.ConvOptions()
	require.NotNil(t, firstOptions)
	require.NotNil(t, secondOptions)
	assert.NotSame(t, firstOptions, secondOptions)
	assert.NotNil(t, firstOptions.Claude.DefaultMaxTokens)
	assert.NotNil(t, firstOptions.Gemini.SupportsImagine)
	assert.NotNil(t, firstOptions.Gemini.SafetySetting)
	assert.NotNil(t, firstOptions.PreserveThinkingSuffix)
	assert.NotNil(t, firstOptions.PreserveEffortTail)
}

func TestGenRelayInfoCapturesRequestReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		path        string
		relayFormat types.RelayFormat
		request     dto.Request
		expected    string
	}{
		{
			name:        "OpenAI chat top-level effort",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "gpt-5.6-sol", ReasoningEffort: " high "},
			expected:    "high",
		},
		{
			name:        "OpenRouter nested chat effort",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "anthropic/claude", Reasoning: json.RawMessage(`{"effort":"xhigh"}`)},
			expected:    "xhigh",
		},
		{
			name:        "OpenAI Responses effort",
			path:        "/v1/responses",
			relayFormat: types.RelayFormatOpenAIResponses,
			request:     &dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol", Reasoning: &dto.Reasoning{Effort: "max"}},
			expected:    "max",
		},
		{
			name:        "explicit none is preserved",
			path:        "/v1/responses",
			relayFormat: types.RelayFormatOpenAIResponses,
			request:     &dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol", Reasoning: &dto.Reasoning{Effort: "none"}},
			expected:    "none",
		},
		{
			name:        "non-string nested effort is ignored",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "anthropic/claude", Reasoning: json.RawMessage(`{"effort":42}`)},
			expected:    "",
		},
		{
			name:        "Claude output config effort",
			path:        "/v1/messages",
			relayFormat: types.RelayFormatClaude,
			request:     &dto.ClaudeRequest{Model: "claude-opus-4-7", OutputConfig: json.RawMessage(`{"effort":"medium"}`)},
			expected:    "medium",
		},
		{
			name:        "Gemini thinking level",
			path:        "/v1beta/models/gemini-3-pro:generateContent",
			relayFormat: types.RelayFormatGemini,
			request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "low"},
			}},
			expected: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", tt.path, nil)

			info, err := GenRelayInfo(ctx, tt.relayFormat, tt.request, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, info.ReasoningEffort)
		})
	}
}

func TestGenRelayInfoKeepsOriginAndLeavesBillingUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	const model = "qwen3.8-max@thinking:on@temperature:0.2"
	ctx.Set("original_model", model)

	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Model: model}, nil)
	require.NoError(t, err)
	assert.Equal(t, model, info.OriginModelName)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, model, info.GetBillingModelName())
}

func TestInitChannelMetaRestoresRequestReasoningEffortForRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	request := &dto.OpenAIResponsesRequest{
		Model:     "gpt-5.6-sol",
		Reasoning: &dto.Reasoning{Effort: "max"},
	}
	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAIResponses, request, nil)
	require.NoError(t, err)

	info.SetReasoningEffort("high")
	info.InitChannelMeta(ctx)
	assert.Equal(t, "max", info.ReasoningEffort)

	info.SetReasoningEffort("low")
	info.InitChannelMeta(ctx)
	assert.Equal(t, "max", info.ReasoningEffort)
}

func TestInitChannelMetaResetsPerAttemptStreamStateAndPreservesRequestState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Model: "gpt-test"}, nil)
	require.NoError(t, err)

	claudeState := relayconvert.NewClaudeToChatStreamState()
	_, err = claudeState.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: ptr(7),
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "tool_use",
			Id:   "toolu_1",
			Name: "lookup",
		},
	})
	require.NoError(t, err)
	_, err = claudeState.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: ptr(7),
		Delta: &dto.ClaudeMediaMessage{
			Type:        "input_json_delta",
			PartialJson: ptr(`{"q":"x"}`),
		},
	})
	require.NoError(t, err)

	geminiState, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatGemini, relayconvert.ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)

	info.SendResponseCount = 3
	info.ClaudeToChatStreamState = claudeState
	info.ChatToGeminiStreamState = geminiState
	info.LastError = types.NewError(assert.AnError, types.ErrorCodeBadResponseBody)
	info.StreamStatus = NewStreamStatus()
	info.StreamStatus.RecordError("attempt 1 soft error")
	info.RecordConversionDiagnostics(context.Background(), []types.ConversionDiagnostic{{
		Code:     "test.loss",
		Message:  "attempt 1 conversion loss",
		Severity: types.ConversionDiagnosticWarning,
		From:     types.RelayFormatClaude,
		To:       types.RelayFormatOpenAI,
	}})

	info.InitChannelMeta(ctx)

	assert.Zero(t, info.SendResponseCount)
	assert.Nil(t, info.ClaudeToChatStreamState)
	assert.Nil(t, info.ChatToGeminiStreamState)

	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.Equal(t, 1, info.StreamStatus.TotalErrorCount())
	diagnostics := info.ConversionDiagnostics()
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "test.loss", diagnostics[0].Code)
	require.NotNil(t, info.LastError)

	freshClaude := relayconvert.NewClaudeToChatStreamState()
	_, err = freshClaude.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: ptr(7),
		Delta: &dto.ClaudeMediaMessage{
			Type:        "input_json_delta",
			PartialJson: ptr(`{"q":"x"}`),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown content block index")

	info.IncrSendResponseCount()
	responses := relayconvert.StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_retry",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
	}, info)
	require.NotEmpty(t, responses)
	assert.Equal(t, "message_start", responses[0].Type)
}

func ptr[T any](value T) *T {
	return &value
}
