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
	err := s.db.WithContext(ctx).Order(`"key"`).Find(&values).Error
	return values, err
}

func (s *store) put(ctx context.Context, values []entity.Option) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The same transaction lock serializes committed writes and publishers.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('new-api:options:' || current_schema(), 0))").Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&values).Error
	})
}

func (s *store) publishSnapshot(ctx context.Context, publish func([]entity.Option) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('new-api:options:' || current_schema(), 0))").Error; err != nil {
			return err
		}
		var values []entity.Option
		if err := tx.Order(`"key"`).Find(&values).Error; err != nil {
			return err
		}
		return publish(values)
	})
}
