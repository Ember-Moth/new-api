package quota

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/internal/dbtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	if entity.NormalizeResetPeriod(plan.QuotaResetPeriod) == entity.SubscriptionResetNever {
		if sub.NextResetTime == 0 {
			return nil
		}
		sub.NextResetTime, sub.LastResetTime = 0, 0
		return tx.Save(sub).Error
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	base := time.Unix(baseUnix, 0)
	next := plan.NextResetTime(base, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		if next <= base.Unix() {
			return errors.New("subscription reset schedule must advance")
		}
		advanced = true
		base = time.Unix(next, 0)
		next = plan.NextResetTime(base, sub.EndTime)
	}
	if !advanced {
		if sub.NextResetTime != next {
			sub.NextResetTime = next
			sub.LastResetTime = 0
			if next > 0 {
				sub.LastResetTime = base.Unix()
			}
			return tx.Save(sub).Error
		}
		return nil
	}
	sub.AmountUsed = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	return tx.Save(sub).Error
}

// ResetDueSubscriptions resets subscriptions whose next_reset_time has passed.
func (s *Store) ResetDueSubscriptions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := dbtime.Timestamp(s.db.WithContext(ctx))
	var subs []UserSubscription
	if err := s.db.WithContext(ctx).Where("next_reset_time > 0 AND next_reset_time <= ? AND status = ? AND end_time > ?", now, "active", now).Order("next_reset_time asc, id asc").Limit(limit).Find(&subs).Error; err != nil {
		return 0, err
	}
	resetCount := 0
	for _, sub := range subs {
		processed := false
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var locked UserSubscription
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND next_reset_time > 0 AND next_reset_time <= ? AND status = ? AND end_time > ?", sub.Id, now, "active", now).First(&locked).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			plan, err := s.catalog.Plan(ctx, tx, locked.PlanId)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &locked, plan, now); err != nil {
				return err
			}
			processed = true
			return nil
		})
		if err != nil {
			return resetCount, err
		}
		if processed {
			resetCount++
		}
	}
	return resetCount, nil
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency records to keep table small.
func (s *Store) CleanupSubscriptionPreConsumeRecords(ctx context.Context, olderThanSeconds int64) (int64, error) {
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := dbtime.Timestamp(s.db.WithContext(ctx)) - olderThanSeconds
	res := s.db.WithContext(ctx).Where("updated_at < ?", cutoff).Delete(&SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}
