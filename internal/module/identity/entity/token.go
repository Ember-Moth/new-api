package entity

import (
	"strings"

	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"gorm.io/gorm"
)

type Token struct {
	Id                 int              `json:"id"`
	UserId             int              `json:"user_id" gorm:"index"`
	Key                string           `json:"key" gorm:"type:varchar(128);uniqueIndex"`
	Status             int              `json:"status" gorm:"default:1"`
	Name               string           `json:"name" gorm:"index" `
	CreatedTime        int64            `json:"created_time" gorm:"bigint"`
	AccessedTime       int64            `json:"accessed_time" gorm:"bigint"`
	ExpiredTime        int64            `json:"expired_time" gorm:"bigint;default:-1"` // -1 means never expired
	RemainQuota        int              `json:"remain_quota" gorm:"default:0"`
	UnlimitedQuota     bool             `json:"unlimited_quota"`
	ModelLimitsEnabled bool             `json:"model_limits_enabled"`
	ModelLimits        value.StringList `json:"model_limits" gorm:"type:text[];not null;default:'{}'"`
	AllowIps           *string          `json:"allow_ips" gorm:"default:''"`
	UsedQuota          int              `json:"used_quota" gorm:"default:0"` // used quota
	Group              string           `json:"group" gorm:"default:''"`
	CrossGroupRetry    bool             `json:"cross_group_retry"` // 跨分组重试，仅auto分组有效
	AutoGroups         value.StringList `json:"-" gorm:"type:text[]"`
	DeletedAt          gorm.DeletedAt   `gorm:"index"`
}

func (token *Token) GetAutoGroups() ([]string, error) {
	if token.AutoGroups == nil {
		return nil, nil
	}
	return append([]string{}, token.AutoGroups...), nil
}

func (token *Token) SetAutoGroups(groups []string) error {
	if len(groups) == 0 {
		token.AutoGroups = nil
		return nil
	}
	token.AutoGroups = value.StringList(groups).Normalized()
	return nil
}

func (token *Token) BeforeCreate(tx *gorm.DB) error {
	if token.ModelLimits == nil {
		token.ModelLimits = value.StringList{}
	}
	return nil
}

func (token *Token) Clean() {
	token.Key = ""
}

func MaskTokenKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	if len(key) <= 8 {
		return key[:2] + "****" + key[len(key)-2:]
	}
	return key[:4] + "**********" + key[len(key)-4:]
}

func (token *Token) GetFullKey() string {
	return token.Key
}

func (token *Token) GetMaskedKey() string {
	return MaskTokenKey(token.Key)
}

func (token *Token) GetIpLimits() []string {
	// delete empty spaces
	//split with \n
	ipLimits := make([]string, 0)
	if token.AllowIps == nil {
		return ipLimits
	}
	cleanIps := strings.ReplaceAll(*token.AllowIps, " ", "")
	if cleanIps == "" {
		return ipLimits
	}
	ips := strings.Split(cleanIps, "\n")
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		ip = strings.ReplaceAll(ip, ",", "")
		if ip != "" {
			ipLimits = append(ipLimits, ip)
		}
	}
	return ipLimits
}

func (token *Token) IsModelLimitsEnabled() bool {
	return token.ModelLimitsEnabled
}

func (token *Token) GetModelLimits() []string {
	if len(token.ModelLimits) == 0 {
		return []string{}
	}
	return append([]string{}, token.ModelLimits...)
}

func (token *Token) GetModelLimitsMap() map[string]bool {
	limits := token.GetModelLimits()
	limitsMap := make(map[string]bool)
	for _, limit := range limits {
		limitsMap[limit] = true
	}
	return limitsMap
}
