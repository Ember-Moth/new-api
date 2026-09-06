package service

import (
	"fmt"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
)

const (
	ViolationFeeCodePrefix     = "violation_fee."
	CSAMViolationMarker        = "Failed check: SAFETY_CHECK_TYPE"
	ContentViolatesUsageMarker = "Content violates usage guidelines"
)

func IsViolationFeeCode(code types.ErrorCode) bool {
	return strings.HasPrefix(string(code), ViolationFeeCodePrefix)
}

func HasCSAMViolationMarker(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), CSAMViolationMarker) || strings.Contains(err.Error(), ContentViolatesUsageMarker) {
		return true
	}
	msg := err.ToOpenAIError().Message
	return strings.Contains(msg, CSAMViolationMarker) || strings.Contains(err.Error(), ContentViolatesUsageMarker)
}

func WrapAsViolationFeeGrokCSAM(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return nil
	}
	oai := err.ToOpenAIError()
	oai.Type = string(types.ErrorCodeViolationFeeGrokCSAM)
	oai.Code = string(types.ErrorCodeViolationFeeGrokCSAM)
	return types.WithOpenAIError(oai, err.StatusCode, types.ErrOptionWithSkipRetry())
}

// NormalizeViolationFeeError ensures:
// - if the CSAM marker is present, error.code is set to a stable violation-fee code and skip-retry is enabled.
// - if error.code already has the violation-fee prefix, skip-retry is enabled.
//
// It must be called before retry decision logic.
func NormalizeViolationFeeError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return nil
	}

	if HasCSAMViolationMarker(err) {
		return WrapAsViolationFeeGrokCSAM(err)
	}

	if IsViolationFeeCode(err.GetErrorCode()) {
		oai := err.ToOpenAIError()
		return types.WithOpenAIError(oai, err.StatusCode, types.ErrOptionWithSkipRetry())
	}

	return err
}

func shouldChargeViolationFee(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.GetErrorCode() == types.ErrorCodeViolationFeeGrokCSAM {
		return true
	}
	// In case some callers didn't normalize, keep a safety net.
	return HasCSAMViolationMarker(err)
}

func calcViolationFeeQuota(amount, groupRatio float64, quotaPerUnits ...float64) int {
	if amount <= 0 {
		return 0
	}
	if groupRatio <= 0 {
		return 0
	}
	quotaPerUnit := common.QuotaPerUnit
	if len(quotaPerUnits) > 0 {
		quotaPerUnit = quotaPerUnits[0]
	}
	quota := common.QuotaFromDecimal(decimal.NewFromFloat(amount).
		Mul(decimal.NewFromFloat(quotaPerUnit)).
		Mul(decimal.NewFromFloat(groupRatio)).
		Round(0))
	if quota <= 0 {
		return 0
	}
	return quota
}

// ChargeViolationFeeIfNeeded charges an additional fee after the normal flow finishes (including refund).
// It uses Grok fee settings as the fee policy.
func ChargeViolationFeeIfNeeded(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError) bool {
	if ctx == nil || relayInfo == nil || apiErr == nil {
		return false
	}
	//if relayInfo.IsPlayground {
	//	return false
	//}
	if !shouldChargeViolationFee(apiErr) {
		return false
	}

	feeEnabled, feeAmount := relayInfo.GrokViolationDeduction()
	if !feeEnabled {
		return false
	}

	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	feeQuota := calcViolationFeeQuota(feeAmount, groupRatio, relayInfo.QuotaPerUnit())
	if feeQuota <= 0 {
		return false
	}

	result, err := applyQuotaAdjustment(relayInfo, billingcontract.BillingAdjustment{
		OperationID:    "request:" + relayInfo.RequestId + ":violation_fee",
		Source:         relayInfo.BillingSource,
		SubscriptionID: relayInfo.SubscriptionId,
		Delta:          feeQuota,
		UsageDelta:     feeQuota,
		RequestDelta:   1,
		ChannelID:      relayInfo.ChannelId,
	}, true)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("failed to charge violation fee: %s", err.Error()))
		return false
	}
	if result.Replayed {
		return true
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	tokenName := ctx.GetString("token_name")
	oai := apiErr.ToOpenAIError()

	other := model.NewLogOther()
	other.MergePublic(map[string]interface{}{
		"violation_fee":        true,
		"violation_fee_code":   string(types.ErrorCodeViolationFeeGrokCSAM),
		"fee_quota":            feeQuota,
		"base_amount":          feeAmount,
		"group_ratio":          groupRatio,
		"status_code":          apiErr.StatusCode,
		"upstream_error_type":  oai.Type,
		"upstream_error_code":  fmt.Sprintf("%v", oai.Code),
		"violation_fee_marker": CSAMViolationMarker,
	})

	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:      relayInfo.ChannelId,
		ModelName:      relayInfo.OriginModelName,
		TokenName:      tokenName,
		Quota:          feeQuota,
		Content:        "Violation fee charged",
		TokenId:        relayInfo.TokenId,
		UseTimeSeconds: int(useTimeSeconds),
		IsStream:       relayInfo.IsStream,
		Group:          relayInfo.UsingGroup,
		Other:          other,
	})

	return true
}
