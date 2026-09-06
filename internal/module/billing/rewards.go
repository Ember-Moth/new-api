package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/shopspring/decimal"
)

func (s *Service) GetCheckinStatus(ctx context.Context, userID int, month string) (contract.CheckinStatus, error) {
	cfg := s.rewardConfig()
	if !cfg.CheckinEnabled {
		return contract.CheckinStatus{}, errors.New("签到功能未启用")
	}
	if userID <= 0 {
		return contract.CheckinStatus{}, errors.New("用户不存在")
	}
	now := s.now()
	if month == "" {
		month = now.Format("2006-01")
	}
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return contract.CheckinStatus{}, errors.New("月份格式错误")
	}
	stats, err := s.checkins.Stats(ctx, userID, start.Format("2006-01-02"), start.AddDate(0, 1, 0).Format("2006-01-02"), now.Format("2006-01-02"))
	if err != nil {
		return contract.CheckinStatus{}, err
	}
	return contract.CheckinStatus{Enabled: true, MinQuota: cfg.MinQuota, MaxQuota: cfg.MaxQuota, Stats: stats}, nil
}
func (s *Service) Checkin(ctx context.Context, userID int) (contract.CheckinRecord, error) {
	cfg := s.rewardConfig()
	if !cfg.CheckinEnabled {
		return contract.CheckinRecord{}, errors.New("签到功能未启用")
	}
	if userID <= 0 {
		return contract.CheckinRecord{}, errors.New("用户不存在")
	}
	if cfg.MinQuota < 0 || cfg.MaxQuota < cfg.MinQuota || cfg.MaxQuota > common.MaxWalletQuota {
		return contract.CheckinRecord{}, errors.New("签到额度配置错误")
	}
	amount := cfg.MinQuota
	if cfg.MaxQuota > cfg.MinQuota {
		amount += rand.IntN(cfg.MaxQuota - cfg.MinQuota + 1)
	}
	now := s.now()
	row := entity.Checkin{UserId: userID, CheckinDate: now.Format("2006-01-02"), QuotaAwarded: amount, CreatedAt: now.Unix()}
	if err := s.checkins.Award(ctx, &row); err != nil {
		return contract.CheckinRecord{}, err
	}
	publishCtx := context.WithoutCancel(ctx)
	if amount > 0 {
		if err := s.accounting.PublishUserDelta(publishCtx, userID, int64(amount)); err != nil {
			common.SysError("publish checkin quota: " + err.Error())
		}
	}
	if s.rewardLog != nil {
		s.rewardLog(publishCtx, userID, fmt.Sprintf("用户签到，获得额度 %s", logger.LogQuota(amount)))
	}
	return contract.CheckinRecord{CheckinDate: row.CheckinDate, QuotaAwarded: amount}, nil
}
func (s *Service) TransferAffiliate(ctx context.Context, userID, amount int) error {
	if err := s.RequirePaymentCompliance(); err != nil {
		return err
	}
	if amount <= 0 || amount > common.MaxWalletQuota {
		return contract.ErrInvalidTopUpQuota
	}
	unit := s.rewardConfig().QuotaPerUnit
	if unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return errors.New("额度单位配置错误")
	}
	minimum, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromFloat(unit).Ceil())
	if err != nil {
		return errors.New("额度单位配置错误")
	}
	if amount < minimum {
		return fmt.Errorf("转移额度最小为%s！", logger.LogQuota(minimum))
	}
	if err := s.wallets.TransferAffiliate(ctx, userID, amount); err != nil {
		return err
	}
	if err := s.accounting.PublishUserDelta(context.WithoutCancel(ctx), userID, int64(amount)); err != nil {
		common.SysError("publish affiliate transfer quota: " + err.Error())
	}
	return nil
}
