package contract

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
)

type ProfileInput struct {
	Username         string `json:"username" validate:"max=20"`
	DisplayName      string `json:"display_name" validate:"max=20"`
	Password         string `json:"password" validate:"omitempty,min=8,max=20"`
	OriginalPassword string `json:"original_password"`
	Email            string `json:"email" validate:"max=50"`
	Remark           string `json:"remark" validate:"max=255"`
}

// SelfUpdateRequest retains the established preference-first wire format.
// A non-string preference is ignored; it never falls through to profile edits.
type SelfUpdateRequest struct {
	ProfileInput
	Preference      string
	PreferenceValue *string
}

func (r *SelfUpdateRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"sidebar_modules", "language"} {
		if raw, ok := fields[key]; ok {
			r.Preference = key
			var value string
			if string(raw) != "null" && common.Unmarshal(raw, &value) == nil {
				r.PreferenceValue = &value
			}
			return nil
		}
	}
	return common.Unmarshal(data, &r.ProfileInput)
}

type NotificationSettingsRequest struct {
	QuotaWarningType                 string  `json:"notify_type"`
	QuotaWarningThreshold            float64 `json:"quota_warning_threshold"`
	WebhookUrl                       string  `json:"webhook_url,omitempty"`
	WebhookSecret                    string  `json:"webhook_secret,omitempty"`
	NotificationEmail                string  `json:"notification_email,omitempty"`
	BarkUrl                          string  `json:"bark_url,omitempty"`
	GotifyUrl                        string  `json:"gotify_url,omitempty"`
	GotifyToken                      string  `json:"gotify_token,omitempty"`
	GotifyPriority                   int     `json:"gotify_priority,omitempty"`
	UpstreamModelUpdateNotifyEnabled *bool   `json:"upstream_model_update_notify_enabled,omitempty"`
	AcceptUnsetModelRatioModel       bool    `json:"accept_unset_model_ratio_model"`
	RecordIpLog                      bool    `json:"record_ip_log"`
}

type BindEmailRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type DashboardPermissions struct {
	SidebarSettings  bool                       `json:"sidebar_settings"`
	SidebarModules   map[string]any             `json:"sidebar_modules"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions"`
}

type SelfUserResponse struct {
	Id              int                  `json:"id"`
	Username        string               `json:"username"`
	DisplayName     string               `json:"display_name"`
	Role            int                  `json:"role"`
	Status          int                  `json:"status"`
	Email           string               `json:"email"`
	GitHubId        string               `json:"github_id"`
	DiscordId       string               `json:"discord_id"`
	OidcId          string               `json:"oidc_id"`
	WeChatId        string               `json:"wechat_id"`
	TelegramId      string               `json:"telegram_id"`
	Group           string               `json:"group"`
	Quota           int                  `json:"quota"`
	UsedQuota       int                  `json:"used_quota"`
	RequestCount    int                  `json:"request_count"`
	AffCode         string               `json:"aff_code"`
	AffCount        int                  `json:"aff_count"`
	AffQuota        int                  `json:"aff_quota"`
	AffHistoryQuota int                  `json:"aff_history_quota"`
	InviterId       int                  `json:"inviter_id"`
	LinuxDOId       string               `json:"linux_do_id"`
	Setting         string               `json:"setting"`
	StripeCustomer  string               `json:"stripe_customer"`
	SidebarModules  string               `json:"sidebar_modules"`
	Permissions     DashboardPermissions `json:"permissions"`
}
