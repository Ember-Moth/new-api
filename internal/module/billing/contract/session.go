package contract

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
}

type BillingFailureKind string

const (
	BillingInvalidQuota      BillingFailureKind = "invalid_quota"
	BillingQueryFailure      BillingFailureKind = "query_failure"
	BillingStorageFailure    BillingFailureKind = "storage_failure"
	BillingInsufficientFunds BillingFailureKind = "insufficient_funds"
	BillingInsufficientToken BillingFailureKind = "insufficient_token"
	BillingSessionClosed     BillingFailureKind = "session_closed"
)

type BillingFailure struct {
	Kind  BillingFailureKind
	Cause error
}

func (e *BillingFailure) Error() string { return e.Cause.Error() }
func (e *BillingFailure) Unwrap() error { return e.Cause }

type QuotaAdjustment struct {
	FundingApplied, TokenApplied bool
	SubscriptionPostDelta        int64
}
