package payments

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func (s *Store) PurchaseWithBalance(ctx context.Context, userID, planID int) error {
	if userID <= 0 || planID <= 0 {
		return errors.New("invalid userId or planId")
	}
	var title string
	var money float64
	var charged int
	var upgraded bool
	err := s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		plan, err := s.deps.Catalog.Plan(ctx, tx, planID)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		if plan.PriceAmount < 0 || math.IsNaN(plan.PriceAmount) || math.IsInf(plan.PriceAmount, 0) {
			return errors.New("套餐价格必须是有限的非负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}
		var required int
		if plan.PriceAmount > 0 {
			unit := s.deps.QuotaPerUnit()
			if unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
				return errors.New("额度单位配置错误")
			}
			required, err = common.WalletQuotaFromDecimalStrict(decimal.NewFromFloat(plan.PriceAmount).Mul(decimal.NewFromFloat(unit)).Ceil())
			if err != nil {
				return err
			}
		}
		if err := s.deps.Billing.DebitWalletInTx(tx, userID, required); err != nil {
			return err
		}
		sub, err := s.deps.Members.CreateUserSubscriptionFromPlanTx(tx, userID, plan, "balance")
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		order := entity.SubscriptionOrder{UserId: userID, PlanId: plan.Id, Money: plan.PriceAmount, TradeNo: fmt.Sprintf("SUBBALUSR%dNO%s%d", userID, common.GetRandomString(6), time.Now().UnixNano()), PaymentMethod: "balance", PaymentProvider: "balance", Status: common.TopUpStatusSuccess, CreateTime: now, CompleteTime: now, ProviderPayload: fmt.Sprintf("charged_quota=%d", required)}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		title, money, charged, upgraded = plan.Title, plan.PriceAmount, required, sub.PrevUserGroup != ""
		return nil
	})
	if err != nil {
		return err
	}
	if charged > 0 && s.deps.AfterDebit != nil {
		if err := s.deps.AfterDebit(userID, charged); err != nil {
			common.SysError("failed to decrease user quota cache after subscription balance purchase: " + err.Error())
		}
	}
	if upgraded {
		s.deps.Members.RefreshUserGroupCache(userID, "subscription balance purchase")
	}
	if s.deps.Log != nil {
		s.deps.Log(context.WithoutCancel(ctx), userID, fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", title, money, charged))
	}
	return nil
}
