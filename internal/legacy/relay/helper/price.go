package helper

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/config/setting/billing_setting"
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/internal/config/setting/reasoning"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/shared/common"
	hosttypes "github.com/QuantumNous/new-api/internal/shared/types"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(modelName string, userId int) error {
	if model.IsAdmin(userId) {
		return fmt.Errorf(
			"模型 %s 的价格未配置。请前往「系统设置 → 运营设置」开启自用模式，或在「系统设置 → 分组与模型定价设置」中为该模型配置价格；"+
				"Model %s price not configured. Go to System Settings → Operation Settings to enable self-use mode, or configure the model price in System Settings → Group & Model Pricing.",
			modelName, modelName,
		)
	}
	return fmt.Errorf(
		"模型 %s 的价格尚未由管理员配置，暂时无法使用，请联系站点管理员开启该模型；"+
			"Model %s has not been priced by the administrator yet. Please contact the site administrator to enable this model.",
		modelName, modelName,
	)
}

// https://docs.claude.com/en/docs/build-with-claude/prompt-caching#1-hour-cache-duration
const claudeCacheCreation1hMultiplier = 6 / 3.75

// defaultTieredPreConsumeMaxTokens is the fallback completion-token estimate
// used for tiered expression pre-consume when the client omits max_tokens, so
// the pre-consumed quota still reflects a plausible output cost in paid groups.
const defaultTieredPreConsumeMaxTokens = 8192

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) hosttypes.GroupRatioInfo {
	groupRatioInfo := hosttypes.GroupRatioInfo{
		GroupRatio:        1.0, // default ratio
		GroupSpecialRatio: -1,
	}

	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, "final group: %s", autoGroup)
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// check user group special ratio
	userGroupRatio, ok := relayInfo.GroupGroupRatio(relayInfo.UserGroup, relayInfo.UsingGroup)
	if ok {
		// user group special ratio
		groupRatioInfo.GroupSpecialRatio = userGroupRatio
		groupRatioInfo.GroupRatio = userGroupRatio
		groupRatioInfo.HasSpecialRatio = true
	} else {
		// normal group ratio
		groupRatioInfo.GroupRatio = relayInfo.GroupRatio(relayInfo.UsingGroup)
	}

	return groupRatioInfo
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	if info != nil {
		if matched := resolveBillingModelName(info.GetOriginModelName(), info); matched != "" && matched != info.OriginModelName {
			info.BillingModelName = matched
		}
	}
	billingModelName := info.GetBillingModelName()
	modelPrice, usePrice := info.ModelPrice(billingModelName)

	groupRatioInfo := HandleGroupRatio(c, info)

	// Check if this model uses tiered_expr billing
	if billingMode(info, billingModelName) == billing_setting.BillingModeTieredExpr {
		return modelPriceHelperTiered(c, info, billingModelName, promptTokens, meta, groupRatioInfo)
	}

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		preConsumedTokens := common.Max(promptTokens, info.PreConsumedQuotaSetting())
		if meta.MaxTokens != 0 {
			preConsumedTokens += meta.MaxTokens
		}
		var success bool
		var matchName string
		modelRatio, success, matchName = info.ModelRatio(billingModelName)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
		completionRatio = info.CompletionRatio(billingModelName)
		cacheRatio, _ = info.CacheRatio(billingModelName)
		cacheCreationRatio, _ = info.CreateCacheRatio(billingModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * claudeCacheCreation1hMultiplier
		imageRatio, _ = info.ImageRatio(billingModelName)
		audioRatio = info.AudioRatio(billingModelName)
		audioCompletionRatio = info.AudioCompletionRatio(billingModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		quota, err := common.QuotaFromFloatStrict(float64(preConsumedTokens) * ratio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedQuota = quota
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
	}

	// check if free model pre-consume is disabled
	if !info.EnableFreeModelPreConsume() {
		// if model price or ratio is 0, do not pre-consume quota
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		} else if usePrice {
			if modelPrice == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		} else {
			if modelRatio == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:            freeModel,
		ModelPrice:           modelPrice,
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		GroupRatioInfo:       groupRatioInfo,
		UsePrice:             usePrice,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheCreationRatio:   cacheCreationRatio,
		CacheCreation5mRatio: cacheCreationRatio5m,
		CacheCreation1hRatio: cacheCreationRatio1h,
		QuotaToPreConsume:    preConsumedQuota,
	}
	if usePrice {
		for name, ratio := range meta.BillingRatios {
			priceData.AddOtherRatio(name, ratio)
		}
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * info.QuotaPerUnit() * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		priceData.QuotaToPreConsume = quota
	}

	if common.DebugEnabled {
		logger.LogDebug(c, "model_price_helper result: %s", priceData.ToSetting())
	}
	info.PriceData = priceData
	return priceData, nil
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (hosttypes.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)

	modelPrice, success := info.ModelPrice(info.OriginModelName)
	usePrice := success
	var modelRatio float64

	if !success {
		defaultPrice, ok := info.DefaultModelPrice(info.OriginModelName)
		if ok {
			modelPrice = defaultPrice
			usePrice = true
		} else {
			var ratioSuccess bool
			var matchName string
			modelRatio, ratioSuccess, matchName = info.ModelRatio(info.OriginModelName)
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !ratioSuccess && !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * info.QuotaPerUnit() * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if !info.EnableFreeModelPreConsume() {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		var err error
		quota, err = common.QuotaFromFloatStrict(modelRatio / 2 * info.QuotaPerUnit() * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		modelPrice = -1
		if !info.EnableFreeModelPreConsume() {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

func HasModelBillingConfig(modelName string) bool {
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	if _, ok, _ := ratio_setting.GetModelRatio(modelName); ok {
		return true
	}
	if billing_setting.GetBillingMode(modelName) != billing_setting.BillingModeTieredExpr {
		return false
	}
	expr, ok := billing_setting.GetBillingExpr(modelName)
	return ok && strings.TrimSpace(expr) != ""
}

func billingMode(info *relaycommon.RelayInfo, modelName string) string {
	if info != nil && info.ConfigSnapshot != nil {
		return info.BillingMode(modelName)
	}
	return billing_setting.GetBillingMode(modelName)
}

// BillingModeForRequest reads the effective billing mode from the request's
// frozen configuration generation.
func BillingModeForRequest(info *relaycommon.RelayInfo, modelName string) string {
	return billingMode(info, modelName)
}

func billingExpr(info *relaycommon.RelayInfo, modelName string) (string, bool) {
	if info != nil && info.ConfigSnapshot != nil {
		return info.BillingExpr(modelName)
	}
	return billing_setting.GetBillingExpr(modelName)
}

// BillingExprForRequest reads the effective billing expression from the
// request's frozen configuration generation.
func BillingExprForRequest(info *relaycommon.RelayInfo, modelName string) (string, bool) {
	return billingExpr(info, modelName)
}

// HasPriceOrRatioEntry reports whether name has a configured price, ratio, or
// tiered billing-mode entry after a single wildcard normalization. Self-use
// fallback does not count as a configured ratio.
func HasPriceOrRatioEntry(name string) bool {
	formatted := ratio_setting.FormatMatchingModelName(name)
	if _, ok := ratio_setting.GetModelPrice(formatted, false); ok {
		return true
	}
	if ratio_setting.HasConfiguredModelRatio(formatted) {
		return true
	}
	return billing_setting.GetBillingMode(formatted) == billing_setting.BillingModeTieredExpr
}

func resolveBillingModelName(origin string, relayInfos ...*relaycommon.RelayInfo) string {
	var snapshotInfo *relaycommon.RelayInfo
	if len(relayInfos) > 0 && relayInfos[0] != nil && relayInfos[0].ConfigSnapshot != nil {
		snapshotInfo = relayInfos[0]
	}
	if snapshotInfo != nil {
		opts := snapshotInfo.ConvOptions()
		var candidates []string
		if !reasoning.ParseModelModifiers(origin).HasModifiers() {
			candidates = append(candidates, origin)
		}
		candidates = append(candidates, hostreasoning.CanonicalBillingModelNamesWithCallbacks(
			origin,
			opts.Claude.ThinkingAdapterEnabled,
			opts.Gemini.ThinkingAdapterEnabled,
			opts.ShouldPreserveThinkingSuffix,
			opts.ShouldPreserveEffortTail,
		)...)
		base := hostreasoning.BaseModelNameWithCallbacks(
			origin,
			opts.Claude.ThinkingAdapterEnabled,
			opts.Gemini.ThinkingAdapterEnabled,
			opts.ShouldPreserveThinkingSuffix,
			opts.ShouldPreserveEffortTail,
		)
		candidates = append(candidates, base)

		seen := make(map[string]struct{}, len(candidates))
		for _, name := range candidates {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			if hasPriceOrRatioEntry(snapshotInfo, name) {
				return name
			}
		}
		return base
	}

	var candidates []string
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	base := hostreasoning.BaseModelName(origin)
	candidates = append(candidates, base)

	seen := make(map[string]struct{}, len(candidates))
	matched := ""
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if HasPriceOrRatioEntry(name) {
			matched = name
			break
		}
	}
	if matched == "" {
		matched = base
	}
	return matched
}

func hasPriceOrRatioEntry(info *relaycommon.RelayInfo, name string) bool {
	if _, ok := info.ModelPrice(name); ok {
		return true
	}
	if info.HasConfiguredModelRatio(name) {
		return true
	}
	return billingMode(info, name) == billing_setting.BillingModeTieredExpr
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string, promptTokens int, meta *types.TokenCountMeta, groupRatioInfo hosttypes.GroupRatioInfo) (hosttypes.PriceData, error) {
	exprStr, ok := billingExpr(info, billingModelName)
	if !ok {
		return hosttypes.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", billingModelName)
	}

	estimatedCompletionTokens := meta.MaxTokens
	if estimatedCompletionTokens == 0 && groupRatioInfo.GroupRatio != 0 {
		estimatedCompletionTokens = defaultTieredPreConsumeMaxTokens
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	rawCost, trace, err := billingexpr.RunExprWithRequest(exprStr, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   float64(estimatedCompletionTokens),
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return hosttypes.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", billingModelName, err)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * info.QuotaPerUnit()
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaBeforeGroup * groupRatioInfo.GroupRatio)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	freeModel := false
	if !info.EnableFreeModelPreConsume() {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	exprHash := billingexpr.ExprHashString(exprStr)
	snapshot := &billingexpr.BillingSnapshot{
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 billingModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: estimatedCompletionTokens,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		QuotaPerUnit:              info.QuotaPerUnit(),
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput

	priceData := hosttypes.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupRatio=%.2f tier=%s", billingModelName, preConsumedQuota, quotaBeforeGroup, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
