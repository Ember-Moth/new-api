package quota

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/internal/dbtime"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause" // PreConsumeUserSubscription pre-consumes from any active subscription total quota.
)

func (s *Store) PreConsumeUserSubscription(ctx context.Context, requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if err := validatePreConsume(requestId, userId, amount); err != nil {
		return nil, err
	}
	var result *SubscriptionPreConsumeResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = s.preConsumeUserSubscriptionTx(ctx, tx, requestId, userId, modelName, quotaType, amount)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// PreConsumeUserSubscriptionTx is the transaction-bound form used when a
// billing session also mutates a wallet or token. The caller owns tx's
// commit/rollback.
func (s *Store) PreConsumeUserSubscriptionTx(ctx context.Context, tx *gorm.DB, requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if err := validatePreConsume(requestId, userId, amount); err != nil {
		return nil, err
	}
	return s.preConsumeUserSubscriptionTx(ctx, tx, requestId, userId, modelName, quotaType, amount)
}

func validatePreConsume(requestId string, userId int, amount int64) error {
	if userId <= 0 || strings.TrimSpace(requestId) == "" || len(requestId) > 64 {
		return errors.New("invalid subscription reservation identity")
	}
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return errors.New("subscription reservation amount is out of range")
	}
	return nil
}

func (s *Store) preConsumeUserSubscriptionTx(ctx context.Context, tx *gorm.DB, requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if tx == nil {
		return nil, errors.New("subscription transaction is nil")
	}
	now := dbtime.Timestamp(tx.WithContext(ctx))
	var result *SubscriptionPreConsumeResult
	record := SubscriptionPreConsumeRecord{RequestId: requestId, UserId: userId, PreConsumed: amount, Status: "pending"}
	insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&record)
	if insert.Error != nil {
		return nil, insert.Error
	}
	if insert.RowsAffected == 0 {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return nil, err
		}
		if record.UserId != userId {
			return nil, errors.New("subscription reservation belongs to another user")
		}
		if record.Status != "consumed" {
			return nil, errors.New("subscription pre-consume already refunded or unavailable")
		}
		var sub UserSubscription
		if err := tx.Where("id = ? AND user_id = ?", record.UserSubscriptionId, userId).First(&sub).Error; err != nil {
			return nil, err
		}
		return &SubscriptionPreConsumeResult{UserSubscriptionId: sub.Id, PreConsumed: record.PreConsumed, AmountTotal: sub.AmountTotal, AmountUsedBefore: sub.AmountUsed, AmountUsedAfter: sub.AmountUsed}, nil
	}

	var candidates []int
	if err := tx.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Where("amount_total <= 0 OR amount_used::numeric + ? <= amount_total OR next_reset_time <= ?", amount, now).
		Order("end_time asc, id asc").Pluck("id", &candidates).Error; err != nil {
		return nil, err
	}
	for _, id := range candidates {
		var sub UserSubscription
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND status = ? AND end_time > ?", id, userId, "active", now).First(&sub).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if sub.NextResetTime <= now {
			plan, err := s.catalog.Plan(ctx, tx, sub.PlanId)
			if err != nil {
				return nil, err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return nil, err
			}
		}
		if sub.AmountTotal > 0 && (sub.AmountUsed > sub.AmountTotal || amount > sub.AmountTotal-sub.AmountUsed) {
			continue
		}
		result, err = adjustSubscriptionUsage(tx, sub.Id, amount)
		if err != nil {
			return nil, err
		}
		result.PreConsumed = amount
		if err := tx.Model(&record).Updates(map[string]any{"user_subscription_id": sub.Id, "status": "consumed"}).Error; err != nil {
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("%w, need=%d", contract.ErrSubscriptionQuotaInsufficient, amount)
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func (s *Store) RefundSubscriptionPreConsume(ctx context.Context, requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.RefundSubscriptionPreConsumeTx(ctx, tx, requestId)
	})
}

// RefundSubscriptionPreConsumeTx is the transaction-bound idempotent
// subscription refund used by the billing session lifecycle.
func (s *Store) RefundSubscriptionPreConsumeTx(ctx context.Context, tx *gorm.DB, requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	if tx == nil {
		return errors.New("subscription transaction is nil")
	}
	var record SubscriptionPreConsumeRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("request_id = ?", requestId).First(&record).Error; err != nil {
		return err
	}
	if record.Status == "refunded" {
		return nil
	}
	if record.Status != "consumed" {
		return errors.New("subscription pre-consume is unavailable")
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
}

// AdjustSubscriptionPreConsume changes the refundable reservation and usage in
// the same transaction, including extra reservations made during channel retries.
func (s *Store) AdjustSubscriptionPreConsume(ctx context.Context, requestId string, delta int64) (*SubscriptionPreConsumeResult, error) {
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	var result *SubscriptionPreConsumeResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = s.AdjustSubscriptionPreConsumeTx(ctx, tx, requestId, delta)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// AdjustSubscriptionPreConsumeTx changes a request's reservation while the
// caller's transaction also updates another billing ledger.
func (s *Store) AdjustSubscriptionPreConsumeTx(ctx context.Context, tx *gorm.DB, requestId string, delta int64) (*SubscriptionPreConsumeResult, error) {
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if tx == nil {
		return nil, errors.New("subscription transaction is nil")
	}
	var record SubscriptionPreConsumeRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&record).Error; err != nil {
		return nil, err
	}
	if record.Status != "consumed" || record.PreConsumed < 0 || record.PreConsumed > int64(common.MaxQuota) {
		return nil, errors.New("subscription reservation is unavailable")
	}
	if delta > int64(common.MaxQuota)-record.PreConsumed || delta < -record.PreConsumed {
		return nil, errors.New("subscription reservation adjustment is out of range")
	}
	result, err := adjustSubscriptionUsage(tx, record.UserSubscriptionId, delta)
	if err != nil {
		return nil, err
	}
	record.PreConsumed += delta
	result.PreConsumed = record.PreConsumed
	if err := tx.Model(&record).Update("pre_consumed", record.PreConsumed).Error; err != nil {
		return nil, err
	}
	return result, nil
}
