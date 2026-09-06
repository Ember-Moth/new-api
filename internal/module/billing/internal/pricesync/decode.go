package pricesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/config/setting/billing_setting"
)

func decodePricing(body []byte, openRouter, modelsDev bool) (map[string]any, error) {
	var data map[string]any
	var err error
	switch {
	case openRouter:
		data, err = convertOpenRouterToRatioData(bytes.NewReader(body))
	case modelsDev:
		data, err = convertModelsDevToRatioData(bytes.NewReader(body))
	default:
		var response struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
			Message string          `json:"message"`
		}
		if err = common.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		if !response.Success {
			if response.Message == "" {
				response.Message = "upstream reported failure"
			}
			return nil, errors.New(response.Message)
		}
		if err := common.Unmarshal(response.Data, &data); err == nil {
			for _, field := range pricingSyncFields {
				if _, ok := data[field]; ok {
					return validatedPricingData(data)
				}
			}
		}
		var items []contract.Pricing
		if err := common.Unmarshal(response.Data, &items); err != nil {
			return nil, errors.New("无法解析上游返回数据")
		}
		data = make(map[string]any)
		for _, field := range pricingSyncFields {
			data[field] = map[string]any{}
		}
		for _, item := range items {
			if item.ModelName == "" {
				continue
			}
			if item.BillingMode == billing_setting.BillingModeTieredExpr && strings.TrimSpace(item.BillingExpr) != "" {
				data[billing_setting.BillingModeField].(map[string]any)[item.ModelName] = item.BillingMode
				data[billing_setting.BillingExprField].(map[string]any)[item.ModelName] = item.BillingExpr
			}
			if item.QuotaType == 1 {
				data["model_price"].(map[string]any)[item.ModelName] = item.ModelPrice
			} else {
				data["model_ratio"].(map[string]any)[item.ModelName] = item.ModelRatio
				data["completion_ratio"].(map[string]any)[item.ModelName] = item.CompletionRatio
			}
			for field, value := range map[string]*float64{"cache_ratio": item.CacheRatio, "create_cache_ratio": item.CreateCacheRatio, "image_ratio": item.ImageRatio, "audio_ratio": item.AudioRatio, "audio_completion_ratio": item.AudioCompletionRatio} {
				if value != nil {
					data[field].(map[string]any)[item.ModelName] = *value
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return validatedPricingData(data)
}

// Only scalar, finite prices and expression strings may reach a preview. This
// also prevents malformed upstream objects from becoming comparison operands.
func validatedPricingData(data map[string]any) (map[string]any, error) {
	result := make(map[string]any)
	for _, field := range pricingSyncFields {
		raw, present := data[field]
		if !present || raw == nil {
			continue
		}
		values := valueMap(raw)
		if values == nil {
			return nil, fmt.Errorf("invalid %s pricing map", field)
		}
		converted := make(map[string]any, len(values))
		for model, value := range values {
			if value == nil {
				continue
			}
			if numericPricingSyncFields[field] {
				number, ok := asFloat64(value)
				if !ok || !isValidNonNegativeCost(number) {
					return nil, fmt.Errorf("invalid %s for model %s", field, model)
				}
				converted[model] = number
			} else {
				text, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("invalid %s for model %s", field, model)
				}
				converted[model] = text
			}
		}
		if len(converted) > 0 {
			result[field] = converted
		}
	}
	return result, nil
}
