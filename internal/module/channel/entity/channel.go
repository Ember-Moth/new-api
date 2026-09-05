package entity

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

type StringList = value.StringList
type Channel struct {
	Id                 int        `json:"id"`
	Type               int        `json:"type" gorm:"default:0"`
	Key                string     `json:"key" gorm:"not null"`
	OpenAIOrganization *string    `json:"openai_organization"`
	TestModel          *string    `json:"test_model"`
	Status             int        `json:"status" gorm:"default:1"`
	Name               string     `json:"name" gorm:"index"`
	Weight             *uint      `json:"weight" gorm:"default:0"`
	CreatedTime        int64      `json:"created_time" gorm:"bigint"`
	TestTime           int64      `json:"test_time" gorm:"bigint"`
	ResponseTime       int        `json:"response_time"` // in milliseconds
	BaseURL            *string    `json:"base_url" gorm:"column:base_url;default:''"`
	Other              string     `json:"other"`
	Balance            float64    `json:"balance"` // in USD
	BalanceUpdatedTime int64      `json:"balance_updated_time" gorm:"bigint"`
	Models             StringList `json:"models" gorm:"type:text[];not null;default:'{}'"`
	Group              StringList `json:"group" gorm:"type:text[];not null;default:'{default}'"`
	UsedQuota          int64      `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       *string    `json:"model_mapping" gorm:"type:jsonb"`
	//MaxInputTokens     *int    `json:"max_input_tokens" gorm:"default:0"`
	StatusCodeMapping *string `json:"status_code_mapping" gorm:"type:jsonb"`
	Priority          *int64  `json:"priority" gorm:"bigint;default:0"`
	AutoBan           *int    `json:"auto_ban" gorm:"default:1"`
	OtherInfo         string  `json:"other_info" gorm:"type:jsonb"`
	Tag               *string `json:"tag" gorm:"index"`
	Setting           *string `json:"setting" gorm:"type:jsonb"` // 渠道额外设置
	ParamOverride     *string `json:"param_override" gorm:"type:jsonb"`
	HeaderOverride    *string `json:"header_override" gorm:"type:jsonb"`
	Remark            *string `json:"remark" gorm:"type:varchar(255)" validate:"max=255"`
	// add after v0.8.5
	ChannelInfo ChannelInfo `json:"channel_info" gorm:"type:jsonb"`

	OtherSettings string `json:"settings" gorm:"column:settings;type:jsonb"` // 其他设置，存储azure版本等不需要检索的信息，详见dto.ChannelOtherSettings

	// cache info
	Keys []string `json:"-" gorm:"-"`
}

type ChannelInfo struct {
	IsMultiKey             bool                  `json:"is_multi_key"`                        // 是否多Key模式
	MultiKeySize           int                   `json:"multi_key_size"`                      // 多Key模式下的Key数量
	MultiKeyStatusList     map[int]int           `json:"multi_key_status_list"`               // key状态列表，key index -> status
	MultiKeyDisabledReason map[int]string        `json:"multi_key_disabled_reason,omitempty"` // key禁用原因列表，key index -> reason
	MultiKeyDisabledTime   map[int]int64         `json:"multi_key_disabled_time,omitempty"`   // key禁用时间列表，key index -> time
	MultiKeyPollingIndex   int                   `json:"multi_key_polling_index"`             // 多Key模式下轮询的key索引
	MultiKeyMode           constant.MultiKeyMode `json:"multi_key_mode"`
}

// Value implements driver.Valuer interface
// 必须返回 string 而非 []byte:PG simple protocol 下 []byte 参数按 bytea
// 编码,写 json 列会触发 SQLSTATE 22P02。
func (c ChannelInfo) Value() (driver.Value, error) {
	b, err := common.Marshal(&c)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner interface
func (c *ChannelInfo) Scan(value interface{}) error {
	return common.Unmarshal(jsonScanBytes(value), c)
}

func (channel *Channel) GetKeys() []string {
	if channel.Key == "" {
		return []string{}
	}
	if len(channel.Keys) > 0 {
		return channel.Keys
	}
	trimmed := strings.TrimSpace(channel.Key)
	// If the key starts with '[', try to parse it as a JSON array (e.g., for Vertex AI scenarios)
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := common.Unmarshal([]byte(trimmed), &arr); err == nil {
			res := make([]string, len(arr))
			for i, v := range arr {
				res[i] = string(v)
			}
			return res
		}
	}
	// Otherwise, fall back to splitting by newline
	keys := strings.Split(strings.Trim(channel.Key, "\n"), "\n")
	return keys
}

func (channel *Channel) GetModels() []string {
	return append([]string{}, channel.Models...)
}

func (channel *Channel) GetGroups() []string {
	return append([]string{}, channel.Group...)
}

func (channel *Channel) BeforeCreate(tx *gorm.DB) error {
	if channel.OtherInfo == "" {
		channel.OtherInfo = "{}"
	}
	if channel.OtherSettings == "" {
		channel.OtherSettings = "{}"
	}
	if channel.Models == nil {
		channel.Models = StringList{}
	}
	if channel.Group == nil {
		channel.Group = StringList{"default"}
	}
	return nil
}

func (channel *Channel) BeforeSave(tx *gorm.DB) error {
	if target, ok := tx.Statement.Dest.(*Channel); ok {
		channel = target
	}
	if updates, ok := tx.Statement.Dest.(map[string]any); ok {
		for _, name := range []string{"model_mapping", "status_code_mapping", "setting", "param_override", "header_override", "settings", "other_info"} {
			if value, ok := updates[name].(string); ok && strings.TrimSpace(value) == "" {
				updates[name] = "{}"
			}
		}
	}
	if channel.Models != nil {
		channel.Models = channel.Models.Normalized()
	}
	if channel.Group != nil {
		channel.Group = channel.Group.Normalized()
	}
	for _, field := range []**string{&channel.ModelMapping, &channel.StatusCodeMapping, &channel.Setting, &channel.ParamOverride, &channel.HeaderOverride} {
		if *field == nil {
			continue
		}
		value := strings.TrimSpace(**field)
		if value == "" {
			value = "{}"
		}
		var document json.RawMessage
		if err := common.UnmarshalJsonStr(value, &document); err != nil {
			return err
		}
		*field = &value
	}

	return nil
}

func (channel *Channel) GetOtherInfo() map[string]interface{} {
	otherInfo := make(map[string]interface{})
	if channel.OtherInfo != "" {
		err := common.Unmarshal([]byte(channel.OtherInfo), &otherInfo)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		}
	}
	return otherInfo
}

func (channel *Channel) SetOtherInfo(otherInfo map[string]interface{}) {
	otherInfoBytes, err := common.Marshal(otherInfo)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		return
	}
	channel.OtherInfo = string(otherInfoBytes)
}

func (channel *Channel) GetTag() string {
	if channel.Tag == nil {
		return ""
	}
	return *channel.Tag
}

func (channel *Channel) SetTag(tag string) {
	channel.Tag = &tag
}

func (channel *Channel) GetAutoBan() bool {
	if channel.AutoBan == nil {
		return false
	}
	return *channel.AutoBan == 1
}

func (channel *Channel) GetPriority() int64 {
	if channel.Priority == nil {
		return 0
	}
	return *channel.Priority
}

func (channel *Channel) GetWeight() int {
	if channel.Weight == nil {
		return 0
	}
	return int(*channel.Weight)
}

func (channel *Channel) GetBaseURL() string {
	if channel.BaseURL == nil {
		return ""
	}
	url := *channel.BaseURL
	if url == "" {
		url = constant.GetChannelBaseURL(channel.Type)
	}
	return url
}

func (channel *Channel) GetModelMapping() string {
	if channel.ModelMapping == nil {
		return ""
	}
	return *channel.ModelMapping
}

func (channel *Channel) GetStatusCodeMapping() string {
	if channel.StatusCodeMapping == nil {
		return ""
	}
	return *channel.StatusCodeMapping
}

func (channel *Channel) ValidateSettings() error {
	channelParams := &dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), channelParams)
		if err != nil {
			return err
		}
	}
	if _, err := common.ParseProxyURLStrict(channelParams.Proxy); err != nil {
		return fmt.Errorf("invalid channel proxy: %w", err)
	}
	if err := channelParams.ValidateHTTPTransport(); err != nil {
		return err
	}
	channelOtherSettings := &dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, channelOtherSettings)
		if err != nil {
			return err
		}
	}
	if err := channelOtherSettings.ValidateToolLossPolicy(); err != nil {
		return err
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if channelOtherSettings.AdvancedCustom == nil {
			return fmt.Errorf("advanced_custom is required")
		}
	}
	if channelOtherSettings.AdvancedCustom != nil {
		if err := channelOtherSettings.AdvancedCustom.Validate(); err != nil {
			return err
		}
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom && channelOtherSettings.UpstreamModelUpdateCheckEnabled {
		if _, ok := channelOtherSettings.AdvancedCustom.ModelListRoute(); !ok {
			return fmt.Errorf("advanced custom channels require a %s route when upstream model update checks are enabled", dto.AdvancedCustomModelListPath)
		}
	}
	return nil
}

func (channel *Channel) GetSetting() dto.ChannelSettings {
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.Setting = nil // 清空设置以避免后续错误
		}
	}
	return setting
}

func (channel *Channel) SetSetting(setting dto.ChannelSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.Setting = common.GetPointer[string](string(settingBytes))
}

func (channel *Channel) GetOtherSettings() dto.ChannelOtherSettings {
	setting := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.OtherSettings = "{}" // 清空设置以避免后续错误
		}
	}
	return setting
}

func (channel *Channel) SetOtherSettings(setting dto.ChannelOtherSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.OtherSettings = string(settingBytes)
}

func (channel *Channel) GetParamOverride() map[string]interface{} {
	paramOverride := make(map[string]interface{})
	if channel.ParamOverride != nil && *channel.ParamOverride != "" {
		err := common.Unmarshal([]byte(*channel.ParamOverride), &paramOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal param override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return paramOverride
}

func (channel *Channel) GetHeaderOverride() map[string]interface{} {
	headerOverride := make(map[string]interface{})
	if channel.HeaderOverride != nil && *channel.HeaderOverride != "" {
		err := common.Unmarshal([]byte(*channel.HeaderOverride), &headerOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal header override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return headerOverride
}

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

// jsonScanBytes 归一化 json 列的驱动返回值:不同驱动/协议模式下同一列可能
// 以 []byte 或 string 返回,静默丢弃 string 会导致字段被清零而不报错。
func jsonScanBytes(value interface{}) []byte {
	switch v := value.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		return nil
	}
}
