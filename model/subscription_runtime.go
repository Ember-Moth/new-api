package model

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type subscriptionRuntimeKey struct {
	db    *gorm.DB
	redis *redis.Client
}

var subscriptionCatalogs sync.Map
var subscriptionQuotaStores sync.Map

func SubscriptionCatalog() *catalog.Store {
	key := subscriptionRuntimeKey{db: DB, redis: common.RDB}
	if value, ok := subscriptionCatalogs.Load(key); ok {
		return value.(*catalog.Store)
	}
	store := catalog.New(catalog.Dependencies{DB: DB, Redis: common.RDB, RedisEnabled: func() bool { return common.RedisEnabled },
		PlanTTLSeconds: common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_TTL", 300), InfoTTLSeconds: common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_TTL", 120),
		PlanCapacity: common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_CAP", 5000), InfoCapacity: common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_CAP", 10000)})
	actual, _ := subscriptionCatalogs.LoadOrStore(key, store)
	return actual.(*catalog.Store)
}

func SubscriptionQuota() *quota.Store {
	key := subscriptionRuntimeKey{db: DB, redis: common.RDB}
	if value, ok := subscriptionQuotaStores.Load(key); ok {
		return value.(*quota.Store)
	}
	store := quota.New(DB, SubscriptionCatalog())
	actual, _ := subscriptionQuotaStores.LoadOrStore(key, store)
	return actual.(*quota.Store)
}
