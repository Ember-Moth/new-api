package service

import (
	"fmt"
	"time"

	"context"

	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/types"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type TokenDetails struct {
	TextTokens  int
	AudioTokens int
}

type QuotaInfo struct {
	InputDetails         TokenDetails
	OutputDetails        TokenDetails
	ModelName            string
	UsePrice             bool
	ModelPrice           float64
	ModelRatio           float64
	GroupRatio           float64
	QuotaPerUnit         float64
	CompletionRatio      float64
	AudioRatio           float64
	AudioCompletionRatio float64
}

func hasCustomModelRatio(modelName string, currentRatio float64, relayInfos ...*relaycommon.RelayInfo) bool {
	defaultRatios := ratio_setting.GetDefaultModelRatioMap()
	if len(relayInfos) > 0 && relayInfos[0] != nil && relayInfos[0].ConfigSnapshot != nil {
		defaultRatios = relayInfos[0].ConfigSnapshot.Pricing.DefaultModelRatio
	}
	defaultRatio, exists := defaultRatios[modelName]
	if !exists {
		return true
	}
	return currentRatio != defaultRatio
}

func calculateAudioQuota(info QuotaInfo) (int, *common.QuotaClamp) {
	if info.UsePrice {
		modelPrice := decimal.NewFromFloat(info.ModelPrice)
		quotaPerUnit := decimal.NewFromFloat(info.QuotaPerUnit)
		groupRatio := decimal.NewFromFloat(info.GroupRatio)

		quota := modelPrice.Mul(quotaPerUnit).Mul(groupRatio)
		return common.QuotaFromDecimalChecked(quota)
	}

	completionRatio := decimal.NewFromFloat(info.CompletionRatio)
	audioRatio := decimal.NewFromFloat(info.AudioRatio)
	audioCompletionRatio := decimal.NewFromFloat(info.AudioCompletionRatio)

	groupRatio := decimal.NewFromFloat(info.GroupRatio)
	modelRatio := decimal.NewFromFloat(info.ModelRatio)
	ratio := groupRatio.Mul(modelRatio)

	inputTextTokens := decimal.NewFromInt(int64(info.InputDetails.TextTokens))
	outputTextTokens := decimal.NewFromInt(int64(info.OutputDetails.TextTokens))
	inputAudioTokens := decimal.NewFromInt(int64(info.InputDetails.AudioTokens))
	outputAudioTokens := decimal.NewFromInt(int64(info.OutputDetails.AudioTokens))

	quota := decimal.Zero
	quota = quota.Add(inputTextTokens)
	quota = quota.Add(outputTextTokens.Mul(completionRatio))
	quota = quota.Add(inputAudioTokens.Mul(audioRatio))
	quota = quota.Add(outputAudioTokens.Mul(audioRatio).Mul(audioCompletionRatio))

	quota = quota.Mul(ratio)

	// If ratio is not zero and quota is less than or equal to zero, set quota to 1
	if !ratio.IsZero() && quota.LessThanOrEqual(decimal.Zero) {
		quota = decimal.NewFromInt(1)
	}

	return common.QuotaFromDecimalChecked(quota)
}

func PreWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.RealtimeUsage) error {
	if relayInfo == nil || usage == nil {
		return fmt.Errorf("realtime billing usage is missing")
	}
	if relayInfo.PriceData.UsePrice {
		return nil
	}

	modelName := relayInfo.GetBillingModelName()
	textInputTokens := usage.InputTokenDetails.TextTokens
	textOutTokens := usage.OutputTokenDetails.TextTokens
	audioInputTokens := usage.InputTokenDetails.AudioTokens
	audioOutTokens := usage.OutputTokenDetails.AudioTokens

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            modelName,
		UsePrice:             relayInfo.PriceData.UsePrice,
		ModelRatio:           relayInfo.PriceData.ModelRatio,
		GroupRatio:           relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		QuotaPerUnit:         relayInfo.QuotaPerUnit(),
		CompletionRatio:      relayInfo.PriceData.CompletionRatio,
		AudioRatio:           relayInfo.PriceData.AudioRatio,
		AudioCompletionRatio: relayInfo.PriceData.AudioCompletionRatio,
	}

	var quota int
	if tiered, tieredQuota, _ := TryTieredSettle(relayInfo, realtimeTieredTokenParams(relayInfo, usage)); tiered {
		quota = tieredQuota
	} else {
		var clamp *common.QuotaClamp
		quota, clamp = calculateAudioQuota(quotaInfo)
		noteQuotaClamp(relayInfo, clamp)
	}

	if relayInfo.Billing == nil {
		if relayInfo.PriceData.FreeModel {
			return nil
		}
		return fmt.Errorf("realtime request has no durable billing session")
	}
	if err := relayInfo.Billing.Reserve(quota); err != nil {
		return err
	}
	logger.LogInfo(ctx, fmt.Sprintf("realtime cumulative quota reserved: %d", quota))
	return nil
}

// realtimeTieredTokenParams keeps cumulative reservation and final settlement
// on the same token-normalization rules, including separately priced audio/cache.
func realtimeTieredTokenParams(info *relaycommon.RelayInfo, usage *dto.RealtimeUsage) billingexpr.TokenParams {
	var usedVars map[string]bool
	if info.TieredBillingSnapshot != nil {
		usedVars = billingexpr.UsedVars(info.TieredBillingSnapshot.ExprString)
	}
	return BuildTieredTokenParams(&dto.Usage{
		PromptTokens:           usage.InputTokens,
		CompletionTokens:       usage.OutputTokens,
		PromptTokensDetails:    usage.InputTokenDetails,
		CompletionTokenDetails: usage.OutputTokenDetails,
	}, false, usedVars)
}

func PostWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelName string,
	usage *dto.RealtimeUsage, extraContent string) error {

	var tieredResult *billingexpr.TieredResult
	tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, realtimeTieredTokenParams(relayInfo, usage))
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.InputTokenDetails.TextTokens
	textOutTokens := usage.OutputTokenDetails.TextTokens

	audioInputTokens := usage.InputTokenDetails.AudioTokens
	audioOutTokens := usage.OutputTokenDetails.AudioTokens

	tokenName := ctx.GetString("token_name")
	completionRatio := relayInfo.PriceData.CompletionRatio
	audioRatio := relayInfo.PriceData.AudioRatio
	audioCompletionRatio := relayInfo.PriceData.AudioCompletionRatio

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            modelName,
		UsePrice:             usePrice,
		ModelRatio:           modelRatio,
		GroupRatio:           groupRatio,
		QuotaPerUnit:         relayInfo.QuotaPerUnit(),
		CompletionRatio:      completionRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
	}

	quota := tieredQuota
	if !tieredOk {
		var clamp *common.QuotaClamp
		quota, clamp = calculateAudioQuota(quotaInfo)
		noteQuotaClamp(relayInfo, clamp)
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// record all the consume log even if quota is 0
	if totalTokens == 0 {
		// in this case, must be some error happened
		// we cannot just return, because we may have to return the pre-consumed quota
		quota = 0
		logContent += "（可能是上游超时）"
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
			"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, modelName, relayInfo.FinalPreConsumedQuota))
	}

	billingErr := SettleBilling(ctx, relayInfo, quota, totalTokens != 0)
	if billingErr != nil {
		logger.LogError(ctx, "billing settlement pending: "+billingErr.Error())
	}

	logModel := modelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateWssOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	if billingErr != nil {
		other.SetPublic("billing_status", "pending")
		other.SetAdmin("billing_request_id", relayInfo.RequestId)
	} else {
		other.SetPublic("billing_status", "settled")
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	return billingErr
}

func CalcOpenRouterCacheCreateTokens(usage dto.Usage, priceData types.PriceData, relayInfos ...*relaycommon.RelayInfo) int {
	if priceData.CacheCreationRatio == 1 {
		return 0
	}
	quotaPerUnit := common.QuotaPerUnit
	if len(relayInfos) > 0 && relayInfos[0] != nil {
		quotaPerUnit = relayInfos[0].QuotaPerUnit()
	}
	quotaPrice := priceData.ModelRatio / quotaPerUnit
	promptCacheCreatePrice := quotaPrice * priceData.CacheCreationRatio
	promptCacheReadPrice := quotaPrice * priceData.CacheRatio
	completionPrice := quotaPrice * priceData.CompletionRatio

	cost, _ := usage.Cost.(float64)
	totalPromptTokens := float64(usage.PromptTokens)
	completionTokens := float64(usage.CompletionTokens)
	promptCacheReadTokens := float64(usage.PromptTokensDetails.CachedTokens)

	value := (cost -
		totalPromptTokens*quotaPrice +
		promptCacheReadTokens*(quotaPrice-promptCacheReadPrice) -
		completionTokens*completionPrice) /
		(promptCacheCreatePrice - quotaPrice)
	quota, clamp := common.QuotaRoundChecked(value)
	if clamp != nil {
		return -1
	}
	return quota
}

func PostAudioConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent string) {

	var tieredUsedVars map[string]bool
	if snap := relayInfo.TieredBillingSnapshot; snap != nil {
		tieredUsedVars = billingexpr.UsedVars(snap.ExprString)
	}
	var tieredResult *billingexpr.TieredResult
	tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, BuildTieredTokenParams(usage, false, tieredUsedVars))
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.PromptTokensDetails.TextTokens
	textOutTokens := usage.CompletionTokenDetails.TextTokens

	audioInputTokens := usage.PromptTokensDetails.AudioTokens
	audioOutTokens := usage.CompletionTokenDetails.AudioTokens

	tokenName := ctx.GetString("token_name")
	billingModelName := relayInfo.GetBillingModelName()
	completionRatio := relayInfo.PriceData.CompletionRatio
	audioRatio := relayInfo.PriceData.AudioRatio
	audioCompletionRatio := relayInfo.PriceData.AudioCompletionRatio

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            billingModelName,
		UsePrice:             usePrice,
		ModelRatio:           modelRatio,
		GroupRatio:           groupRatio,
		QuotaPerUnit:         relayInfo.QuotaPerUnit(),
		CompletionRatio:      completionRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
	}

	quota := tieredQuota
	if !tieredOk {
		var clamp *common.QuotaClamp
		quota, clamp = calculateAudioQuota(quotaInfo)
		noteQuotaClamp(relayInfo, clamp)
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// record all the consume log even if quota is 0
	if totalTokens == 0 {
		// in this case, must be some error happened
		// we cannot just return, because we may have to return the pre-consumed quota
		quota = 0
		logContent += "（可能是上游超时）"
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
			"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, billingModelName, relayInfo.FinalPreConsumedQuota))
	}

	billingErr := SettleBilling(ctx, relayInfo, quota, totalTokens != 0)
	if billingErr != nil {
		logger.LogError(ctx, "billing settlement pending: "+billingErr.Error())
	}

	logModel := billingModelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateAudioOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	if billingErr != nil {
		other.SetPublic("billing_status", "pending")
		other.SetAdmin("billing_request_id", relayInfo.RequestId)
	} else {
		other.SetPublic("billing_status", "settled")
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	gopool.Go(func() {
		RecordRelayPerfSample(relayInfo, true, int64(usage.CompletionTokens))
	})
}

type postConsumeQuotaResult = billingcontract.QuotaAdjustment

// applyQuotaAdjustment applies a stable business event once, including its
// usage counters. A cache or notification error must never repeat the debit.
func applyQuotaAdjustment(info *relaycommon.RelayInfo, adjustment billingcontract.BillingAdjustment, sendEmail bool) (postConsumeQuotaResult, error) {
	if info == nil || info.RequestId == "" {
		return postConsumeQuotaResult{}, fmt.Errorf("billing request identity is missing")
	}
	if adjustment.Source == "" {
		adjustment.Source = BillingSourceWallet
	}
	ctx, cancel := context.WithTimeout(context.Background(), billingOperationTimeout)
	defer cancel()
	result, err := billingEngine().ApplyAdjustment(ctx, billingRequest(info), adjustment)
	if !result.Replayed {
		info.SubscriptionPostDelta += result.SubscriptionPostDelta
	}
	if err == nil && !result.Replayed && sendEmail && adjustment.Delta != 0 {
		checkAndSendQuotaNotify(info, adjustment.Delta, 0)
	}
	return result, err
}

func checkAndSendQuotaNotify(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int) {
	gopool.Go(func() {
		userSetting := relayInfo.UserSetting
		threshold := common.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		//noMoreQuota := userCache.Quota-(quota+preConsumedQuota) <= 0
		quotaTooLow := false
		consumeQuota := quota + preConsumedQuota
		if relayInfo.UserQuota-consumeQuota < threshold {
			quotaTooLow = true
		}
		if quotaTooLow {
			prompt := "您的额度即将用尽"
			topUpLink := PaymentReturnURL("/wallet")

			// 根据通知方式生成不同的内容格式
			var content string
			var values []interface{}

			notifyType := userSetting.NotifyType
			if notifyType == "" {
				notifyType = dto.NotifyTypeEmail
			}

			if notifyType == dto.NotifyTypeBark {
				// Bark推送使用简短文本，不支持HTML
				content = "{{value}}，剩余额度：{{value}}，请及时充值"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else if notifyType == dto.NotifyTypeGotify {
				content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else {
				// 默认内容格式，适用于Email和Webhook（支持HTML）
				content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota), topUpLink, topUpLink}
			}

			err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values))
			if err != nil {
				common.SysError(fmt.Sprintf("failed to send quota notify to user %d: %s", relayInfo.UserId, err.Error()))
			}
		}
	})
}

func checkAndSendSubscriptionQuotaNotify(relayInfo *relaycommon.RelayInfo) {
	gopool.Go(func() {
		if relayInfo == nil {
			return
		}
		if relayInfo.SubscriptionId == 0 || relayInfo.SubscriptionAmountTotal <= 0 {
			return
		}

		userSetting := relayInfo.UserSetting
		threshold := common.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		usedAfter := relayInfo.SubscriptionAmountUsedAfterPreConsume + relayInfo.SubscriptionPostDelta
		remaining := relayInfo.SubscriptionAmountTotal - usedAfter
		if remaining >= int64(threshold) {
			return
		}

		prompt := "您的订阅额度即将用尽"
		topUpLink := PaymentReturnURL("/wallet")

		var content string
		var values []interface{}
		notifyType := userSetting.NotifyType
		if notifyType == "" {
			notifyType = dto.NotifyTypeEmail
		}

		if notifyType == dto.NotifyTypeBark {
			content = "{{value}}，剩余额度：{{value}}，请及时充值"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else if notifyType == dto.NotifyTypeGotify {
			content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else {
			content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining)), topUpLink, topUpLink}
		}

		if err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values)); err != nil {
			common.SysError(fmt.Sprintf("failed to send subscription quota notify to user %d: %s", relayInfo.UserId, err.Error()))
		}
	})
}
