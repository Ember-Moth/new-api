package billing

import (
	"errors"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/internal/repo"
	"gorm.io/gorm"
)

// DebitWalletInTx joins an existing purchase transaction. Cache publication
// happens after the caller commits the wallet, order and purchased resource.
func (s *Service) DebitWalletInTx(tx *gorm.DB, id, amount int) error {
	if tx == nil || id <= 0 || amount < 0 {
		return errors.New("invalid wallet debit")
	}
	if err := common.ValidateWalletQuota(amount); err != nil {
		return err
	}
	return s.wallets.Debit(tx, id, amount)
}

func (s *Service) RecordSubscriptionReceipt(tx *gorm.DB, receipt contract.SubscriptionReceipt) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	return repo.RecordSubscriptionReceipt(tx, receipt)
}

func (s *Service) CreditWalletInTx(tx *gorm.DB, id, amount int) error {
	if tx == nil || id <= 0 {
		return errors.New("invalid wallet credit")
	}
	return s.wallets.Credit(tx, id, amount)
}
