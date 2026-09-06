package repo

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Wallets struct{ db *gorm.DB }

func NewWallets(db *gorm.DB) *Wallets { return &Wallets{db: db} }
func (r *Wallets) Replace(ctx context.Context, id, amount int) (int, error) {
	var previous int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var balance struct {
			Id    int
			Quota int
		}
		if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "quota").Where("id = ? AND deleted_at IS NULL", id).Take(&balance).Error; err != nil {
			return err
		}
		previous = balance.Quota
		return tx.Table("users").Where("id = ? AND deleted_at IS NULL", id).Update("quota", amount).Error
	})
	return previous, err
}

func (r *Wallets) Debit(tx *gorm.DB, id, amount int) error {
	var balance struct {
		Id    int
		Quota int
	}
	if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "quota").Where("id = ? AND deleted_at IS NULL", id).Take(&balance).Error; err != nil {
		return err
	}
	if amount > 0 && balance.Quota < amount {
		return contract.ErrInsufficientBalance
	}
	if amount == 0 {
		return nil
	}
	return tx.Table("users").Where("id = ? AND deleted_at IS NULL", id).Update("quota", gorm.Expr("quota - ?", amount)).Error
}

func (r *Wallets) ValidateCredit(ctx context.Context, id, amount int) error {
	if amount <= 0 || amount > common.MaxWalletQuota {
		return contract.ErrInvalidTopUpQuota
	}
	var row struct{ Quota int }
	if err := r.db.WithContext(ctx).Table("users").Select("quota").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return err
	}
	if row.Quota > common.MaxWalletQuota-amount {
		return contract.ErrTopUpQuotaLimitExceeded
	}
	return nil
}

func (r *Wallets) Credit(tx *gorm.DB, id, amount int) error {
	if amount <= 0 || amount > common.MaxWalletQuota {
		return contract.ErrInvalidTopUpQuota
	}
	result := tx.Table("users").Where("id = ? AND deleted_at IS NULL AND quota <= ?", id, common.MaxWalletQuota-amount).Update("quota", gorm.Expr("quota + ?", amount))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := tx.Table("users").Where("id = ? AND deleted_at IS NULL", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return contract.ErrTopUpQuotaLimitExceeded
}

// TransferAffiliate changes only the two balances while holding the user row.
func (r *Wallets) TransferAffiliate(ctx context.Context, id, amount int) error {
	if amount <= 0 || amount > common.MaxWalletQuota {
		return contract.ErrInvalidTopUpQuota
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct{ Quota, AffQuota int }
		if err := tx.Table("users").Select("quota", "aff_quota").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		if row.AffQuota < amount {
			return errors.New("邀请额度不足！")
		}
		if row.Quota > common.MaxWalletQuota-amount {
			return contract.ErrTopUpQuotaLimitExceeded
		}
		result := tx.Table("users").Where("id = ? AND deleted_at IS NULL", id).Updates(map[string]any{"aff_quota": gorm.Expr("aff_quota - ?", amount), "quota": gorm.Expr("quota + ?", amount)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
