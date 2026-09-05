package repo

import (
	"context"

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
