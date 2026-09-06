package contract

import "encoding/json"

type RewardConfig struct {
	CheckinEnabled     bool
	MinQuota, MaxQuota int
	QuotaPerUnit       float64
}
type CheckinRecord struct {
	CheckinDate  string `json:"checkin_date"`
	QuotaAwarded int    `json:"quota_awarded"`
}
type CheckinStats struct {
	TotalQuota     json.Number     `json:"total_quota"`
	TotalCheckins  int64           `json:"total_checkins"`
	CheckinCount   int             `json:"checkin_count"`
	CheckedInToday bool            `json:"checked_in_today"`
	Records        []CheckinRecord `json:"records"`
}
type CheckinStatus struct {
	Enabled  bool         `json:"enabled"`
	MinQuota int          `json:"min_quota"`
	MaxQuota int          `json:"max_quota"`
	Stats    CheckinStats `json:"stats"`
}
type TransferAffQuotaRequest struct {
	Quota int `json:"quota" binding:"required"`
}
