package subscription

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"

	"github.com/QuantumNous/new-api/internal/module/subscription/internal/repo"
	"gorm.io/gorm"
)

var ErrPaymentComplianceRequired = errors.New("payment compliance confirmation required")

type Dependencies struct {
	Members        *memberships.Store
	DB             *gorm.DB
	PaymentAllowed func() bool
	GroupExists    func(string) bool
	InvalidatePlan func(int)
}

type Service struct {
	Members        *memberships.Store
	plans          *repo.Plans
	paymentAllowed func() bool
	groupExists    func(string) bool
	invalidatePlan func(int)
}

func New(deps Dependencies) *Service {
	return &Service{Members: deps.Members, plans: repo.NewPlans(deps.DB), paymentAllowed: deps.PaymentAllowed, groupExists: deps.GroupExists, invalidatePlan: deps.InvalidatePlan}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
