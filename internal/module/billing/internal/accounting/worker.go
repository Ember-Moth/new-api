package accounting

import (
	"context"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	BatchUpdateTypeCount // if you add a new type, you need to add a new map and a new lock
)

func (s *Store) addNewRecord(type_ int, id int, value int) {
	s.batchUpdateLocks[type_].Lock()
	defer s.batchUpdateLocks[type_].Unlock()
	old, ok := s.batchUpdateStores[type_][id]
	if !ok {
		s.batchUpdateStores[type_][id] = value
		return
	}

	sum := old + value
	if (value > 0 && sum < old) || (value < 0 && sum > old) {
		common.SysError(fmt.Sprintf("batch update overflow: type=%d id=%d old=%d value=%d", type_, id, old, value))
		if value > 0 {
			sum = math.MaxInt
		} else {
			sum = math.MinInt
		}
	}
	s.batchUpdateStores[type_][id] = sum
}

func (s *Store) batchUpdate(ctx context.Context) error {
	s.batchFlushMutex.Lock()
	defer s.batchFlushMutex.Unlock()
	if s.pendingQuotaBatch == nil {
		id, err := uuid.NewV7()
		if err != nil {
			common.SysError("cannot identify quota batch: " + err.Error())
			return err
		}
		stores := make([]map[int]int, BatchUpdateTypeCount)
		hasData := false
		for i := range stores {
			s.batchUpdateLocks[i].Lock()
			stores[i] = s.batchUpdateStores[i]
			s.batchUpdateStores[i] = make(map[int]int)
			s.batchUpdateLocks[i].Unlock()
			hasData = hasData || len(stores[i]) > 0
		}
		if !hasData {
			return nil
		}
		s.pendingQuotaBatch = &quotaBatch{ID: id.String(), Stores: stores}
	}
	if err := s.applyQuotaBatch(ctx, s.pendingQuotaBatch); err != nil {
		common.SysError(fmt.Sprintf("quota batch %s retained for retry: %v", s.pendingQuotaBatch.ID, err))
		return err
	}
	id := s.pendingQuotaBatch.ID
	s.pendingQuotaBatch = nil
	// Once COMMIT has been acknowledged this process will never retry this ID.
	// Failed cleanup is harmless; an orphan receipt cannot apply a charge again.
	if err := s.db.WithContext(ctx).Exec("DELETE FROM quota_batch_receipts WHERE id = ?::uuid", id).Error; err != nil {
		common.SysError(fmt.Sprintf("quota batch %s receipt cleanup failed: %v", id, err))
	}
	return nil
}

// Flush drains pending deltas after request handling has quiesced.
func (s *Store) Flush(ctx context.Context) error {
	for {
		if err := s.batchUpdate(ctx); err != nil {
			return err
		}
		pending := false
		for i := range s.batchUpdateStores {
			s.batchUpdateLocks[i].Lock()
			pending = pending || len(s.batchUpdateStores[i]) > 0
			s.batchUpdateLocks[i].Unlock()
		}
		if !pending {
			return nil
		}
	}
}
