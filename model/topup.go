package model

import (
	"context"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	billingentity "github.com/QuantumNous/new-api/internal/module/billing/entity"
)

type TopUp billingentity.TopUp

const (
	PaymentMethodStripe         = billingcontract.PaymentMethodStripe
	PaymentMethodCreem          = billingcontract.PaymentMethodCreem
	PaymentMethodWaffo          = billingcontract.PaymentMethodWaffo
	PaymentMethodWaffoPancake   = billingcontract.PaymentMethodWaffoPancake
	PaymentMethodBalance        = billingcontract.PaymentMethodBalance
	PaymentProviderEpay         = billingcontract.PaymentProviderEpay
	PaymentProviderStripe       = billingcontract.PaymentProviderStripe
	PaymentProviderCreem        = billingcontract.PaymentProviderCreem
	PaymentProviderWaffo        = billingcontract.PaymentProviderWaffo
	PaymentProviderWaffoPancake = billingcontract.PaymentProviderWaffoPancake
	PaymentProviderBalance      = billingcontract.PaymentProviderBalance
)

var (
	ErrTopUpNotFound            = billingcontract.ErrTopUpNotFound
	ErrTopUpStatusInvalid       = billingcontract.ErrTopUpStatusInvalid
	ErrInvalidTopUpQuota        = billingcontract.ErrInvalidTopUpQuota
	ErrTopUpQuotaLimitExceeded  = billingcontract.ErrTopUpQuotaLimitExceeded
	ErrWalletQuotaLimitExceeded = billingcontract.ErrWalletQuotaLimitExceeded
	ErrPaymentMethodMismatch    = billingcontract.ErrPaymentMethodMismatch
)

func (topUp *TopUp) Insert() error {
	return TopUpStore().Create(context.Background(), (*billingentity.TopUp)(topUp))
}
func GetTopUpByTradeNo(tradeNo string) *TopUp {
	row, err := TopUpStore().Get(context.Background(), tradeNo)
	if err != nil {
		return nil
	}
	return (*TopUp)(row)
}
func ValidateTopUpQuotaCapacity(id, quota int) error {
	return TopUpStore().ValidateCapacity(context.Background(), id, quota)
}
func UpdatePendingTopUpStatus(tradeNo, provider, status string) error {
	return TopUpStore().FinishPending(context.Background(), tradeNo, provider, status)
}
func RechargeEpay(tradeNo, method, ip string) (bool, error) {
	return TopUpStore().Complete(context.Background(), billingcontract.TopUpCompletion{TradeNo: tradeNo, Provider: PaymentProviderEpay, ActualMethod: method, CallerIP: ip})
}
