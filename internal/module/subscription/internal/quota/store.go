package quota

import (
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"gorm.io/gorm"
)

type Store struct {
	db      *gorm.DB
	catalog *catalog.Store
}
type SubscriptionPreConsumeResult = contract.SubscriptionPreConsumeResult
type SubscriptionPreConsumeRecord = entity.SubscriptionPreConsumeRecord
type UserSubscription = entity.UserSubscription
type SubscriptionPlan = entity.SubscriptionPlan

func New(db *gorm.DB, catalog *catalog.Store) *Store { return &Store{db: db, catalog: catalog} }
