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
)

var (
	ErrAuthFlowInvalid  = errors.New("auth flow is invalid")
	ErrAuthFlowExpired  = errors.New("auth flow has expired")
	ErrAuthFlowConsumed = errors.New("auth flow has already been consumed")
)

// AuthFlow stores one-time, short-lived state for authentication ceremonies.
// TokenHash is an HMAC of the opaque token; the token itself is never persisted.
type AuthFlow struct {
	TokenHash  string     `json:"-"`
	Purpose    string     `json:"purpose"`
	Provider   string     `json:"provider,omitempty"`
	Intent     string     `json:"intent,omitempty"`
	UserId     int        `json:"user_id,omitempty"`
	SessionId  string     `json:"session_id,omitempty"`
	Payload    string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
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
