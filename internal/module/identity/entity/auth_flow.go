package entity

import (
	"errors"
	"time"
)

const (
	AuthFlowPurposeOAuth             = "oauth"
	AuthFlowPurposeTwoFALogin        = "2fa_login"
	AuthFlowPurposePasskeyLogin      = "passkey_login"
	AuthFlowPurposePasskeyRegister   = "passkey_register"
	AuthFlowPurposePasskeyStepUp     = "passkey_step_up"
	AuthFlowPurposeTelegramBind      = "telegram_bind"
	AuthFlowPurposeTelegramAssertion = "telegram_assertion"
	AuthFlowIntentLogin              = "login"
	AuthFlowIntentBind               = "bind"
	AuthFlowTokenBytes               = 32
	AuthFlowDefaultCleanupRetention  = 24 * time.Hour
)

var (
	ErrAuthFlowInvalid  = errors.New("auth flow is invalid")
	ErrAuthFlowExpired  = errors.New("auth flow has expired")
	ErrAuthFlowConsumed = errors.New("auth flow has already been consumed")
)

// AuthFlow stores one-time, short-lived state for authentication ceremonies.
// TokenHash is an HMAC of the opaque token; the token itself is never persisted.
type AuthFlow struct {
	Id         int64      `json:"id" gorm:"primaryKey"`
	TokenHash  string     `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Purpose    string     `json:"purpose" gorm:"type:varchar(32);not null;index:idx_auth_flow_purpose_expiry"`
	Provider   string     `json:"provider,omitempty" gorm:"type:varchar(64)"`
	Intent     string     `json:"intent,omitempty" gorm:"type:varchar(16)"`
	UserId     int        `json:"user_id,omitempty" gorm:"index"`
	SessionId  string     `json:"session_id,omitempty" gorm:"type:varchar(64);index"`
	Payload    string     `json:"-" gorm:"type:text"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null;index:idx_auth_flow_purpose_expiry"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty" gorm:"index"`
}

func (AuthFlow) TableName() string {
	return "auth_flows"
}

type AuthFlowCreate struct {
	Purpose   string
	Provider  string
	Intent    string
	UserId    int
	SessionId string
	Payload   string
	ExpiresAt time.Time
}

type AuthFlowMatch struct {
	Purpose   string
	Provider  string
	Intent    string
	UserId    int
	SessionId string
}
