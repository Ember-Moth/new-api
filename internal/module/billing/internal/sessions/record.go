package sessions

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

const (
	sessionStatusActive   = "active"
	sessionStatusSettled  = "settled"
	sessionStatusRefunded = "refunded"
)

// billingSessionRecord is the durable state machine for one gateway billing
// request. It intentionally stores token/user identities and authorization
// attributes, but never the token secret. A first Begin validates the current
// key; terminal retries use the persisted token id so rotation or soft delete
// cannot strand an already-authorized charge.
type billingSessionRecord struct {
	RequestID string `gorm:"column:request_id;primaryKey;type:varchar(64)"`
	UserID    int    `gorm:"column:user_id;not null"`
	TokenID   int    `gorm:"column:token_id;not null"`

	Source     string `gorm:"column:source;not null;type:varchar(32)"`
	ModelName  string `gorm:"column:model_name;not null;type:varchar(255)"`
	Preference string `gorm:"column:preference;not null;type:varchar(32)"`

	RequestedQuota int  `gorm:"column:requested_quota;not null"`
	ReservedQuota  int  `gorm:"column:reserved_quota;not null"`
	ActualQuota    *int `gorm:"column:actual_quota"`

	SubscriptionID        int    `gorm:"column:subscription_id;not null"`
	PlanID                int    `gorm:"column:plan_id;not null"`
	PlanTitle             string `gorm:"column:plan_title;not null;type:varchar(255)"`
	UserQuota             int    `gorm:"column:user_quota;not null"`
	SubscriptionTotal     int64  `gorm:"column:subscription_total;not null"`
	SubscriptionUsed      int64  `gorm:"column:subscription_used;not null"`
	SubscriptionPostDelta int64  `gorm:"column:subscription_post_delta;not null"`

	TokenUnlimited  bool `gorm:"column:token_unlimited;not null"`
	Playground      bool `gorm:"column:playground;not null"`
	ForcePreConsume bool `gorm:"column:force_pre_consume;not null"`
	Trusted         bool `gorm:"column:trusted;not null"`

	Status               string `gorm:"column:status;not null;type:varchar(32)"`
	ChannelID            int    `gorm:"column:channel_id;not null"`
	UsageRecorded        bool   `gorm:"column:usage_recorded;not null"`
	PendingAction        string `gorm:"column:pending_action;not null;type:varchar(32)"`
	IntentActual         *int   `gorm:"column:intent_actual"`
	IntentChannel        int    `gorm:"column:intent_channel;not null"`
	IntentRequiresCommit bool   `gorm:"column:intent_requires_commit;not null"`
	IntentUsage          bool   `gorm:"column:intent_usage;not null"`
	CreatedAt            int64  `gorm:"column:created_at;not null"`
	UpdatedAt            int64  `gorm:"column:updated_at;not null"`
}

func (billingSessionRecord) TableName() string { return "billing_sessions" }

func (r *billingSessionRecord) BeforeCreate(tx *gorm.DB) error {
	if r.CreatedAt == 0 {
		r.CreatedAt = common.GetTimestamp()
	}
	if r.UpdatedAt == 0 {
		r.UpdatedAt = r.CreatedAt
	}
	return nil
}

func (r *billingSessionRecord) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

func (r *billingSessionRecord) validate() error {
	if r.RequestID == "" || len(r.RequestID) > 64 || r.UserID <= 0 || r.TokenID < 0 || (r.TokenID == 0 && !r.Playground) {
		return errors.New("invalid billing session record")
	}
	if r.Source != contractSourceWallet && r.Source != contractSourceSubscription {
		return errors.New("invalid billing session source")
	}
	if r.Status != sessionStatusActive && r.Status != sessionStatusSettled && r.Status != sessionStatusRefunded {
		return errors.New("invalid billing session status")
	}
	if r.RequestedQuota < 0 || r.RequestedQuota > common.MaxQuota || r.ReservedQuota < 0 || r.ReservedQuota > common.MaxQuota {
		return errors.New("invalid billing session quota")
	}
	if r.ActualQuota != nil && (*r.ActualQuota < 0 || *r.ActualQuota > common.MaxQuota) {
		return errors.New("invalid billing session actual quota")
	}
	if r.PendingAction != "" && r.PendingAction != "settle" && r.PendingAction != "refund" && r.PendingAction != "reconcile" {
		return errors.New("invalid billing session pending action")
	}
	if r.IntentActual != nil && (*r.IntentActual < 0 || *r.IntentActual > common.MaxQuota) {
		return errors.New("invalid billing session settlement intent")
	}
	if r.Status == sessionStatusSettled && r.ActualQuota == nil {
		return errors.New("settled billing session has no actual quota")
	}
	if r.Status == sessionStatusActive && r.PendingAction == "settle" && r.IntentActual == nil {
		return errors.New("pending settlement has no actual quota")
	}
	return nil
}

// Keep source literals local to the persistence package so record validation
// cannot accidentally accept an unrelated operation kind.
const (
	contractSourceWallet       = "wallet"
	contractSourceSubscription = "subscription"
)
