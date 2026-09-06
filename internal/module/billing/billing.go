package billing

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/module/billing/accounting"

	"github.com/QuantumNous/new-api/internal/module/billing/paymentconfig"

	"github.com/QuantumNous/new-api/internal/module/billing/purchases"

	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"

	"github.com/QuantumNous/new-api/internal/module/billing/topups"

	"github.com/QuantumNous/new-api/internal/module/billing/internal/repo"
	"gorm.io/gorm"
)

var ErrPaymentComplianceRequired = errors.New("payment compliance confirmation required")

type Dependencies struct {
	PaymentConfig  *paymentconfig.Service
	Purchases      *purchases.Service
	Webhooks       *webhooks.Processor
	TopUps         *topups.Store
	Accounting     *accounting.Store
	DB             *gorm.DB
	PaymentAllowed func() bool
}

type Service struct {
	PaymentConfig  *paymentconfig.Service
	Purchases      *purchases.Service
	Webhooks       *webhooks.Processor
	TopUps         *topups.Store
	wallets        *repo.Wallets
	accounting     *accounting.Store
	redemptions    *repo.Redemptions
	paymentAllowed func() bool
}

func New(deps Dependencies) *Service {
	if deps.Accounting == nil {
		deps.Accounting = accounting.New(accounting.Dependencies{DB: deps.DB})
	}
	return &Service{PaymentConfig: deps.PaymentConfig, Purchases: deps.Purchases, Webhooks: deps.Webhooks, TopUps: deps.TopUps, wallets: repo.NewWallets(deps.DB), accounting: deps.Accounting, redemptions: repo.NewRedemptions(deps.DB), paymentAllowed: deps.PaymentAllowed}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
