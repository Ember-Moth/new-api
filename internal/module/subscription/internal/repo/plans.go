package repo

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"gorm.io/gorm"
)

type Plans struct{ db *gorm.DB }

func NewPlans(db *gorm.DB) *Plans { return &Plans{db: db} }

func (r *Plans) List(ctx context.Context, enabledOnly bool) ([]entity.SubscriptionPlan, error) {
	query := r.db.WithContext(ctx).Order("sort_order desc, id desc")
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var plans []entity.SubscriptionPlan
	err := query.Find(&plans).Error
	return plans, err
}

func (r *Plans) Create(ctx context.Context, plan *entity.SubscriptionPlan) error {
	return r.db.WithContext(ctx).Create(plan).Error
}

func (r *Plans) Update(ctx context.Context, plan *entity.SubscriptionPlan) error {
	updateMap := map[string]interface{}{
		"title":                      plan.Title,
		"subtitle":                   plan.Subtitle,
		"price_amount":               plan.PriceAmount,
		"currency":                   plan.Currency,
		"duration_unit":              plan.DurationUnit,
		"duration_value":             plan.DurationValue,
		"custom_seconds":             plan.CustomSeconds,
		"enabled":                    plan.Enabled,
		"sort_order":                 plan.SortOrder,
		"stripe_price_id":            plan.StripePriceId,
		"creem_product_id":           plan.CreemProductId,
		"waffo_pancake_product_id":   plan.WaffoPancakeProductId,
		"max_purchase_per_user":      plan.MaxPurchasePerUser,
		"total_amount":               plan.TotalAmount,
		"upgrade_group":              plan.UpgradeGroup,
		"downgrade_group":            plan.DowngradeGroup,
		"quota_reset_period":         plan.QuotaResetPeriod,
		"quota_reset_custom_seconds": plan.QuotaResetCustomSeconds,
		"updated_at":                 common.GetTimestamp(),
	}
	if plan.AllowBalancePay != nil {
		updateMap["allow_balance_pay"] = *plan.AllowBalancePay
	}
	if plan.AllowWalletOverflow != nil {
		updateMap["allow_wallet_overflow"] = *plan.AllowWalletOverflow
	}
	return r.db.WithContext(ctx).Model(&entity.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(updateMap).Error
}

func (r *Plans) UpdateStatus(ctx context.Context, id int, enabled bool) error {
	return r.db.WithContext(ctx).Model(&entity.SubscriptionPlan{}).Where("id = ?", id).Update("enabled", enabled).Error
}

func (r *Plans) Get(ctx context.Context, id int) (*entity.SubscriptionPlan, error) {
	var plan entity.SubscriptionPlan
	if err := r.db.WithContext(ctx).First(&plan, id).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	return &plan, nil
}
