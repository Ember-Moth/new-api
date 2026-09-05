package contract

// RefreshedCredential is the public result of a provider credential refresh.
// Access and refresh tokens are deliberately not part of this projection.
type RefreshedCredential struct {
	ExpiresAt   string `json:"expires_at"`
	LastRefresh string `json:"last_refresh"`
	AccountID   string `json:"account_id"`
	Email       string `json:"email"`
	ChannelID   int    `json:"channel_id"`
	ChannelType int    `json:"channel_type"`
	ChannelName string `json:"channel_name"`
}
