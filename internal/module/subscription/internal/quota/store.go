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

// WithTx binds quota operations to an existing PostgreSQL transaction. This
// is used by billing sessions so the subscription reservation, token debit,
// and durable lifecycle record commit or roll back together.
func (s *Store) WithTx(tx *gorm.DB) *Store {
	if tx == nil {
		return nil
	}
	return &Store{db: tx, catalog: s.catalog}
}
