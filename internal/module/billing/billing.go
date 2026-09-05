package billing

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/module/billing/internal/repo"
	"gorm.io/gorm"
)

var ErrPaymentComplianceRequired = errors.New("payment compliance confirmation required")

type WalletRuntime struct {
	Credit func(int, int) error
	Debit  func(int, int) error
}

type Dependencies struct {
	WalletRuntime  WalletRuntime
	DB             *gorm.DB
	PaymentAllowed func() bool
}

type Service struct {
	wallets        *repo.Wallets
	walletRuntime  WalletRuntime
	redemptions    *repo.Redemptions
	paymentAllowed func() bool
}

func New(deps Dependencies) *Service {
	return &Service{wallets: repo.NewWallets(deps.DB), walletRuntime: deps.WalletRuntime, redemptions: repo.NewRedemptions(deps.DB), paymentAllowed: deps.PaymentAllowed}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
