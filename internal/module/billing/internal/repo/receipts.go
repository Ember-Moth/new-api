package repo

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordSubscriptionReceipt records payment without crediting the wallet.
func RecordSubscriptionReceipt(tx *gorm.DB, receipt contract.SubscriptionReceipt) error {
	if receipt.UserID <= 0 || receipt.TradeNo == "" || receipt.Money < 0 || math.IsNaN(receipt.Money) || math.IsInf(receipt.Money, 0) {
		return errors.New("invalid subscription receipt")
	}
	var topup entity.TopUp
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", receipt.TradeNo).First(&topup).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&entity.TopUp{UserId: receipt.UserID, Amount: 0, Money: receipt.Money, TradeNo: receipt.TradeNo, PaymentMethod: receipt.PaymentMethod, PaymentProvider: receipt.PaymentProvider, CreateTime: receipt.CreatedAt, CompleteTime: common.GetTimestamp(), Status: common.TopUpStatusSuccess}).Error
	}
	if err != nil {
		return err
	}
	if topup.UserId != receipt.UserID || topup.Amount != 0 || topup.PaymentProvider != receipt.PaymentProvider {
		return contract.ErrPaymentMethodMismatch
	}
	topup.Money = receipt.Money
	topup.PaymentMethod = receipt.PaymentMethod
	if topup.CreateTime == 0 {
		topup.CreateTime = receipt.CreatedAt
	}
	topup.CompleteTime = common.GetTimestamp()
	topup.Status = common.TopUpStatusSuccess
	return tx.Save(&topup).Error
}
