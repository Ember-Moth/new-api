package contract

type Plan struct {
	Id int `json:"id"`

	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`

	// Display money amount (follow existing code style: float64 for money)
	PriceAmount float64 `json:"price_amount"`
	Currency    string  `json:"currency"`

	DurationUnit  string `json:"duration_unit"`
	DurationValue int    `json:"duration_value"`
	CustomSeconds int64  `json:"custom_seconds"`

	Enabled   bool `json:"enabled"`
	SortOrder int  `json:"sort_order"`

	AllowBalancePay *bool `json:"allow_balance_pay"`

	// Allow falling back to wallet balance after subscription quota is exhausted (empty = true)
	AllowWalletOverflow *bool `json:"allow_wallet_overflow"`

	StripePriceId         string `json:"stripe_price_id"`
	CreemProductId        string `json:"creem_product_id"`
	WaffoPancakeProductId string `json:"waffo_pancake_product_id"`

	// Max purchases per user (0 = unlimited)
	MaxPurchasePerUser int `json:"max_purchase_per_user"`

	// Upgrade user group after purchase (empty = no change)
	UpgradeGroup string `json:"upgrade_group"`

	// Downgrade user group on expiry (empty = revert to the group held before purchase)
	DowngradeGroup string `json:"downgrade_group"`

	// Total quota (amount in quota units, 0 = unlimited)
	TotalAmount int64 `json:"total_amount"`

	// Quota reset period for plan
	QuotaResetPeriod        string `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// PlanItem preserves the dashboard's plan-list envelope.
type PlanItem struct {
	Plan Plan `json:"plan"`
}

type UpsertPlanRequest struct {
	Plan Plan `json:"plan"`
}

type UpdatePlanStatusRequest struct {
	Enabled *bool `json:"enabled"`
}
