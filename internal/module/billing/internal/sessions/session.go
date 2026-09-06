package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

// Session is a process-local handle for the durable billing session identified
// by input.RequestID. The database row, rather than these fields or the mutex,
// owns lifecycle state, so a new process can safely re-open the handle.
type Session struct {
	mu            sync.Mutex
	engine        *Engine
	input         contract.BillingRequest
	state         contract.BillingState
	refundBlocked bool
}

func (s *Session) Snapshot() contract.BillingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.PreConsumedQuota
}

func (s *Session) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.refundBlocked && s.state.Status == sessionStatusActive && s.state.PreConsumedQuota > 0 && s.state.PendingAction == ""
}

// Reserve grows the durable pre-consume target. Repeating a target that is no
// greater than the recorded target is a no-op; the final settlement owns any
// resulting refund. Funding and token changes, when needed, share one transaction.
func (s *Session) Reserve(ctx context.Context, target int) error {
	if err := validateQuota(target); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var committed bool
	var userChanged, tokenChanged bool
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status != sessionStatusActive {
			return sessionConflict("cannot reserve a terminal billing session")
		}
		if record.PendingAction != "" {
			return sessionConflict("cannot reserve a session with a pending terminal action")
		}
		if target <= record.ReservedQuota {
			nextState = stateFromRecord(record)
			return nil
		}
		if record.Trusted {
			// Trust bypass deliberately keeps the pre-consume at zero. The final
			// settlement remains responsible for the actual charge.
			nextState = stateFromRecord(record)
			return nil
		}
		delta := target - record.ReservedQuota
		accountingTx := s.engine.deps.Accounting.WithHistoricalTx(tx)
		if _, err := accountingTx.UserQuotaTx(ctx, record.UserID); err != nil {
			return s.engine.fundingFailure(err)
		}
		if record.Source == contract.BillingSourceWallet {
			reserved, err := accountingTx.TryReserveUserQuota(ctx, record.UserID, delta)
			if err != nil {
				return s.engine.fundingFailure(err)
			}
			if !reserved {
				return failure(contract.BillingInsufficientFunds, errors.New("用户额度不足"))
			}
			userChanged = delta != 0
		} else {
			if s.engine.deps.Subscriptions == nil {
				return failure(contract.BillingStorageFailure, errors.New("subscription quota store is unavailable"))
			}
			if _, err := s.engine.deps.Subscriptions.WithTx(tx).AdjustSubscriptionPreConsumeTx(ctx, tx, record.RequestID, int64(delta)); err != nil {
				return s.engine.fundingFailure(err)
			}
		}
		if !record.Playground {
			reserved, err := accountingTx.TryReserveTokenQuota(ctx, record.TokenID, s.input.TokenKey, delta, record.TokenUnlimited)
			if err != nil {
				return failure(contract.BillingInsufficientToken, err)
			}
			if !reserved {
				return failure(contract.BillingInsufficientToken, errors.New("token quota is not enough"))
			}
			tokenChanged = delta != 0
		}
		record.ReservedQuota = target
		if record.Source == contract.BillingSourceSubscription {
			if err := s.engine.refreshSubscriptionStateTx(ctx, tx, record); err != nil {
				return err
			}
		}
		if err := tx.Model(record).Updates(map[string]any{"reserved_quota": record.ReservedQuota, "subscription_used": record.SubscriptionUsed, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		nextState = stateFromRecord(record)
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	s.state = nextState
	if committed {
		s.engine.publishChanges(ctx, s.input, userChanged, tokenChanged)
	}
	return nil
}

// Settle commits the final amount without recording usage statistics.
// Call SettleWithUsage when the request's user/channel statistics belong
// to the same atomic commit.
func (s *Session) Settle(ctx context.Context, actual int) error {
	return s.settle(ctx, actual, 0, false, nil)
}

// SettleWithUsage atomically commits final billing and the corresponding user
// and channel usage statistics. A repeated call with the same actual amount
// and channel is idempotent and never writes the statistics twice.
func (s *Session) SettleWithUsage(ctx context.Context, actual, channelID int) error {
	if channelID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("channel id is required for usage settlement"))
	}
	return s.settle(ctx, actual, channelID, true, nil)
}

// SettleWithUsageAndCommit settles the request and executes commit inside the
// same PostgreSQL transaction after the durable session reaches its terminal
// state. It is used by task creation so a task's initial-settlement marker and
// the account ledger cannot commit independently. Replays execute commit
// again after checking the same terminal amount/channel, without recharging.
func (s *Session) SettleWithUsageAndCommit(ctx context.Context, actual, channelID int, commit func(*gorm.DB) error) error {
	if commit == nil {
		return failure(contract.BillingInvalidRequest, errors.New("settlement commit callback is required"))
	}
	if channelID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("channel id is required for usage settlement"))
	}
	return s.settle(ctx, actual, channelID, true, commit)
}

// MarkDispatch records that an upstream task submission is about to be sent.
// Until a known result arrives, the reservation must not be automatically
// refunded: the remote task may exist even when its response was lost.
func (s *Session) MarkDispatch(ctx context.Context, channelID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refundBlocked {
		return sessionConflict("billing request already has an unresolved outcome")
	}
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status != sessionStatusActive || record.PendingAction != "" {
			return sessionConflict("billing request already has a dispatched or terminal outcome")
		}
		record.PendingAction, record.IntentChannel = "reconcile", channelID
		if err := tx.Model(record).Updates(map[string]any{"pending_action": record.PendingAction, "intent_channel": channelID, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		nextState = stateFromRecord(record)
		return nil
	})
	if err == nil {
		s.state, s.refundBlocked = nextState, true
	}
	return err
}

// ResolveRejectedDispatch releases the uncertain-outcome fence only after a
// definitive upstream rejection. The caller may then retry or refund using
// the ordinary request lifecycle; transport errors must not call this method.
func (s *Session) ResolveRejectedDispatch(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status != sessionStatusActive || (record.PendingAction != "reconcile" && record.PendingAction != "") {
			return sessionConflict("billing request has a conflicting terminal outcome")
		}
		record.PendingAction, record.IntentChannel = "", 0
		if err := tx.Model(record).Updates(map[string]any{"pending_action": "", "intent_channel": 0, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		nextState = stateFromRecord(record)
		return nil
	})
	if err == nil {
		s.state, s.refundBlocked = nextState, false
	}
	return err
}

// persistIntent records the caller's observed upstream outcome before any
// compensating ledger write. A failed funding transaction therefore remains
// distinguishable from an untouched active request and cannot be blindly
// refunded by a controller defer.
func (s *Session) persistIntent(ctx context.Context, action string, actual, channelID int, withUsage, requiresCommit bool) error {
	var committed bool
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status == sessionStatusSettled || record.Status == sessionStatusRefunded {
			return nil
		}
		if record.PendingAction == "reconcile" && action != "settle" {
			return sessionConflict("upstream outcome is unresolved; automatic refund is not allowed")
		}
		if record.PendingAction != "" && record.PendingAction != "reconcile" {
			if record.PendingAction != action {
				return sessionConflict("billing session has another pending terminal action")
			}
			if action == "settle" && (record.IntentActual == nil || *record.IntentActual != actual || record.IntentChannel != channelID || record.IntentUsage != withUsage || record.IntentRequiresCommit != requiresCommit) {
				return sessionConflict("settlement intent conflicts with the durable request")
			}
			nextState = stateFromRecord(record)
			return nil
		}
		record.PendingAction = action
		record.IntentUsage = withUsage
		record.IntentRequiresCommit = requiresCommit
		record.IntentChannel = channelID
		if action == "settle" {
			record.IntentActual = &actual
		}
		if err := tx.Model(record).Updates(map[string]any{
			"pending_action":         record.PendingAction,
			"intent_actual":          record.IntentActual,
			"intent_channel":         record.IntentChannel,
			"intent_usage":           record.IntentUsage,
			"intent_requires_commit": record.IntentRequiresCommit,
			"updated_at":             common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}
		nextState = stateFromRecord(record)
		committed = true
		return nil
	})
	if err != nil {
		// If the intent itself could not be persisted, this process must not
		// let a cancellation path refund the still-active reservation.
		if action == "settle" {
			s.refundBlocked = true
		}
		return err
	}
	if committed {
		s.refundBlocked = false
	}
	if err == nil && (committed || nextState.Status != "") {
		s.state = nextState
	}
	return nil
}

func (s *Session) settle(ctx context.Context, actual, channelID int, withUsage bool, commit func(*gorm.DB) error) error {
	if err := validateQuota(actual); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistIntent(ctx, "settle", actual, channelID, withUsage, commit != nil); err != nil {
		return err
	}

	var committed bool
	var userChanged, tokenChanged bool
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status == sessionStatusRefunded {
			return sessionConflict("cannot settle a refunded billing session")
		}
		if record.Status == sessionStatusActive && record.PendingAction != "settle" {
			return sessionConflict("settlement intent is missing")
		}
		if record.Status == sessionStatusActive && (record.IntentActual == nil || *record.IntentActual != actual || record.IntentChannel != channelID || record.IntentUsage != withUsage || record.IntentRequiresCommit != (commit != nil)) {
			return sessionConflict("settlement intent conflicts with the requested result")
		}
		if record.Status == sessionStatusSettled {
			if record.ActualQuota == nil || *record.ActualQuota != actual {
				return sessionConflict("settlement amount conflicts with the durable result")
			}
			if withUsage != record.UsageRecorded {
				return sessionConflict("settlement statistics mode conflicts with the durable result")
			}
			if withUsage && record.ChannelID != channelID {
				return sessionConflict("settlement channel conflicts with the durable result")
			}
			if commit != nil {
				if err := commit(tx); err != nil {
					return err
				}
				committed = true
			}
			nextState = stateFromRecord(record)
			return nil
		}

		delta := actual - record.ReservedQuota
		accountingTx := s.engine.deps.Accounting.WithHistoricalTx(tx)
		if _, err := accountingTx.UserQuotaTx(ctx, record.UserID); err != nil {
			return s.engine.fundingFailure(err)
		}
		if record.Source == contract.BillingSourceWallet {
			if delta > 0 {
				if err := accountingTx.DecreaseUserQuota(ctx, record.UserID, delta, false); err != nil {
					return s.engine.fundingFailure(err)
				}
				userChanged = true
			} else if delta < 0 {
				if err := accountingTx.IncreaseUserQuota(ctx, record.UserID, -delta, false); err != nil {
					return s.engine.fundingFailure(err)
				}
				userChanged = true
			}
		} else if delta != 0 {
			if s.engine.deps.Subscriptions == nil {
				return failure(contract.BillingStorageFailure, errors.New("subscription quota store is unavailable"))
			}
			if _, err := s.engine.deps.Subscriptions.WithTx(tx).PostConsumeUserSubscriptionDeltaForUserTx(ctx, tx, record.UserID, record.SubscriptionID, int64(delta)); err != nil {
				return s.engine.fundingFailure(err)
			}
		}
		if !record.Playground {
			if delta > 0 {
				if err := accountingTx.DecreaseTokenQuota(ctx, record.TokenID, s.input.TokenKey, delta); err != nil {
					return failure(contract.BillingStorageFailure, err)
				}
				tokenChanged = true
			} else if delta < 0 {
				if err := accountingTx.IncreaseTokenQuota(ctx, record.TokenID, s.input.TokenKey, -delta); err != nil {
					return failure(contract.BillingStorageFailure, err)
				}
				tokenChanged = true
			}
		}
		if withUsage {
			if err := s.recordUsageTx(ctx, tx, record, actual, channelID); err != nil {
				return err
			}
			record.UsageRecorded, record.ChannelID = true, channelID
		}
		record.ActualQuota = &actual
		record.Status = sessionStatusSettled
		record.PendingAction = ""
		record.SubscriptionPostDelta = int64(delta)
		if record.Source != contract.BillingSourceSubscription {
			record.SubscriptionPostDelta = 0
		}
		if err := tx.Model(record).Updates(map[string]any{
			"actual_quota":            actual,
			"status":                  record.Status,
			"pending_action":          record.PendingAction,
			"subscription_post_delta": record.SubscriptionPostDelta,
			"usage_recorded":          record.UsageRecorded,
			"channel_id":              record.ChannelID,
			"updated_at":              common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}
		if record.Source == contract.BillingSourceSubscription {
			if err := s.engine.refreshSubscriptionStateTx(ctx, tx, record); err != nil {
				return err
			}
			if err := tx.Model(record).Updates(map[string]any{"subscription_used": record.SubscriptionUsed, "updated_at": common.GetTimestamp()}).Error; err != nil {
				return err
			}
		}
		if commit != nil {
			if err := commit(tx); err != nil {
				return err
			}
		}
		nextState = stateFromRecord(record)
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	s.state = nextState
	if committed {
		s.engine.publishChanges(ctx, s.input, userChanged, tokenChanged)
	}
	return nil
}

// Refund atomically restores the current durable reservation and marks the
// row refunded. Replays see the terminal receipt and return nil; a settled
// session is a conflicting terminal state and must be reconciled explicitly.
func (s *Session) Refund(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refundBlocked {
		return sessionConflict("settlement outcome could not be durably recorded; session requires reconciliation")
	}
	if err := s.persistIntent(ctx, "refund", 0, 0, false, false); err != nil {
		return err
	}

	var committed bool
	var userChanged, tokenChanged bool
	var nextState contract.BillingState
	err := s.engine.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		record, err := s.engine.lockSessionTx(ctx, tx, s.input, "", nil)
		if err != nil {
			return err
		}
		if record.Status == sessionStatusRefunded {
			nextState = stateFromRecord(record)
			return nil
		}
		if record.Status == sessionStatusSettled {
			return sessionConflict("cannot refund a settled billing session")
		}
		if record.PendingAction != "refund" {
			return sessionConflict("refund intent is missing")
		}
		accountingTx := s.engine.deps.Accounting.WithHistoricalTx(tx)
		if _, err := accountingTx.UserQuotaTx(ctx, record.UserID); err != nil {
			return s.engine.fundingFailure(err)
		}
		if record.Source == contract.BillingSourceWallet && record.ReservedQuota > 0 {
			if err := accountingTx.IncreaseUserQuota(ctx, record.UserID, record.ReservedQuota, false); err != nil {
				return s.engine.fundingFailure(err)
			}
			userChanged = true
		} else if record.Source == contract.BillingSourceSubscription {
			if s.engine.deps.Subscriptions == nil {
				return failure(contract.BillingStorageFailure, errors.New("subscription quota store is unavailable"))
			}
			if err := s.engine.deps.Subscriptions.WithTx(tx).RefundSubscriptionPreConsumeTx(ctx, tx, record.RequestID); err != nil {
				return s.engine.fundingFailure(err)
			}
		}
		if !record.Playground && record.ReservedQuota > 0 {
			if err := accountingTx.IncreaseTokenQuota(ctx, record.TokenID, s.input.TokenKey, record.ReservedQuota); err != nil {
				return failure(contract.BillingStorageFailure, err)
			}
			tokenChanged = true
		}
		record.Status = sessionStatusRefunded
		record.PendingAction = ""
		if err := tx.Model(record).Updates(map[string]any{"status": record.Status, "pending_action": record.PendingAction, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		nextState = stateFromRecord(record)
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	s.state = nextState
	if committed {
		logger.LogInfo(ctx, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（quota=%s, source=%s）", s.input.UserID, logger.FormatQuota(s.state.PreConsumedQuota), s.state.Source))
		s.engine.publishChanges(ctx, s.input, userChanged, tokenChanged)
	}
	return nil
}

func (s *Session) recordUsageTx(ctx context.Context, tx *gorm.DB, record *billingSessionRecord, actual, channelID int) error {
	if channelID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("channel id is required for usage settlement"))
	}
	accountingTx := s.engine.deps.Accounting.WithTx(tx)
	if err := accountingTx.RecordUsageTx(ctx, tx, record.UserID, actual, 1); err != nil {
		return err
	}
	return accountingTx.RecordChannelUsageTx(ctx, tx, channelID, actual)
}

func stateFromRecord(record *billingSessionRecord) contract.BillingState {
	state := contract.BillingState{
		Source:                record.Source,
		UserQuota:             record.UserQuota,
		PreConsumedQuota:      record.ReservedQuota,
		SubscriptionID:        record.SubscriptionID,
		PlanID:                record.PlanID,
		PlanTitle:             record.PlanTitle,
		SubscriptionPostDelta: record.SubscriptionPostDelta,
		SubscriptionTotal:     record.SubscriptionTotal,
		SubscriptionUsed:      record.SubscriptionUsed,
		Status:                record.Status,
		Trusted:               record.Trusted,
		PendingAction:         record.PendingAction,
		PendingChannelID:      record.IntentChannel,
		PendingUsage:          record.IntentUsage,
		UsageRecorded:         record.UsageRecorded,
		ChannelID:             record.ChannelID,
	}
	if record.Source == contract.BillingSourceSubscription {
		state.SubscriptionPreConsumed = int64(record.ReservedQuota)
	}
	if record.ActualQuota != nil {
		state.ActualQuota = *record.ActualQuota
	}
	if record.IntentActual != nil {
		state.PendingActualQuota = *record.IntentActual
	}
	return state
}

func validateQuota(value int) error {
	if value < 0 || value > common.MaxQuota {
		return failure(contract.BillingInvalidQuota, fmt.Errorf("billing quota out of range: %d", value))
	}
	return nil
}

func sessionConflict(message string) error {
	return &contract.BillingFailure{Kind: contract.BillingSessionConflict, Cause: fmt.Errorf("%w: %s", contract.ErrBillingSessionConflict, message)}
}
