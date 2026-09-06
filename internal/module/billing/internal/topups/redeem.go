package topups

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) Redeem(ctx context.Context, key string, userID int) (int, error) {
	if key == "" || userID <= 0 {
		return 0, contract.ErrRedeemFailed
	}
	var row entity.Redemption
	err := s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(`"key" = ?`, key).First(&row).Error; err != nil {
			return err
		}
		if row.Status != common.RedemptionCodeStatusEnabled || (row.ExpiredTime != 0 && row.ExpiredTime < common.GetTimestamp()) {
			return errors.New("redemption is unavailable")
		}
		result := tx.Model(&row).Where("status = ?", common.RedemptionCodeStatusEnabled).Updates(map[string]any{"redeemed_time": common.GetTimestamp(), "status": common.RedemptionCodeStatusUsed, "used_user_id": userID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("redemption is unavailable")
		}
		return s.wallets.Credit(tx, userID, row.Quota)
	})
	if err != nil {
		common.SysError("redemption failed: " + err.Error())
		return 0, contract.ErrRedeemFailed
	}
	if s.deps.AfterCredit != nil {
		if err := s.deps.AfterCredit(userID, row.Quota); err != nil {
			common.SysError("failed to sync redemption credit: " + err.Error())
		}
	}
	if s.deps.Log != nil {
		s.deps.Log(context.WithoutCancel(ctx), contract.TopUpEvent{UserID: userID, Content: fmt.Sprintf("通过兑换码充值 %s，兑换码ID %d", logger.LogQuota(row.Quota), row.Id)})
	}
	return row.Quota, nil
}
