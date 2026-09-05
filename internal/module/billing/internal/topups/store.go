package topups

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/module/billing/internal/repo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Dependencies struct {
	DB              *gorm.DB
	QuotaPerUnit    func() float64
	Customer        func(*gorm.DB, int, *string, string) (bool, error)
	PublishCustomer func(int) error
	AfterCredit     func(int, int) error
	Log             func(context.Context, contract.TopUpEvent)
}

type Store struct {
	deps    Dependencies
	wallets *repo.Wallets
}

func New(deps Dependencies) *Store { return &Store{deps: deps, wallets: repo.NewWallets(deps.DB)} }

func (s *Store) Create(ctx context.Context, row *entity.TopUp) error {
	if row == nil || row.UserId <= 0 || row.TradeNo == "" || row.Amount <= 0 || row.Money < 0 || math.IsNaN(row.Money) || math.IsInf(row.Money, 0) || row.Status != common.TopUpStatusPending {
		return contract.ErrInvalidTopUpQuota
	}
	if row.CreateTime == 0 {
		row.CreateTime = common.GetTimestamp()
	}
	return s.deps.DB.WithContext(ctx).Create(row).Error
}

func (s *Store) Get(ctx context.Context, reference string) (*entity.TopUp, error) {
	var row entity.TopUp
	err := s.deps.DB.WithContext(ctx).Where("trade_no = ?", reference).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, contract.ErrTopUpNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) GetByID(ctx context.Context, id int) (*entity.TopUp, error) {
	var row entity.TopUp
	err := s.deps.DB.WithContext(ctx).First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, contract.ErrTopUpNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) ValidateCapacity(ctx context.Context, id, amount int) error {
	return s.wallets.ValidateCredit(ctx, id, amount)
}

func (s *Store) FinishPending(ctx context.Context, reference, provider, status string) error {
	if reference == "" {
		return errors.New("未提供支付单号")
	}
	if status != common.TopUpStatusExpired && status != common.TopUpStatusFailed {
		return contract.ErrTopUpStatusInvalid
	}
	return s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row entity.TopUp
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", reference).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrTopUpNotFound
		}
		if err != nil {
			return err
		}
		if provider != "" && row.PaymentProvider != provider {
			return contract.ErrPaymentMethodMismatch
		}
		if row.Status != common.TopUpStatusPending {
			return contract.ErrTopUpStatusInvalid
		}
		return tx.Model(&row).Updates(map[string]any{"status": status, "complete_time": common.GetTimestamp()}).Error
	})
}
