package topups

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/logger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Complete keeps provider-unit conversion, order completion and wallet credit
// in one transaction. alreadyDone means no cache or log effects were repeated.
func (s *Store) Complete(ctx context.Context, input contract.TopUpCompletion) (alreadyDone bool, err error) {
	if input.TradeNo == "" {
		return false, errors.New("未提供支付单号")
	}
	var row entity.TopUp
	var amount int
	var customerChanged bool
	err = s.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", input.TradeNo).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrTopUpNotFound
		}
		if err != nil {
			return err
		}
		if !input.Manual && (input.Provider == "" || row.PaymentProvider != input.Provider) {
			return contract.ErrPaymentMethodMismatch
		}
		if row.Status == common.TopUpStatusSuccess {
			alreadyDone = true
			return nil
		}
		if row.Status != common.TopUpStatusPending {
			return contract.ErrTopUpStatusInvalid
		}
		if row.Amount <= 0 || row.Money < 0 || math.IsNaN(row.Money) || math.IsInf(row.Money, 0) {
			return contract.ErrInvalidTopUpQuota
		}
		var quota decimal.Decimal
		switch row.PaymentProvider {
		case contract.PaymentProviderCreem:
			quota = decimal.NewFromInt(row.Amount)
		case contract.PaymentProviderStripe, contract.PaymentProviderEpay, contract.PaymentProviderWaffo, contract.PaymentProviderWaffoPancake:
			unit := s.deps.QuotaPerUnit()
			if unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
				return contract.ErrInvalidTopUpQuota
			}
			base := decimal.NewFromInt(row.Amount)
			if row.PaymentProvider == contract.PaymentProviderStripe {
				base = decimal.NewFromFloat(row.Money)
			}
			quota = base.Mul(decimal.NewFromFloat(unit))
		default:
			return contract.ErrPaymentMethodMismatch
		}
		amount, err = common.WalletQuotaFromDecimalStrict(quota)
		if err != nil || amount <= 0 {
			return contract.ErrInvalidTopUpQuota
		}
		if err := s.wallets.Credit(tx, row.UserId, amount); err != nil {
			return err
		}
		if !input.Manual && s.deps.Customer != nil && (row.PaymentProvider == contract.PaymentProviderStripe || row.PaymentProvider == contract.PaymentProviderCreem) {
			stripeID, email := input.StripeCustomerID, input.CustomerEmail
			if row.PaymentProvider != contract.PaymentProviderStripe {
				stripeID = nil
			}
			if row.PaymentProvider != contract.PaymentProviderCreem {
				email = ""
			}
			customerChanged, err = s.deps.Customer(tx, row.UserId, stripeID, email)
			if err != nil {
				return err
			}
		}
		if input.ActualMethod != "" {
			row.PaymentMethod = input.ActualMethod
		}
		row.CompleteTime = common.GetTimestamp()
		row.Status = common.TopUpStatusSuccess
		return tx.Save(&row).Error
	})
	if err != nil {
		return false, err
	}
	if alreadyDone {
		return true, nil
	}
	if s.deps.AfterCredit != nil {
		if err := s.deps.AfterCredit(row.UserId, amount); err != nil {
			common.SysError("failed to sync topup credit to user quota cache: " + err.Error())
		}
	}
	if customerChanged && s.deps.PublishCustomer != nil {
		if err := s.deps.PublishCustomer(row.UserId); err != nil {
			common.SysError("failed to publish payment customer: " + err.Error())
		}
	}
	provider := row.PaymentProvider
	var content string
	switch provider {
	case contract.PaymentProviderStripe:
		content = fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%d", logger.FormatQuota(amount), row.Amount)
	case contract.PaymentProviderCreem:
		content = fmt.Sprintf("使用Creem充值成功，充值额度: %v，支付金额：%.2f", amount, row.Money)
	case contract.PaymentProviderWaffo:
		content = fmt.Sprintf("Waffo充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(amount), row.Money)
	case contract.PaymentProviderWaffoPancake:
		content = fmt.Sprintf("Waffo Pancake充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(amount), row.Money)
	default:
		content = fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f", logger.LogQuota(amount), row.Money)
	}
	if input.Manual {
		provider = "admin"
		content = fmt.Sprintf("管理员补单成功，充值金额: %v，支付金额：%f", logger.FormatQuota(amount), row.Money)
	}
	if s.deps.Log != nil {
		s.deps.Log(context.WithoutCancel(ctx), contract.TopUpEvent{UserID: row.UserId, Content: content, CallerIP: input.CallerIP, Method: row.PaymentMethod, Provider: provider})
	}
	return false, nil
}
