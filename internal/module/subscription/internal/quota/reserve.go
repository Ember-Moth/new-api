package quota

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/internal/dbtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause" // PreConsumeUserSubscription pre-consumes from any active subscription total quota.
)

func (s *Store) PreConsumeUserSubscription(ctx context.Context, requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 || strings.TrimSpace(requestId) == "" || len(requestId) > 64 {
		return nil, errors.New("invalid subscription reservation identity")
	}
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return nil, errors.New("subscription reservation amount is out of range")
	}
	now := dbtime.Timestamp(s.db.WithContext(ctx))
	var result *SubscriptionPreConsumeResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := SubscriptionPreConsumeRecord{RequestId: requestId, UserId: userId, PreConsumed: amount, Status: "pending"}
		insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&record)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			if err := tx.Where("request_id = ?", requestId).First(&record).Error; err != nil {
				return err
			}
			if record.UserId != userId {
				return errors.New("subscription reservation belongs to another user")
			}
			if record.Status != "consumed" {
				return errors.New("subscription pre-consume already refunded or unavailable")
			}
			var sub UserSubscription
			if err := tx.Where("id = ? AND user_id = ?", record.UserSubscriptionId, userId).First(&sub).Error; err != nil {
				return err
			}
			result = &SubscriptionPreConsumeResult{UserSubscriptionId: sub.Id, PreConsumed: record.PreConsumed, AmountTotal: sub.AmountTotal, AmountUsedBefore: sub.AmountUsed, AmountUsedAfter: sub.AmountUsed}
			return nil
		}

		var candidates []int
		if err := tx.Model(&UserSubscription{}).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
			Where("amount_total <= 0 OR amount_used::numeric + ? <= amount_total OR next_reset_time <= ?", amount, now).
			Order("end_time asc, id asc").Pluck("id", &candidates).Error; err != nil {
			return err
		}
		for _, id := range candidates {
			var sub UserSubscription
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND status = ? AND end_time > ?", id, userId, "active", now).First(&sub).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if sub.NextResetTime <= now {
				plan, err := s.catalog.Plan(ctx, tx, sub.PlanId)
				if err != nil {
					return err
				}
				if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
					return err
				}
			}
			if sub.AmountTotal > 0 && (sub.AmountUsed > sub.AmountTotal || amount > sub.AmountTotal-sub.AmountUsed) {
				continue
			}
			result, err = adjustSubscriptionUsage(tx, sub.Id, amount)
			if err != nil {
				return err
			}
			result.PreConsumed = amount
			return tx.Model(&record).Updates(map[string]any{"user_subscription_id": sub.Id, "status": "consumed"}).Error
		}
		return fmt.Errorf("%w, need=%d", contract.ErrSubscriptionQuotaInsufficient, amount)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func (s *Store) RefundSubscriptionPreConsume(ctx context.Context, requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record SubscriptionPreConsumeRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.PreConsumed <= 0 {
			record.Status = "refunded"
			return tx.Save(&record).Error
		}
		if _, err := adjustSubscriptionUsage(tx, record.UserSubscriptionId, -record.PreConsumed); err != nil {
			return err
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// AdjustSubscriptionPreConsume changes the refundable reservation and usage in
// the same transaction, including extra reservations made during channel retries.
func (s *Store) AdjustSubscriptionPreConsume(ctx context.Context, requestId string, delta int64) (*SubscriptionPreConsumeResult, error) {
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	var result *SubscriptionPreConsumeResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record SubscriptionPreConsumeRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status != "consumed" || record.PreConsumed < 0 || record.PreConsumed > int64(common.MaxQuota) {
			return errors.New("subscription reservation is unavailable")
		}
		if delta > int64(common.MaxQuota)-record.PreConsumed || delta < -record.PreConsumed {
			return errors.New("subscription reservation adjustment is out of range")
		}
		var err error
		result, err = adjustSubscriptionUsage(tx, record.UserSubscriptionId, delta)
		if err != nil {
			return err
		}
		record.PreConsumed += delta
		result.PreConsumed = record.PreConsumed
		return tx.Model(&record).Update("pre_consumed", record.PreConsumed).Error
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
