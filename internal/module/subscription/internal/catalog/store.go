package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
	"gorm.io/gorm"
)

type Dependencies struct {
	DB                                                         *gorm.DB
	Redis                                                      *redis.Client
	RedisEnabled                                               func() bool
	PlanTTLSeconds, InfoTTLSeconds, PlanCapacity, InfoCapacity int
}

type Store struct {
	db               *gorm.DB
	plans            *cachex.HybridCache[entity.SubscriptionPlan]
	info             *cachex.HybridCache[contract.SubscriptionPlanInfo]
	planTTL, infoTTL time.Duration
}

func New(deps Dependencies) *Store {
	if deps.PlanTTLSeconds <= 0 {
		deps.PlanTTLSeconds = 300
	}
	if deps.InfoTTLSeconds <= 0 {
		deps.InfoTTLSeconds = 120
	}
	if deps.PlanCapacity <= 0 {
		deps.PlanCapacity = 5000
	}
	if deps.InfoCapacity <= 0 {
		deps.InfoCapacity = 10000
	}
	planTTL := time.Duration(min(deps.PlanTTLSeconds, int(math.MaxInt64/int64(time.Second)))) * time.Second
	infoTTL := time.Duration(min(deps.InfoTTLSeconds, int(math.MaxInt64/int64(time.Second)))) * time.Second
	return &Store{db: deps.DB, planTTL: planTTL, infoTTL: infoTTL,
		plans: cachex.NewHybridCache[entity.SubscriptionPlan](cachex.HybridCacheConfig[entity.SubscriptionPlan]{
			Namespace: "new-api:subscription_plan:v1", Redis: deps.Redis, RedisEnabled: deps.RedisEnabled, RedisCodec: cachex.JSONCodec[entity.SubscriptionPlan]{},
			Memory: func() *hot.HotCache[string, entity.SubscriptionPlan] {
				return hot.NewHotCache[string, entity.SubscriptionPlan](hot.LRU, deps.PlanCapacity).WithTTL(planTTL).Build()
			},
		}),
		info: cachex.NewHybridCache[contract.SubscriptionPlanInfo](cachex.HybridCacheConfig[contract.SubscriptionPlanInfo]{
			Namespace: "new-api:subscription_plan_info:v1", Redis: deps.Redis, RedisEnabled: deps.RedisEnabled, RedisCodec: cachex.JSONCodec[contract.SubscriptionPlanInfo]{},
			Memory: func() *hot.HotCache[string, contract.SubscriptionPlanInfo] {
				return hot.NewHotCache[string, contract.SubscriptionPlanInfo](hot.LRU, deps.InfoCapacity).WithTTL(infoTTL).Build()
			},
		}),
	}
}

// Plan bypasses both cache reads and publication in a transaction. Financial
// decisions see transactional SQL state, and rolled-back data cannot escape.
func (s *Store) Plan(ctx context.Context, tx *gorm.DB, id int) (*entity.SubscriptionPlan, error) {
	if id <= 0 {
		return nil, errors.New("invalid plan id")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := strconv.Itoa(id)
	if tx == nil {
		if cached, found, err := s.plans.Get(key); err == nil && found {
			cached.NormalizeDefaults()
			return &cached, nil
		}
	}
	query := s.db
	if tx != nil {
		query = tx
	}
	var plan entity.SubscriptionPlan
	if err := query.WithContext(ctx).First(&plan, id).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	if tx == nil {
		_ = s.plans.SetWithTTL(key, plan, s.planTTL)
	}
	return &plan, nil
}

func (s *Store) PlanInfo(ctx context.Context, subscriptionID int) (*contract.SubscriptionPlanInfo, error) {
	if subscriptionID <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("sub:%d", subscriptionID)
	if cached, found, err := s.info.Get(key); err == nil && found {
		return &cached, nil
	}
	var sub entity.UserSubscription
	if err := s.db.WithContext(ctx).Select("id", "plan_id").First(&sub, subscriptionID).Error; err != nil {
		return nil, err
	}
	plan, err := s.Plan(ctx, nil, sub.PlanId)
	if err != nil {
		return nil, err
	}
	info := contract.SubscriptionPlanInfo{PlanId: sub.PlanId, PlanTitle: plan.Title}
	_ = s.info.SetWithTTL(key, info, s.infoTTL)
	return &info, nil
}

func (s *Store) Invalidate(planID int) error {
	if planID <= 0 {
		return nil
	}
	_, err := s.plans.DeleteMany([]string{strconv.Itoa(planID)})
	return errors.Join(err, s.info.Purge())
}
