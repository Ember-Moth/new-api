package memberships

import (
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"gorm.io/gorm"
)

type UserGroups struct {
	Lock    func(*gorm.DB, int) (string, error)
	Set     func(*gorm.DB, int, string) error
	Refresh func(int) error
}

type Dependencies struct {
	DB     *gorm.DB
	Plan   func(*gorm.DB, int) (*entity.SubscriptionPlan, error)
	Groups UserGroups
}

type Store struct {
	db     *gorm.DB
	plan   func(*gorm.DB, int) (*entity.SubscriptionPlan, error)
	groups UserGroups
}

type UserSubscription = entity.UserSubscription
type SubscriptionPlan = entity.SubscriptionPlan
type SubscriptionSummary = contract.SubscriptionSummary
type SubscriptionResetResult = contract.SubscriptionResetResult

func New(deps Dependencies) *Store { return &Store{db: deps.DB, plan: deps.Plan, groups: deps.Groups} }
