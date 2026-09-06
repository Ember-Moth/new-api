package accounting

import (
	"context"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	BatchUpdateTypeCount
)

const quotaBatchDeliveryLimit = 500

// quotaBatchDelivery is an append-only pending accounting intent. It is
// deleted in the same transaction as the corresponding balance/statistics
// updates, so a row is either still deliverable or its effects are committed.
type quotaBatchDelivery struct {
	ID         uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	UpdateType int       `gorm:"column:update_type"`
	EntityID   int64     `gorm:"column:entity_id"`
	Delta      int64     `gorm:"column:delta"`
}

func (quotaBatchDelivery) TableName() string { return "quota_batch_deliveries" }

type quotaBatchDelta struct {
	UpdateType int
	EntityID   int
	Delta      int
}

func (s *Store) addNewRecord(ctx context.Context, type_, id, value int) error {
	return s.enqueueBatchDeltas(ctx, []quotaBatchDelta{{UpdateType: type_, EntityID: id, Delta: value}})
}

func (s *Store) enqueueBatchDeltas(ctx context.Context, deltas []quotaBatchDelta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rows := make([]quotaBatchDelivery, 0, len(deltas))
	for _, delta := range deltas {
		if delta.Delta == 0 {
			continue
		}
		if delta.UpdateType < 0 || delta.UpdateType >= BatchUpdateTypeCount {
			return fmt.Errorf("invalid quota batch update type: %d", delta.UpdateType)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("identify quota batch delivery: %w", err)
		}
		rows = append(rows, quotaBatchDelivery{
			ID:         id,
			UpdateType: delta.UpdateType,
			EntityID:   int64(delta.EntityID),
			Delta:      int64(delta.Delta),
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&rows).Error
	})
}

// flushOne atomically claims, applies, and removes one durable batch. Row
// locks are held only for the transaction, so another accounting instance can
// take over after a worker process dies.
func (s *Store) flushOne(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var deliveries []quotaBatchDelivery
		if err := tx.Raw(`
SELECT id, update_type, entity_id, delta
FROM quota_batch_deliveries
ORDER BY created_at, id
LIMIT ?
FOR UPDATE SKIP LOCKED`, quotaBatchDeliveryLimit).Scan(&deliveries).Error; err != nil {
			return err
		}
		if len(deliveries) == 0 {
			return nil
		}
		claimed = true

		stores := make([]map[int]int, BatchUpdateTypeCount)
		for i := range stores {
			stores[i] = make(map[int]int)
		}
		for _, delivery := range deliveries {
			if delivery.EntityID < 1 || int64(int(delivery.EntityID)) != delivery.EntityID {
				return fmt.Errorf("invalid quota batch entity id: %d", delivery.EntityID)
			}
			if delivery.UpdateType < 0 || delivery.UpdateType >= BatchUpdateTypeCount {
				return fmt.Errorf("invalid quota batch update type: %d", delivery.UpdateType)
			}
			old := int64(stores[delivery.UpdateType][int(delivery.EntityID)])
			if (delivery.Delta > 0 && old > math.MaxInt64-delivery.Delta) ||
				(delivery.Delta < 0 && old < math.MinInt64-delivery.Delta) {
				common.SysError(fmt.Sprintf("quota batch delivery overflow: type=%d id=%d old=%d delta=%d", delivery.UpdateType, delivery.EntityID, old, delivery.Delta))
				return fmt.Errorf("quota batch delivery overflow: type=%d id=%d", delivery.UpdateType, delivery.EntityID)
			}
			sum := old + delivery.Delta
			if sum > int64(math.MaxInt) || sum < int64(math.MinInt) {
				common.SysError(fmt.Sprintf("quota batch integer overflow: type=%d id=%d sum=%d", delivery.UpdateType, delivery.EntityID, sum))
				return fmt.Errorf("quota batch integer overflow: type=%d id=%d", delivery.UpdateType, delivery.EntityID)
			}
			stores[delivery.UpdateType][int(delivery.EntityID)] = int(sum)
		}

		batchID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("identify quota batch: %w", err)
		}
		if err := s.applyQuotaBatchTx(tx, &quotaBatch{ID: batchID.String(), Stores: stores}); err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(deliveries))
		for _, delivery := range deliveries {
			ids = append(ids, delivery.ID)
		}
		result := tx.Where("id IN ?", ids).Delete(&quotaBatchDelivery{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("quota batch delivery delete mismatch: want=%d got=%d", len(ids), result.RowsAffected)
		}
		return nil
	})
	return claimed, err
}

// Flush drains pending deltas after request handling has quiesced.
func (s *Store) Flush(ctx context.Context) error {
	s.batchFlushMutex.Lock()
	defer s.batchFlushMutex.Unlock()
	for {
		claimed, err := s.flushOne(ctx)
		if err != nil {
			common.SysError("quota batch flush retained durable deliveries: " + err.Error())
			return err
		}
		if !claimed {
			return nil
		}
	}
}
