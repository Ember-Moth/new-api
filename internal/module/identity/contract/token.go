package contract

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
)

// TokenActor carries authenticated ownership and the group resolved by inbound auth.
type TokenActor struct {
	ID    int
	Group string
}

// TokenAutoGroupsInput distinguishes omission (preserve on update) from null or
// an empty list (inherit global routing groups).
type TokenAutoGroupsInput struct {
	Set    bool
	Groups []string
}

func (input *TokenAutoGroupsInput) UnmarshalJSON(data []byte) error {
	input.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		input.Groups = nil
		return nil
	}
	return common.Unmarshal(data, &input.Groups)
}

type TokenSettings struct {
	Id                 int      `json:"id"`
	Status             int      `json:"status"`
	Name               string   `json:"name"`
	ExpiredTime        int64    `json:"expired_time"`
	RemainQuota        int      `json:"remain_quota"`
	UnlimitedQuota     bool     `json:"unlimited_quota"`
	ModelLimitsEnabled bool     `json:"model_limits_enabled"`
	ModelLimits        []string `json:"model_limits"`
	AllowIps           *string  `json:"allow_ips"`
	Group              string   `json:"group"`
	CrossGroupRetry    bool     `json:"cross_group_retry"`
}

type TokenRequest struct {
	TokenSettings
	AutoGroups TokenAutoGroupsInput `json:"auto_groups"`
}

type TokenResponse struct {
	Id                 int      `json:"id"`
	UserId             int      `json:"user_id"`
	Key                string   `json:"key"`
	Status             int      `json:"status"`
	Name               string   `json:"name"`
	CreatedTime        int64    `json:"created_time"`
	AccessedTime       int64    `json:"accessed_time"`
	ExpiredTime        int64    `json:"expired_time"`
	RemainQuota        int      `json:"remain_quota"`
	UnlimitedQuota     bool     `json:"unlimited_quota"`
	ModelLimitsEnabled bool     `json:"model_limits_enabled"`
	ModelLimits        []string `json:"model_limits"`
	AllowIps           *string  `json:"allow_ips"`
	UsedQuota          int      `json:"used_quota"`
	Group              string   `json:"group"`
	CrossGroupRetry    bool     `json:"cross_group_retry"`
	AutoGroups         []string `json:"auto_groups"`
	DeletedAt          *time.Time
}

type TokenBatch struct {
	Ids []int `json:"ids"`
}

type TokenAutoGroupOptions struct {
	Groups   []string `json:"groups"`
	MaxCount int      `json:"max_count"`
}
