package billing

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/module/billing/internal/repo"
	"gorm.io/gorm"
)

var ErrPaymentComplianceRequired = errors.New("payment compliance confirmation required")

type Dependencies struct {
	DB             *gorm.DB
	PaymentAllowed func() bool
}

type Service struct {
	redemptions    *repo.Redemptions
	paymentAllowed func() bool
}

func New(deps Dependencies) *Service {
	return &Service{redemptions: repo.NewRedemptions(deps.DB), paymentAllowed: deps.PaymentAllowed}
}

func (s *Service) RequirePaymentCompliance() error {
	if s.paymentAllowed == nil || !s.paymentAllowed() {
		return ErrPaymentComplianceRequired
	}
	return nil
}
