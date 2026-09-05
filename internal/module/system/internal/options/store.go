package options

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type store struct{ db *gorm.DB }

func (s *store) all(ctx context.Context) ([]entity.Option, error) {
	var values []entity.Option
	err := s.db.WithContext(ctx).Find(&values).Error
	return values, err
}

func (s *store) put(ctx context.Context, values []entity.Option) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&values).Error
	})
}
