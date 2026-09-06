package contract

import "errors"

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

type BillingRequest struct {
	RequestID, ModelName, Preference            string
	UserID, TokenID, TokenQuota                 int
	TokenKey                                    string
	TokenUnlimited, Playground, ForcePreConsume bool
}

type BillingState struct {
	Source                                                                              string
	UserQuota, PreConsumedQuota                                                         int
	SubscriptionID, PlanID                                                              int
	PlanTitle                                                                           string
	SubscriptionPreConsumed, SubscriptionPostDelta, SubscriptionTotal, SubscriptionUsed int64
	ActualQuota                                                                         int
	Status                                                                              string
	Trusted                                                                             bool
	PendingAction                                                                       string
	PendingActualQuota, PendingChannelID                                                int
	PendingUsage, UsageRecorded                                                         bool
	ChannelID                                                                           int
}

type BillingFailureKind string

const (
	BillingInvalidQuota      BillingFailureKind = "invalid_quota"
	BillingQueryFailure      BillingFailureKind = "query_failure"
	BillingStorageFailure    BillingFailureKind = "storage_failure"
	BillingInsufficientFunds BillingFailureKind = "insufficient_funds"
	BillingInsufficientToken BillingFailureKind = "insufficient_token"
	BillingSessionClosed     BillingFailureKind = "session_closed"
	BillingSessionConflict   BillingFailureKind = "session_conflict"
	BillingOperationConflict BillingFailureKind = "operation_conflict"
	BillingInvalidRequest    BillingFailureKind = "invalid_request"
)

var (
	ErrBillingSessionConflict   = errors.New("billing session identity or state conflict")
	ErrBillingOperationConflict = errors.New("billing operation identity or state conflict")
)

type BillingFailure struct {
	Kind  BillingFailureKind
	Cause error
}

func (e *BillingFailure) Error() string { return e.Cause.Error() }
func (e *BillingFailure) Unwrap() error { return e.Cause }

type QuotaAdjustment struct {
	FundingApplied, TokenApplied bool
	Replayed                     bool
	SubscriptionPostDelta        int64
}

// BillingAdjustment identifies one durable non-session accounting operation.
// OperationID must be supplied by the caller from a stable business event;
// it must not be generated from the delta or regenerated for each retry.
type BillingAdjustment struct {
	OperationID    string
	Source         string
	SubscriptionID int
	Delta          int
	UsageDelta     int
	RequestDelta   int
	ChannelID      int
	// UseHistoricalToken permits an already-authorized task/event to settle
	// against the persisted token owner after token metadata changes. The
	// operation receipt binds this bit, so retries must use the same mode.
	UseHistoricalToken bool
}
