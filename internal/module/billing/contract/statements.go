package contract

import (
	"encoding/json"

	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
)

type StatementConfig struct {
	TokenStats                 bool
	DisplayType                string
	QuotaPerUnit, ExchangeRate float64
}
type TokenUsage struct {
	Object             string          `json:"object"`
	Name               string          `json:"name"`
	TotalGranted       json.Number     `json:"total_granted"`
	TotalUsed          int             `json:"total_used"`
	TotalAvailable     int             `json:"total_available"`
	UnlimitedQuota     bool            `json:"unlimited_quota"`
	ModelLimits        map[string]bool `json:"model_limits"`
	ModelLimitsEnabled bool            `json:"model_limits_enabled"`
	ExpiresAt          int64           `json:"expires_at"`
}

type OpenAISubscriptionResponse = kitdto.OpenAISubscriptionResponse
type OpenAIUsageResponse = kitdto.OpenAIUsageResponse
