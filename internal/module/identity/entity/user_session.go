package entity

// UserSession is authoritative DragonflyDB state. Only HMAC digests of refresh secrets are stored.
type UserSession struct {
	SID                 string `json:"sid" redis:"SID"`
	UserID              int    `json:"user_id" redis:"UserID"`
	Version             int64  `json:"version" redis:"Version"`
	UserAuthVersion     int64  `json:"user_auth_version" redis:"UserAuthVersion"`
	Status              string `json:"status" redis:"Status"`
	RefreshHash         string `json:"-" redis:"RefreshHash"`
	PreviousRefreshHash string `json:"-" redis:"PreviousRefreshHash"`
	PreviousValidUntil  int64  `json:"-" redis:"PreviousValidUntil"`
	LoginMethod         string `json:"login_method" redis:"LoginMethod"`
	IP                  string `json:"ip" redis:"IP"`
	UserAgent           string `json:"user_agent" redis:"UserAgent"`
	CreatedAt           int64  `json:"created_at" redis:"CreatedAt"`
	LastActiveAt        int64  `json:"last_active_at" redis:"LastActiveAt"`
	ExpiresAt           int64  `json:"expires_at" redis:"ExpiresAt"`
	RevokedAt           int64  `json:"revoked_at,omitempty" redis:"RevokedAt"`
	RevokedReason       string `json:"revoked_reason,omitempty" redis:"RevokedReason"`
}
