package repo

import (
	"context"

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
