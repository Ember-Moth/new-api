package contract

import "time"

// Redemption is the existing management response, independent of GORM.
type Redemption struct {
	Id           int    `json:"id"`
	UserId       int    `json:"user_id"`
	Key          string `json:"key"`
	Status       int    `json:"status"`
	Name         string `json:"name"`
	Quota        int    `json:"quota"`
	CreatedTime  int64  `json:"created_time"`
	RedeemedTime int64  `json:"redeemed_time"`
	Count        int    `json:"count"`
	UsedUserId   int    `json:"used_user_id"`
	DeletedAt    *time.Time
	ExpiredTime  int64 `json:"expired_time"`
}

type CreateRedemptionsRequest struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	Quota       int    `json:"quota"`
	ExpiredTime int64  `json:"expired_time"`
}

type UpdateRedemptionRequest struct {
	Id          int    `json:"id"`
	Name        string `json:"name"`
	Quota       int    `json:"quota"`
	ExpiredTime int64  `json:"expired_time"`
	Status      int    `json:"status"`
}
