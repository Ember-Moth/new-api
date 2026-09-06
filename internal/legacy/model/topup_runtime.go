package model

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/topups"
	"github.com/QuantumNous/new-api/internal/module/identity"
)

var topUpStores sync.Map

func TopUpStore() *topups.Store {
	key := subscriptionRuntimeKey{db: DB, redis: common.RDB}
	if value, ok := topUpStores.Load(key); ok {
		return value.(*topups.Store)
	}
	accounts := identity.New(identity.Dependencies{DB: DB})
	store := topups.New(topups.Dependencies{DB: DB, QuotaPerUnit: func() float64 { return common.QuotaPerUnit }, Customer: accounts.ApplyPaymentCustomer, PublishCustomer: PublishUserAuthCache, AfterCredit: func(id, amount int) error { return cacheIncrUserQuota(id, int64(amount)) }, Log: func(ctx context.Context, event billingcontract.TopUpEvent) {
		if event.Provider == "" {
			LogService().RecordLog(ctx, event.UserID, LogTypeTopup, event.Content)
			return
		}
		LogService().RecordTopupLog(ctx, event.UserID, event.Content, event.CallerIP, event.Method, event.Provider)
	}})
	actual, _ := topUpStores.LoadOrStore(key, store)
	return actual.(*topups.Store)
}
