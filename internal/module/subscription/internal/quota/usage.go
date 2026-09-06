package quota

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm" // Update subscription used amount by delta (positive consume more, negative refund).
	"gorm.io/gorm/clause"
)

func (s *Store) PostConsumeUserSubscriptionDelta(ctx context.Context, userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	_, err := s.PostConsumeUserSubscriptionDeltaTx(ctx, s.db, userSubscriptionId, delta)
	return err
}

// PostConsumeUserSubscriptionDeltaTx applies a subscription usage delta in
// the caller's transaction. It is the only form billing sessions and
// operation receipts should use when token/funding changes must be atomic.
func (s *Store) PostConsumeUserSubscriptionDeltaTx(ctx context.Context, tx *gorm.DB, userSubscriptionId int, delta int64) (*SubscriptionPreConsumeResult, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	if tx == nil {
		return nil, errors.New("subscription transaction is nil")
	}
	if delta == 0 {
		return &SubscriptionPreConsumeResult{UserSubscriptionId: userSubscriptionId}, nil
	}
	return adjustSubscriptionUsage(tx.WithContext(ctx), userSubscriptionId, delta)
}

// PostConsumeUserSubscriptionDeltaForUserTx binds the subscription leg to the
// request owner before applying a delta. Callers handling a raw subscription
// id must use this form so another user's subscription cannot be charged by a
// replayed operation identity.
func (s *Store) PostConsumeUserSubscriptionDeltaForUserTx(ctx context.Context, tx *gorm.DB, userID, userSubscriptionId int, delta int64) (*SubscriptionPreConsumeResult, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	if tx == nil {
		return nil, errors.New("subscription transaction is nil")
	}
	var sub UserSubscription
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", userSubscriptionId, userID).First(&sub).Error; err != nil {
		return nil, err
	}
	if delta == 0 {
		return &SubscriptionPreConsumeResult{UserSubscriptionId: userSubscriptionId, AmountTotal: sub.AmountTotal, AmountUsedBefore: sub.AmountUsed, AmountUsedAfter: sub.AmountUsed}, nil
	}
	return adjustSubscriptionUsage(tx.WithContext(ctx), userSubscriptionId, delta)
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
	return nil, fmt.Errorf("%w: used exceeds total or supported limit, used=%d delta=%d total=%d", contract.ErrSubscriptionQuotaInsufficient, sub.AmountUsed, delta, sub.AmountTotal)
}
