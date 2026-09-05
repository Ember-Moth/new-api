package model

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	BatchUpdateTypeCount // if you add a new type, you need to add a new map and a new lock
)

var batchUpdateStores []map[int]int
var batchUpdateLocks []sync.Mutex
var batchFlushMutex sync.Mutex
var pendingQuotaBatch *quotaBatch

func init() {
	for i := 0; i < BatchUpdateTypeCount; i++ {
		batchUpdateStores = append(batchUpdateStores, make(map[int]int))
		batchUpdateLocks = append(batchUpdateLocks, sync.Mutex{})
	}
}

func InitBatchUpdater() {
	gopool.Go(func() {
		for {
			time.Sleep(time.Duration(common.BatchUpdateInterval) * time.Second)
			batchUpdate()
		}
	})
}

func addNewRecord(type_ int, id int, value int) {
	batchUpdateLocks[type_].Lock()
	defer batchUpdateLocks[type_].Unlock()
	old, ok := batchUpdateStores[type_][id]
	if !ok {
		batchUpdateStores[type_][id] = value
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
	batchUpdateStores[type_][id] = sum
}

func batchUpdate() error {
	batchFlushMutex.Lock()
	defer batchFlushMutex.Unlock()
	if pendingQuotaBatch == nil {
		id, err := uuid.NewV7()
		if err != nil {
			common.SysError("cannot identify quota batch: " + err.Error())
			return err
		}
		stores := make([]map[int]int, BatchUpdateTypeCount)
		hasData := false
		for i := range stores {
			batchUpdateLocks[i].Lock()
			stores[i] = batchUpdateStores[i]
			batchUpdateStores[i] = make(map[int]int)
			batchUpdateLocks[i].Unlock()
			hasData = hasData || len(stores[i]) > 0
		}
		if !hasData {
			return nil
		}
		pendingQuotaBatch = &quotaBatch{ID: id.String(), Stores: stores}
	}
	if err := applyQuotaBatch(pendingQuotaBatch); err != nil {
		common.SysError(fmt.Sprintf("quota batch %s retained for retry: %v", pendingQuotaBatch.ID, err))
		return err
	}
	id := pendingQuotaBatch.ID
	pendingQuotaBatch = nil
	// Once COMMIT has been acknowledged this process will never retry this ID.
	// Failed cleanup is harmless; an orphan receipt cannot apply a charge again.
	if err := DB.Exec("DELETE FROM quota_batch_receipts WHERE id = ?::uuid", id).Error; err != nil {
		common.SysError(fmt.Sprintf("quota batch %s receipt cleanup failed: %v", id, err))
	}
	return nil
}

// FlushQuotaUpdates drains pending deltas after request handling has quiesced.
func FlushQuotaUpdates() error {
	for {
		if err := batchUpdate(); err != nil {
			return err
		}
		pending := false
		for i := range batchUpdateStores {
			batchUpdateLocks[i].Lock()
			pending = pending || len(batchUpdateStores[i]) > 0
			batchUpdateLocks[i].Unlock()
		}
		if !pending {
			return nil
		}
	}
}

func RecordExist(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func shouldUpdateRedis(fromDB bool, err error) bool {
	return common.RedisEnabled && fromDB && err == nil
}
