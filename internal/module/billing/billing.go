package billing

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"

	"github.com/QuantumNous/new-api/internal/module/billing/pricesync"

	"github.com/QuantumNous/new-api/internal/module/billing/pricing"

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
	StatementConfig func() contract.StatementConfig
	RewardConfig    func() contract.RewardConfig
	RewardLog       func(context.Context, int, string)
	Now             func() time.Time

	PriceSync      *pricesync.Service
	Pricing        *pricing.Service
	PaymentConfig  *paymentconfig.Service
	Purchases      *purchases.Service
	Webhooks       *webhooks.Processor
	TopUps         *topups.Store
	Accounting     *accounting.Store
	DB             *gorm.DB
	PaymentAllowed func() bool
}

type Service struct {
	statementConfig func() contract.StatementConfig
	statements      *repo.Statements
	rewardConfig    func() contract.RewardConfig
	rewardLog       func(context.Context, int, string)
	now             func() time.Time
	checkins        *repo.Checkins

	PriceSync      *pricesync.Service
	Pricing        *pricing.Service
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
	if deps.StatementConfig == nil {
		deps.StatementConfig = func() contract.StatementConfig { return contract.StatementConfig{QuotaPerUnit: 1} }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.RewardConfig == nil {
		deps.RewardConfig = func() contract.RewardConfig { return contract.RewardConfig{} }
	}
	if deps.Accounting == nil {
		deps.Accounting = accounting.New(accounting.Dependencies{DB: deps.DB})
	}
	return &Service{statementConfig: deps.StatementConfig, statements: repo.NewStatements(deps.DB), rewardConfig: deps.RewardConfig, rewardLog: deps.RewardLog, now: deps.Now, checkins: repo.NewCheckins(deps.DB), PriceSync: deps.PriceSync, Pricing: deps.Pricing, PaymentConfig: deps.PaymentConfig, Purchases: deps.Purchases, Webhooks: deps.Webhooks, TopUps: deps.TopUps, wallets: repo.NewWallets(deps.DB), accounting: deps.Accounting, redemptions: repo.NewRedemptions(deps.DB), paymentAllowed: deps.PaymentAllowed}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
