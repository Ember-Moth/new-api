package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/internal/shared/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) Complete(ctx context.Context, tradeNo, payload, expectedProvider, actualMethod string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	var completed *entity.SubscriptionOrder
	var title string
	var upgraded bool
	err := s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order entity.SubscriptionOrder
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", tradeNo).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if expectedProvider != "" && order.PaymentProvider != expectedProvider {
			return billingcontract.ErrPaymentMethodMismatch
		}
		if order.Status == common.TopUpStatusSuccess {
			return nil
		}
		if order.Status != common.TopUpStatusPending {
			return ErrOrderStatusInvalid
		}
		plan, err := s.deps.Catalog.Plan(ctx, tx, order.PlanId)
		if err != nil {
			return err
		}
		// Existing paid orders can complete after their plan is disabled.
		sub, err := s.deps.Members.CreateUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order")
		if err != nil {
			return err
		}
		if actualMethod != "" {
			order.PaymentMethod = actualMethod
		}
		if err := s.deps.Billing.RecordSubscriptionReceipt(tx, billingcontract.SubscriptionReceipt{UserID: order.UserId, Money: order.Money, TradeNo: order.TradeNo, PaymentMethod: order.PaymentMethod, PaymentProvider: order.PaymentProvider, CreatedAt: order.CreateTime}); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.CompleteTime = common.GetTimestamp()
		if payload != "" {
			order.ProviderPayload = payload
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		completed = &order
		title = plan.Title
		upgraded = sub.PrevUserGroup != ""
		return nil
	})
	if err != nil {
		return err
	}
	if completed == nil {
		return nil
	}
	if upgraded {
		s.deps.Members.RefreshUserGroupCache(completed.UserId, "subscription payment completion")
	}
	if s.deps.Log != nil {
		s.deps.Log(context.WithoutCancel(ctx), completed.UserId, fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", title, completed.Money, completed.PaymentMethod))
	}
	return nil
}

// FinishPending cannot replace a successful callback with a stale checkout failure.
func (s *Store) FinishPending(ctx context.Context, tradeNo, expectedProvider, status string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	if status != common.TopUpStatusExpired && status != common.TopUpStatusFailed {
		return ErrOrderStatusInvalid
	}
	return s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order entity.SubscriptionOrder
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", tradeNo).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if expectedProvider != "" && order.PaymentProvider != expectedProvider {
			return billingcontract.ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusPending {
			return nil
		}
		return tx.Model(&order).Updates(map[string]any{"status": status, "complete_time": common.GetTimestamp()}).Error
	})
}
