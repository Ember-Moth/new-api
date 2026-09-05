package contract

import "time"

type UserActor struct {
	ID   int
	Role int
}

type UserFilter struct {
	Keyword   string
	Group     string
	Role      *int
	Status    *int
	Search    bool
	SortBy    string
	SortOrder string
	Offset    int
	Limit     int
}

type UserRequest struct {
	Id               int                        `json:"id"`
	Username         string                     `json:"username" validate:"max=20"`
	Password         string                     `json:"password" validate:"omitempty,min=8,max=20"`
	DisplayName      string                     `json:"display_name" validate:"max=20"`
	Email            string                     `json:"email" validate:"max=50"`
	Role             int                        `json:"role"`
	Group            string                     `json:"group"`
	Remark           string                     `json:"remark,omitempty" validate:"max=255"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty"`
}

type ManageUserRequest struct {
	Id     int    `json:"id"`
	Action string `json:"action"`
	Value  int    `json:"value"`
	Mode   string `json:"mode"`
}

type UserAudit struct {
	TargetID   int
	Action     string
	Parameters map[string]any
}

type UserMutation struct {
	Data  *UserResponse
	Audit UserAudit
}

type UserResponse struct {
	Id               int                        `json:"id"`
	Username         string                     `json:"username"`
	Password         string                     `json:"password"`
	OriginalPassword string                     `json:"original_password"` // this field is only for Password change verification, don't save it to database!
	DisplayName      string                     `json:"display_name"`
	Role             int                        `json:"role"`   // admin, common
	Status           int                        `json:"status"` // enabled, disabled
	Email            string                     `json:"email"`
	GitHubId         string                     `json:"github_id"`
	DiscordId        string                     `json:"discord_id"`
	OidcId           string                     `json:"oidc_id"`
	WeChatId         string                     `json:"wechat_id"`
	TelegramId       string                     `json:"telegram_id"`
	VerificationCode string                     `json:"verification_code"` // this field is only for Email verification, don't save it to database!
	AccessToken      *string                    `json:"-"`                 // this token is for system management
	Quota            int                        `json:"quota"`
	UsedQuota        int                        `json:"used_quota"`    // used quota
	RequestCount     int                        `json:"request_count"` // request number
	Group            string                     `json:"group"`
	AffCode          string                     `json:"aff_code"`
	AffCount         int                        `json:"aff_count"`
	AffQuota         int                        `json:"aff_quota"`         // 邀请剩余额度
	AffHistoryQuota  int                        `json:"aff_history_quota"` // 邀请历史额度
	InviterId        int                        `json:"inviter_id"`
	DeletedAt        *time.Time                 `gorm:"index"`
	LinuxDOId        string                     `json:"linux_do_id"`
	Setting          string                     `json:"setting"`
	Remark           string                     `json:"remark,omitempty"`
	StripeCustomer   string                     `json:"stripe_customer"`
	CreatedAt        int64                      `json:"created_at"`
	LastLoginAt      int64                      `json:"last_login_at"`
	AuthVersion      int64                      `json:"-"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty"`
}
