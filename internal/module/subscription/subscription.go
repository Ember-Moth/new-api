package subscription

import (
	"context"
	"errors"
	"sync"
	"time"

	identitycontract "github.com/QuantumNous/new-api/internal/module/identity/contract"

	"github.com/QuantumNous/new-api/internal/module/subscription/payments"

	"github.com/QuantumNous/new-api/internal/module/subscription/quota"

	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"

	"github.com/QuantumNous/new-api/internal/module/subscription/internal/repo"
	"gorm.io/gorm"
)

var ErrPaymentComplianceRequired = errors.New("payment compliance confirmation required")

type Dependencies struct {
	Gateways          CheckoutGateways
	CheckoutBuyer     func(context.Context, int) (*identitycontract.CheckoutBuyer, error)
	BillingPreference func(context.Context, int) (string, error)
	Payments          *payments.Store
	Quota             *quota.Store
	Members           *memberships.Store
	DB                *gorm.DB
	PaymentAllowed    func() bool
	GroupExists       func(string) bool
	InvalidatePlan    func(int)
}

type Service struct {
	Gateways          CheckoutGateways
	checkoutBuyer     func(context.Context, int) (*identitycontract.CheckoutBuyer, error)
	billingPreference func(context.Context, int) (string, error)
	Payments          *payments.Store
	Quota             *quota.Store
	maintenanceMu     sync.Mutex
	lastCleanup       time.Time
	Members           *memberships.Store
	plans             *repo.Plans
	paymentAllowed    func() bool
	groupExists       func(string) bool
	invalidatePlan    func(int)
}

func New(deps Dependencies) *Service {
	return &Service{Gateways: deps.Gateways, checkoutBuyer: deps.CheckoutBuyer, billingPreference: deps.BillingPreference, Payments: deps.Payments, Quota: deps.Quota, Members: deps.Members, plans: repo.NewPlans(deps.DB), paymentAllowed: deps.PaymentAllowed, groupExists: deps.GroupExists, invalidatePlan: deps.InvalidatePlan}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
