package contract

type SelfSubscriptions struct {
	BillingPreference string                `json:"billing_preference"`
	Subscriptions     []SubscriptionSummary `json:"subscriptions"`
	AllSubscriptions  []SubscriptionSummary `json:"all_subscriptions"`
}
