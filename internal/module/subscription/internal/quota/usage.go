package quota

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm" // Update subscription used amount by delta (positive consume more, negative refund).
)

func (s *Store) PostConsumeUserSubscriptionDelta(ctx context.Context, userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	_, err := adjustSubscriptionUsage(s.db.WithContext(ctx), userSubscriptionId, delta)
	return err
}

// adjustSubscriptionUsage keeps arithmetic in PostgreSQL numeric until the
// result passes bigint and subscription bounds. OLD/NEW supplies receipt data.
func adjustSubscriptionUsage(tx *gorm.DB, id int, delta int64) (*SubscriptionPreConsumeResult, error) {
	result := &SubscriptionPreConsumeResult{UserSubscriptionId: id}
	update := tx.Raw(`
UPDATE user_subscriptions
SET amount_used = GREATEST(0, amount_used::numeric + ?)::bigint, updated_at = ?
WHERE id = ?
  AND GREATEST(0, amount_used::numeric + ?) <= ?
  AND (amount_total <= 0 OR GREATEST(0, amount_used::numeric + ?) <= amount_total)
RETURNING OLD.amount_used AS amount_used_before, NEW.amount_used AS amount_used_after,
          NEW.amount_total AS amount_total, NEW.id AS user_subscription_id`, delta, common.GetTimestamp(), id, delta, int64(math.MaxInt64), delta).Scan(result)
	if update.Error != nil {
		return nil, update.Error
	}
	if update.RowsAffected == 1 {
		return result, nil
	}
	var sub UserSubscription
	if err := tx.Select("id", "amount_total", "amount_used").First(&sub, id).Error; err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("subscription used exceeds total or supported limit, used=%d delta=%d total=%d", sub.AmountUsed, delta, sub.AmountTotal)
}
