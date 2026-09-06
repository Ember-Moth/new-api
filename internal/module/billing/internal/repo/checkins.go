package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Checkins struct {
	db      *gorm.DB
	wallets *Wallets
}

func NewCheckins(db *gorm.DB) *Checkins { return &Checkins{db: db, wallets: NewWallets(db)} }
func (r *Checkins) Award(ctx context.Context, row *entity.Checkin) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user struct{ Id int }
		if err := tx.Table("users").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", row.UserId).Take(&user).Error; err != nil {
			return err
		}
		insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "checkin_date"}}, DoNothing: true}).Create(row)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			return errors.New("今日已签到")
		}
		if row.QuotaAwarded == 0 {
			return nil
		}
		return r.wallets.Credit(tx, row.UserId, row.QuotaAwarded)
	})
}
func (r *Checkins) Stats(ctx context.Context, userID int, start, end, today string) (contract.CheckinStats, error) {
	result := contract.CheckinStats{Records: []contract.CheckinRecord{}}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var totals struct {
			TotalQuota     string
			TotalCheckins  int64
			CheckedInToday bool
		}
		if err := tx.Model(&entity.Checkin{}).Where("user_id = ?", userID).Select("COUNT(*) AS total_checkins, COALESCE(SUM(quota_awarded), 0)::text AS total_quota, COALESCE(BOOL_OR(checkin_date = ?), false) AS checked_in_today", today).Scan(&totals).Error; err != nil {
			return err
		}
		result.TotalQuota = json.Number(totals.TotalQuota)
		result.TotalCheckins = totals.TotalCheckins
		result.CheckedInToday = totals.CheckedInToday
		if err := tx.Model(&entity.Checkin{}).Select("checkin_date", "quota_awarded").Where("user_id = ? AND checkin_date >= ? AND checkin_date < ?", userID, start, end).Order("checkin_date DESC").Find(&result.Records).Error; err != nil {
			return err
		}
		result.CheckinCount = len(result.Records)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
