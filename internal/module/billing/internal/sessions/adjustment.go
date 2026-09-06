package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// billingAdjustmentReceipt is an immutable receipt for the non-session
// accounting path. Funding, token, statistics, and this row are committed in
// one transaction, so an uncertain commit can be retried by OperationID.
type billingAdjustmentReceipt struct {
	OperationID string `gorm:"column:operation_id;primaryKey;type:varchar(128)"`
	UserID      int    `gorm:"column:user_id;not null"`
	TokenID     int    `gorm:"column:token_id;not null"`
	Source      string `gorm:"column:source;not null;type:varchar(32)"`

	SubscriptionID int `gorm:"column:subscription_id;not null"`
	Delta          int `gorm:"column:delta;not null"`
	UsageDelta     int `gorm:"column:usage_delta;not null"`
	RequestDelta   int `gorm:"column:request_delta;not null"`
	ChannelID      int `gorm:"column:channel_id;not null"`

	TokenUnlimited  bool  `gorm:"column:token_unlimited;not null"`
	Playground      bool  `gorm:"column:playground;not null"`
	HistoricalToken bool  `gorm:"column:historical_token;not null"`
	AppliedAt       int64 `gorm:"column:applied_at;not null"`
}

func (billingAdjustmentReceipt) TableName() string { return "billing_adjustment_receipts" }

// ApplyAdjustment commits one non-session funding/token/statistics operation.
// OperationID is caller-owned business identity. Exact retries return the
// original result; any identity or parameter mismatch is rejected.
func (e *Engine) ApplyAdjustment(ctx context.Context, input contract.BillingRequest, adjustment contract.BillingAdjustment) (result contract.QuotaAdjustment, err error) {
	err = e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		var txErr error
		result, txErr = e.ApplyAdjustmentTx(ctx, tx, input, adjustment)
		return txErr
	})
	if err != nil {
		return contract.QuotaAdjustment{}, err
	}
	e.PublishCommitted(input)
	return result, nil
}

// ApplyAdjustmentTx is the transaction-bound form used by task and gateway
// compositions that already own a larger PostgreSQL transaction. It never
// commits, opens a nested transaction, or publishes cache projections.
func (e *Engine) ApplyAdjustmentTx(ctx context.Context, tx *gorm.DB, input contract.BillingRequest, adjustment contract.BillingAdjustment) (result contract.QuotaAdjustment, err error) {
	if tx == nil {
		return result, errors.New("billing transaction is nil")
	}
	if err := validateAdjustment(input, adjustment); err != nil {
		return result, err
	}
	receipt := billingAdjustmentReceipt{
		OperationID:     adjustment.OperationID,
		UserID:          input.UserID,
		TokenID:         input.TokenID,
		Source:          adjustment.Source,
		SubscriptionID:  adjustment.SubscriptionID,
		Delta:           adjustment.Delta,
		UsageDelta:      adjustment.UsageDelta,
		RequestDelta:    adjustment.RequestDelta,
		ChannelID:       adjustment.ChannelID,
		TokenUnlimited:  input.TokenUnlimited,
		Playground:      input.Playground,
		HistoricalToken: adjustment.UseHistoricalToken,
		AppliedAt:       common.GetTimestamp(),
	}
	insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "operation_id"}}, DoNothing: true}).Create(&receipt)
	if insert.Error != nil {
		return result, insert.Error
	}
	if insert.RowsAffected == 0 {
		var existing billingAdjustmentReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", adjustment.OperationID).First(&existing).Error; err != nil {
			return result, err
		}
		if !sameAdjustment(existing, receipt) {
			return result, operationConflict("operation parameters conflict with the durable receipt")
		}
		result = adjustmentResult(adjustment, input.Playground, true)
		return result, nil
	}

	accountingTx := e.deps.Accounting.WithHistoricalTx(tx)
	// User statistics are part of either funding source's commit. Lock the
	// user before subscription/token rows to keep one order across all paths.
	if _, err := accountingTx.UserQuotaTx(ctx, input.UserID); err != nil {
		return result, e.fundingFailure(err)
	}
	switch adjustment.Source {
	case contract.BillingSourceSubscription:
		if e.deps.Subscriptions == nil {
			return result, failure(contract.BillingStorageFailure, errors.New("subscription quota store is unavailable"))
		}
		if _, err := e.deps.Subscriptions.WithTx(tx).PostConsumeUserSubscriptionDeltaForUserTx(ctx, tx, input.UserID, adjustment.SubscriptionID, int64(adjustment.Delta)); err != nil {
			return result, e.fundingFailure(err)
		}
	case contract.BillingSourceWallet:
		if adjustment.Delta > 0 {
			if err := accountingTx.DecreaseUserQuota(ctx, input.UserID, adjustment.Delta, false); err != nil {
				return result, e.fundingFailure(err)
			}
		} else if adjustment.Delta < 0 {
			if err := accountingTx.IncreaseUserQuota(ctx, input.UserID, -adjustment.Delta, false); err != nil {
				return result, e.fundingFailure(err)
			}
		}
	}
	if !input.Playground {
		authoritativeUnlimited, err := e.deps.Accounting.WithTx(tx).ValidateHistoricalTokenIdentity(ctx, input.UserID, input.TokenID)
		if err != nil {
			return result, operationConflict(err.Error())
		}
		if !adjustment.UseHistoricalToken && authoritativeUnlimited != input.TokenUnlimited {
			return result, operationConflict("token unlimited-quota authorization changed")
		}
	}

	if !input.Playground {
		if adjustment.Delta > 0 {
			if err := accountingTx.DecreaseTokenQuota(ctx, input.TokenID, input.TokenKey, adjustment.Delta); err != nil {
				return result, failure(contract.BillingStorageFailure, err)
			}
		} else if adjustment.Delta < 0 {
			if err := accountingTx.IncreaseTokenQuota(ctx, input.TokenID, input.TokenKey, -adjustment.Delta); err != nil {
				return result, failure(contract.BillingStorageFailure, err)
			}
		}
	}
	if adjustment.UsageDelta != 0 || adjustment.RequestDelta != 0 {
		if err := accountingTx.RecordUsageTx(ctx, tx, input.UserID, adjustment.UsageDelta, adjustment.RequestDelta); err != nil {
			return result, err
		}
	}
	if adjustment.UsageDelta != 0 {
		if err := accountingTx.RecordChannelUsageTx(ctx, tx, adjustment.ChannelID, adjustment.UsageDelta); err != nil {
			return result, err
		}
	}
	result = adjustmentResult(adjustment, input.Playground, false)
	return result, nil
}

func validateAdjustment(input contract.BillingRequest, adjustment contract.BillingAdjustment) error {
	if strings.TrimSpace(adjustment.OperationID) == "" || len(adjustment.OperationID) > 128 {
		return failure(contract.BillingInvalidRequest, errors.New("billing operation id is required"))
	}
	if input.UserID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is required"))
	}
	if input.TokenID < 0 {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is invalid"))
	}
	if !input.Playground && input.TokenID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is required"))
	}
	if adjustment.Source != contract.BillingSourceWallet && adjustment.Source != contract.BillingSourceSubscription {
		return failure(contract.BillingInvalidRequest, errors.New("invalid billing adjustment source"))
	}
	if adjustment.Source == contract.BillingSourceSubscription && adjustment.SubscriptionID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("subscription id is required"))
	}
	if adjustment.Delta < -common.MaxQuota || adjustment.Delta > common.MaxQuota || adjustment.UsageDelta < -common.MaxQuota || adjustment.UsageDelta > common.MaxQuota || adjustment.RequestDelta < -common.MaxQuota || adjustment.RequestDelta > common.MaxQuota {
		return failure(contract.BillingInvalidQuota, fmt.Errorf("billing adjustment is out of range: delta=%d usage=%d requests=%d", adjustment.Delta, adjustment.UsageDelta, adjustment.RequestDelta))
	}
	if adjustment.UsageDelta != 0 && adjustment.ChannelID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("channel id is required for channel usage adjustment"))
	}
	return nil
}

func sameAdjustment(a, b billingAdjustmentReceipt) bool {
	return a.OperationID == b.OperationID && a.UserID == b.UserID && a.TokenID == b.TokenID && a.Source == b.Source &&
		a.SubscriptionID == b.SubscriptionID && a.Delta == b.Delta && a.UsageDelta == b.UsageDelta && a.RequestDelta == b.RequestDelta &&
		a.ChannelID == b.ChannelID && a.TokenUnlimited == b.TokenUnlimited && a.Playground == b.Playground && a.HistoricalToken == b.HistoricalToken
}

func adjustmentResult(adjustment contract.BillingAdjustment, playground, replayed bool) contract.QuotaAdjustment {
	return contract.QuotaAdjustment{
		FundingApplied:        true,
		TokenApplied:          !playground,
		Replayed:              replayed,
		SubscriptionPostDelta: adjustmentSubscriptionDelta(adjustment),
	}
}

func adjustmentSubscriptionDelta(adjustment contract.BillingAdjustment) int64 {
	if adjustment.Source != contract.BillingSourceSubscription {
		return 0
	}
	return int64(adjustment.Delta)
}

func operationConflict(message string) error {
	return &contract.BillingFailure{Kind: contract.BillingOperationConflict, Cause: fmt.Errorf("%w: %s", contract.ErrBillingOperationConflict, message)}
}
