package contract

import "github.com/QuantumNous/new-api/constant"

type ModelPricing struct {
	ModelName              string
	SupportedEndpointTypes []constant.EndpointType
	EnableGroup            []string
	QuotaType              int
}

const (
	NameRuleExact = iota
	NameRulePrefix
	NameRuleContains
	NameRuleSuffix
)

type BoundChannel struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

type Model struct {
	Id           int    `json:"id"`
	ModelName    string `json:"model_name"`
	Description  string `json:"description,omitempty"`
	Icon         string `json:"icon,omitempty"`
	Tags         string `json:"tags,omitempty"`
	VendorID     int    `json:"vendor_id,omitempty"`
	Endpoints    string `json:"endpoints,omitempty"`
	Status       int    `json:"status"`
	SyncOfficial int    `json:"sync_official"`
	CreatedTime  int64  `json:"created_time"`
	UpdatedTime  int64  `json:"updated_time"`

	BoundChannels []BoundChannel `json:"bound_channels,omitempty"`
	EnableGroups  []string       `json:"enable_groups,omitempty"`
	QuotaTypes    []int          `json:"quota_types,omitempty"`
	NameRule      int            `json:"name_rule"`

	MatchedModels []string `json:"matched_models,omitempty"`
	MatchedCount  int      `json:"matched_count,omitempty"`
}
