package repo

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Redemptions struct{ db *gorm.DB }

func NewRedemptions(db *gorm.DB) *Redemptions { return &Redemptions{db: db} }

func (r *Redemptions) List(ctx context.Context, keyword, status string, offset, limit int) (rows []*entity.Redemption, total int64, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&entity.Redemption{})
		if keyword != "" {
			if id, err := strconv.Atoi(keyword); err == nil {
				query = query.Where("id = ? OR name LIKE ?", id, keyword+"%")
			} else {
				query = query.Where("name LIKE ?", keyword+"%")
			}
		}

		if status != "" {
			now := common.GetTimestamp()
			switch status {
			case "expired":
				query = query.Where(
					"status = ? AND expired_time != 0 AND expired_time < ?",
					common.RedemptionCodeStatusEnabled,
					now,
				)
			case strconv.Itoa(common.RedemptionCodeStatusEnabled):
				query = query.Where(
					"status = ? AND (expired_time = 0 OR expired_time >= ?)",
					common.RedemptionCodeStatusEnabled,
					now,
				)
			case strconv.Itoa(common.RedemptionCodeStatusDisabled):
				query = query.Where("status = ?", common.RedemptionCodeStatusDisabled)
			case strconv.Itoa(common.RedemptionCodeStatusUsed):
				query = query.Where("status = ?", common.RedemptionCodeStatusUsed)
			}
		}

		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return query.Order("id desc").Limit(limit).Offset(offset).Find(&rows).Error
	})
	return rows, total, err
}

func (r *Redemptions) Get(ctx context.Context, id int) (*entity.Redemption, error) {
	row := &entity.Redemption{}
	err := r.db.WithContext(ctx).First(row, "id = ?", id).Error
	return row, err
}

func (r *Redemptions) Create(ctx context.Context, redemption *entity.Redemption) error {
	return r.db.WithContext(ctx).Create(redemption).Error
}

func (r *Redemptions) Update(ctx context.Context, redemption *entity.Redemption, statusOnly bool) error {
	changes := map[string]any{"name": redemption.Name, "quota": redemption.Quota, "expired_time": redemption.ExpiredTime}
	if statusOnly {
		changes = map[string]any{"status": redemption.Status}
	}
	// Each operation owns only these fields. A configuration edit must not restore
	// a status read before a concurrent wallet redemption completed.
	result := r.db.WithContext(ctx).Model(redemption).Clauses(clause.Returning{}).Updates(changes)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Redemptions) Delete(ctx context.Context, id int) error {
	result := r.db.WithContext(ctx).Delete(&entity.Redemption{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Redemptions) DeleteInvalid(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)", []int{common.RedemptionCodeStatusUsed, common.RedemptionCodeStatusDisabled}, common.RedemptionCodeStatusEnabled, common.GetTimestamp()).Delete(&entity.Redemption{})
	return result.RowsAffected, result.Error
}
