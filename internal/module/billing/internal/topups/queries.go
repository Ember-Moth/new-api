package topups

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	dbquery "github.com/QuantumNous/new-api/internal/infra/database/query"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"gorm.io/gorm"
)

func (s *Store) List(ctx context.Context, input contract.TopUpQuery) (rows []*entity.TopUp, total int64, err error) {
	if !input.Admin && input.UserID <= 0 {
		return nil, 0, errors.New("invalid user id")
	}
	if input.Offset < 0 {
		input.Offset = 0
	}
	if input.Limit <= 0 {
		input.Limit = 20
	}
	input.Limit = min(input.Limit, 100)
	err = s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&entity.TopUp{})
		if !input.Admin {
			query = query.Where("user_id = ? AND create_time >= ?", input.UserID, common.GetTimestamp()-30*24*3600)
		}
		if input.Keyword != "" {
			pattern, err := dbquery.SanitizeLikePattern(input.Keyword)
			if err != nil {
				return err
			}
			query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
			if err := tx.Table("(?) AS matched_topups", query.Select("id").Limit(10000)).Count(&total).Error; err != nil {
				return err
			}
		} else {
			if err := query.Count(&total).Error; err != nil {
				return err
			}
		}
		return query.Select("*").Order("id desc").Limit(input.Limit).Offset(input.Offset).Find(&rows).Error
	})
	return rows, total, err
}
