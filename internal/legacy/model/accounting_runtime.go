package model

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/accounting"
)

var accountingStores sync.Map

// AccountingStore shares the module-owned ledger across legacy runtime callers.
func AccountingStore() *accounting.Store {
	key := subscriptionRuntimeKey{db: DB, redis: common.RDB}
	if value, ok := accountingStores.Load(key); ok {
		return value.(*accounting.Store)
	}
	store := accounting.New(accounting.Dependencies{DB: DB, Redis: common.RDB, CacheEnabled: func() bool { return common.RedisEnabled }, BatchEnabled: func() bool { return common.BatchUpdateEnabled }})
	actual, _ := accountingStores.LoadOrStore(key, store)
	return actual.(*accounting.Store)
}
func IncreaseUserQuota(id, quota int, direct bool) error {
	return AccountingStore().IncreaseUserQuota(context.Background(), id, quota, direct)
}
func DecreaseUserQuota(id, quota int, direct bool) error {
	return AccountingStore().DecreaseUserQuota(context.Background(), id, quota, direct)
}
func DeltaUpdateUserQuota(id, delta int) error {
	return AccountingStore().DeltaUpdateUserQuota(context.Background(), id, delta)
}
func IncreaseTokenQuota(id int, key string, quota int) error {
	return AccountingStore().IncreaseTokenQuota(context.Background(), id, key, quota)
}
func DecreaseTokenQuota(id int, key string, quota int) error {
	return AccountingStore().DecreaseTokenQuota(context.Background(), id, key, quota)
}
func TryReserveUserQuota(id, quota int) (bool, error) {
	return AccountingStore().TryReserveUserQuota(context.Background(), id, quota)
}
func TryReserveTokenQuota(id int, key string, quota int, unlimited bool) (bool, error) {
	return AccountingStore().TryReserveTokenQuota(context.Background(), id, key, quota, unlimited)
}
func UpdateUserUsedQuotaAndRequestCount(id, quota int) {
	if err := AccountingStore().RecordUsage(context.Background(), id, quota, 1); err != nil {
		common.SysLog("failed to update user usage: " + err.Error())
	}
}
func UpdateUserUsedQuota(id, quota int) {
	if err := AccountingStore().RecordUsage(context.Background(), id, quota, 0); err != nil {
		common.SysLog("failed to update user usage: " + err.Error())
	}
}
func cacheIncrUserQuota(id int, delta int64) error {
	return AccountingStore().PublishUserDelta(context.Background(), id, delta)
}
func cacheDecrUserQuota(id int, delta int64) error {
	return AccountingStore().PublishUserDelta(context.Background(), id, -delta)
}
func FlushQuotaUpdates() error { return AccountingStore().Flush(context.Background()) }
