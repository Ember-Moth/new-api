package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/httpclient"

	"github.com/QuantumNous/new-api/internal/config/setting"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	relayconstant "github.com/QuantumNous/new-api/internal/legacy/relay/constant"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/shared/dto"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	midjourneyBillingActionSettlement = "settlement"
	midjourneyBillingActionRefund     = "refund"
)

func CovertMjpActionToModelName(mjAction string) string {
	modelName := "mj_" + strings.ToLower(mjAction)
	if mjAction == constant.MjActionSwapFace {
		modelName = "swap_face"
	}
	return modelName
}

func midjourneyBillingOperationID(id int, action string) string {
	if id <= 0 || action == "" {
		return ""
	}
	return fmt.Sprintf("midjourney:%d:%s", id, action)
}

func setMidjourneyBillingIntent(task *model.Midjourney, action string, targetQuota, delta int) {
	if task == nil {
		return
	}
	task.BillingPending = action == midjourneyBillingActionSettlement || delta != 0
	task.BillingAction = action
	task.BillingOperationID = midjourneyBillingOperationID(task.Id, action)
	task.BillingTargetQuota = targetQuota
	task.BillingDelta = delta
	if !task.BillingPending {
		clearMidjourneyBillingIntent(task)
	}
}

func setMidjourneyBillingUnknown(task *model.Midjourney) {
	if task == nil {
		return
	}
	if task.BillingPending && task.BillingAction != "" {
		return
	}
	task.BillingPending = true
	task.BillingAction = "unknown"
	task.BillingOperationID = ""
	task.BillingTargetQuota = task.Quota
	task.BillingDelta = 0
}

func clearMidjourneyBillingIntent(task *model.Midjourney) {
	if task == nil {
		return
	}
	task.BillingPending = false
	task.BillingAction = ""
	task.BillingOperationID = ""
	task.BillingTargetQuota = 0
	task.BillingDelta = 0
}

func midjourneyBillingInput(tx *gorm.DB, task *model.Midjourney) (billingcontract.BillingRequest, error) {
	if task == nil || task.Id <= 0 {
		return billingcontract.BillingRequest{}, errors.New("Midjourney billing requires a persisted task")
	}
	if task.UserId <= 0 {
		return billingcontract.BillingRequest{}, errors.New("Midjourney billing user is invalid")
	}
	requestID := task.BillingRequestID
	if requestID == "" {
		requestID = fmt.Sprintf("midjourney:%d", task.Id)
	}
	input := billingcontract.BillingRequest{
		RequestID:       requestID,
		ModelName:       CovertMjpActionToModelName(task.Action),
		UserID:          task.UserId,
		TokenID:         task.TokenId,
		TokenUnlimited:  task.BillingTokenUnlimited,
		Playground:      task.BillingPlayground || task.TokenId == 0,
		ForcePreConsume: true,
	}
	if input.Playground {
		return input, nil
	}
	token, err := model.GetHistoricalTokenForBilling(tx, task.UserId, task.TokenId)
	if err != nil {
		return billingcontract.BillingRequest{}, fmt.Errorf("load historical Midjourney token: %w", err)
	}
	input.TokenKey = token.Key
	return input, nil
}

func persistMidjourneyBillingIntent(ctx context.Context, taskID int, action string, targetQuota, delta, tokenID int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := model.LockMidjourneyForBilling(tx, taskID)
		if err != nil {
			return err
		}
		if task.BillingPending {
			if task.BillingAction != action || task.BillingTargetQuota != targetQuota || task.BillingDelta != delta {
				return fmt.Errorf("Midjourney billing intent conflicts with existing marker")
			}
			return nil
		}
		validMarker := int64(task.Quota)+int64(delta) == int64(targetQuota)
		if action == midjourneyBillingActionSettlement {
			validMarker = task.Quota == targetQuota && delta == targetQuota
		}
		if targetQuota < 0 || targetQuota > common.MaxQuota || task.Quota < 0 || task.Quota > common.MaxQuota || !validMarker {
			return fmt.Errorf("Midjourney billing intent conflicts with current quota")
		}
		if tokenID > 0 {
			task.TokenId = tokenID
		}
		setMidjourneyBillingIntent(task, action, targetQuota, delta)
		return task.UpdateBillingStateTx(tx)
	})
}

func applyMidjourneyBillingIntent(ctx context.Context, taskID int) (billingcontract.QuotaAdjustment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	engine := billingEngine()
	var result billingcontract.QuotaAdjustment
	var input billingcontract.BillingRequest
	var committedTask *model.Midjourney
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Acquire the user lock before the task row. Session settlement locks
		// the user before invoking its task-marker callback; keeping refund
		// recovery in the same order avoids a terminal-operation deadlock.
		var owner struct {
			UserID int `gorm:"column:user_id"`
		}
		if err := tx.Model(&model.Midjourney{}).Select("user_id").Where("id = ?", taskID).First(&owner).Error; err != nil {
			return err
		}
		if owner.UserID <= 0 {
			return errors.New("Midjourney billing user is invalid")
		}
		if _, err := model.AccountingStore().WithHistoricalTx(tx).UserQuotaTx(ctx, owner.UserID); err != nil {
			return err
		}
		task, err := model.LockMidjourneyForBilling(tx, taskID)
		if err != nil {
			return err
		}
		if task.UserId != owner.UserID {
			return errors.New("Midjourney billing task owner changed")
		}
		if !task.BillingPending {
			committedTask = task
			return nil
		}
		if task.BillingAction != midjourneyBillingActionSettlement && task.BillingAction != midjourneyBillingActionRefund {
			return errors.New("Midjourney billing outcome is unresolved")
		}
		if task.BillingAction == midjourneyBillingActionSettlement {
			return errors.New("Midjourney settlement must resume its billing session")
		}
		if task.BillingOperationID != midjourneyBillingOperationID(task.Id, task.BillingAction) {
			return errors.New("Midjourney billing operation identity is invalid")
		}
		validMarker := int64(task.Quota)+int64(task.BillingDelta) == int64(task.BillingTargetQuota)
		if task.BillingAction == midjourneyBillingActionSettlement {
			validMarker = task.Quota == task.BillingTargetQuota && task.BillingDelta == task.BillingTargetQuota
		}
		if task.Quota < 0 || task.Quota > common.MaxQuota || task.BillingTargetQuota < 0 || task.BillingTargetQuota > common.MaxQuota || !validMarker {
			return errors.New("Midjourney billing marker conflicts with current quota")
		}
		input, err = midjourneyBillingInput(tx, task)
		if err != nil {
			return err
		}
		source := task.BillingSource
		if source == "" {
			source = BillingSourceWallet
		}
		result, err = engine.ApplyAdjustmentTx(ctx, tx, input, billingcontract.BillingAdjustment{
			OperationID:        task.BillingOperationID,
			Source:             source,
			SubscriptionID:     task.BillingSubscriptionID,
			Delta:              task.BillingDelta,
			UsageDelta:         task.BillingDelta,
			RequestDelta:       0,
			ChannelID:          task.GetBillingChannelId(),
			UseHistoricalToken: true,
		})
		if err != nil {
			return err
		}
		task.Quota = task.BillingTargetQuota
		clearMidjourneyBillingIntent(task)
		if err := task.UpdateBillingStateTx(tx); err != nil {
			return err
		}
		committedTask = task
		return nil
	})
	if err != nil {
		return billingcontract.QuotaAdjustment{}, err
	}
	if committedTask != nil && input.UserID > 0 {
		engine.PublishCommitted(input)
	}
	return result, nil
}

// PrepareMidjourneyTaskBilling binds the task to the already authorized
// request/session. The durable settlement marker is installed only after the
// task receives its database id, in the same transaction as the insert.
func PrepareMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int, shouldBill bool) (bool, error) {
	if task == nil {
		return false, errors.New("Midjourney task is nil")
	}
	task.Quota = 0
	task.TokenId = 0
	task.BillingChannelId = 0
	task.BillingRequestID = ""
	task.BillingTokenUnlimited = false
	task.BillingPlayground = false
	task.BillingFreeModel = false
	task.BillingSource = ""
	task.BillingSubscriptionID = 0
	if !shouldBill {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if quota < 0 || quota > common.MaxQuota {
		return false, errors.New("quota is out of range")
	}
	if relayInfo.RequestId == "" {
		return false, errors.New("Midjourney billing request id is missing")
	}
	if task.ChannelId <= 0 {
		return false, errors.New("Midjourney billing channel is missing")
	}
	if !relayInfo.PriceData.FreeModel {
		if relayInfo.Billing == nil {
			return false, errors.New("Midjourney billing session is missing")
		}
		expectedPreConsumed := quota
		if relayInfo.BillingSource == BillingSourceSubscription && expectedPreConsumed == 0 {
			expectedPreConsumed = 1
		}
		if relayInfo.Billing.GetPreConsumedQuota() != expectedPreConsumed {
			return false, fmt.Errorf("Midjourney pre-consume mismatch: got=%d want=%d", relayInfo.Billing.GetPreConsumedQuota(), expectedPreConsumed)
		}
	}

	if relayInfo.PriceData.FreeModel {
		quota = 0
	}
	task.Quota = quota
	task.TokenId = relayInfo.TokenId
	task.BillingRequestID = relayInfo.RequestId
	task.BillingTokenUnlimited = relayInfo.TokenUnlimited
	task.BillingPlayground = relayInfo.IsPlayground || relayInfo.TokenId == 0
	task.BillingFreeModel = relayInfo.PriceData.FreeModel
	task.BillingSource = relayInfo.BillingSource
	if task.BillingSource == "" {
		task.BillingSource = BillingSourceWallet
	}
	if task.BillingSource != BillingSourceWallet && task.BillingSource != BillingSourceSubscription {
		return false, errors.New("Midjourney billing source is invalid")
	}
	task.BillingSubscriptionID = relayInfo.SubscriptionId
	if task.BillingSource == BillingSourceSubscription && task.BillingSubscriptionID <= 0 {
		return false, errors.New("Midjourney subscription identity is missing")
	}
	task.BillingChannelId = task.ChannelId
	return true, nil
}

// PreConsumeMidjourneyBilling creates the request session before an upstream
// submission is attempted. Async MJ work therefore has a durable reservation
// even if the provider response is delayed or the client disconnects.
func PreConsumeMidjourneyBilling(c *gin.Context, relayInfo *relaycommon.RelayInfo, quota int) (bool, error) {
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if quota < 0 || quota > common.MaxQuota {
		return false, errors.New("Midjourney quota is out of range")
	}
	relayInfo.ForcePreConsume = true
	if relayInfo.PriceData.FreeModel {
		return true, nil
	}
	if apiErr := PreConsumeBilling(c, quota, relayInfo); apiErr != nil {
		return false, apiErr
	}
	return true, nil
}

// PersistMidjourneyTaskWithBilling inserts a successful upstream task and its
// initial settlement marker in one PostgreSQL transaction. The marker remains
// until Session.SettleWithUsageAndCommit clears it together with the ledger.
func PersistMidjourneyTaskWithBilling(task *model.Midjourney, prepared bool) error {
	if task == nil {
		return errors.New("Midjourney task is nil")
	}
	if !prepared {
		return task.Insert()
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return errors.New("Midjourney task quota is out of range")
	}
	ctx, cancel := context.WithTimeout(context.Background(), billingOperationTimeout)
	defer cancel()
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := task.InsertTx(tx); err != nil {
			return err
		}
		setMidjourneyBillingIntent(task, midjourneyBillingActionSettlement, task.Quota, task.Quota)
		return task.UpdateBillingStateTx(tx)
	})
}

// PersistMidjourneyUnknownTask retains a local reconciliation record when the
// upstream outcome cannot be settled automatically. A provider id, when one
// was returned, is kept for manual reconciliation; only a conflicting local
// id is cleared so this user's reservation can still be represented locally.
// It deliberately leaves the billing session active and never schedules an
// automatic refund.
func PersistMidjourneyUnknownTask(ctx context.Context, task *model.Midjourney, quota int, reason string) error {
	if task == nil {
		return errors.New("Midjourney task is nil")
	}
	if task.UserId <= 0 || task.BillingRequestID == "" {
		return errors.New("Midjourney unknown task provenance is missing")
	}
	if quota < 0 || quota > common.MaxQuota {
		return errors.New("Midjourney task quota is out of range")
	}
	if task.BillingFreeModel {
		quota = 0
	}
	if task.BillingSource == "" {
		task.BillingSource = BillingSourceWallet
	}
	if task.BillingSource != BillingSourceWallet && task.BillingSource != BillingSourceSubscription {
		return errors.New("Midjourney billing source is invalid")
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	requestCtx, cancel := context.WithTimeout(baseCtx, billingOperationTimeout)
	defer cancel()
	providerID := strings.TrimSpace(task.MjId)
	task.Quota = quota
	task.Id = 0
	if providerID == "" {
		task.MjId = ""
	}
	task.Status = "UNKNOWN"
	task.Progress = "100%"
	task.FailReason = reason
	task.BillingPending = true
	task.BillingAction = "unknown"
	task.BillingOperationID = ""
	task.BillingTargetQuota = quota
	task.BillingDelta = 0
	persist := func() error {
		return model.DB.WithContext(requestCtx).Transaction(func(tx *gorm.DB) error {
			if err := task.InsertTx(tx); err != nil {
				return err
			}
			return task.UpdateBillingStateTx(tx)
		})
	}
	if err := persist(); err == nil {
		return nil
	} else if providerID == "" || model.GetByOnlyMJId(providerID) == nil {
		return err
	}
	// The provider id already belongs to another local row. Keep the durable
	// unknown marker without claiming that row's upstream identity.
	task.Id = 0
	task.MjId = ""
	return persist()
}

// RefundMidjourneyBilling explicitly refunds a request that the provider
// confirmed as rejected. Unknown provider outcomes must use the unknown-task
// path instead of calling this helper.
func RefundMidjourneyBilling(ctx context.Context, relayInfo *relaycommon.RelayInfo) error {
	if relayInfo == nil {
		return nil
	}
	if relayInfo.Billing == nil {
		if relayInfo.PriceData.FreeModel {
			relayInfo.TaskSubmissionUncertain = false
			return nil
		}
		return errors.New("Midjourney billing session is missing")
	}
	adapter, ok := relayInfo.Billing.(*BillingSession)
	if !ok || adapter.session == nil {
		return errors.New("Midjourney billing session adapter is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := adapter.session.Snapshot()
	if state.Status == "settled" || state.Status == "refunded" {
		return nil
	}
	if relayInfo.TaskSubmissionUncertain || state.PendingAction == "reconcile" {
		return errors.New("Midjourney upstream outcome is unresolved")
	}
	baseCtx := adapter.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(baseCtx, billingOperationTimeout)
	defer cancel()
	err := adapter.session.Refund(requestCtx)
	adapter.sync()
	return err
}

// ResolveMidjourneyRejectedDispatch records the definitive provider rejection
// needed before a marked dispatch can be refunded.
func ResolveMidjourneyRejectedDispatch(ctx context.Context, relayInfo *relaycommon.RelayInfo) error {
	if relayInfo == nil {
		return errors.New("Midjourney billing session is missing")
	}
	if relayInfo.Billing == nil {
		if relayInfo.PriceData.FreeModel {
			relayInfo.TaskSubmissionUncertain = false
			return nil
		}
		return errors.New("Midjourney billing session is missing")
	}
	adapter, ok := relayInfo.Billing.(*BillingSession)
	if !ok || adapter.session == nil {
		return errors.New("Midjourney billing session adapter is unavailable")
	}
	baseCtx := adapter.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(baseCtx, billingOperationTimeout)
	defer cancel()
	err := adapter.session.ResolveRejectedDispatch(requestCtx)
	if err == nil {
		relayInfo.TaskSubmissionUncertain = false
	}
	adapter.sync()
	return err
}

// RefundConfirmedMidjourneyBilling resolves a marked dispatch only when the
// caller has a definitive rejection, then performs the idempotent refund.
func RefundConfirmedMidjourneyBilling(ctx context.Context, relayInfo *relaycommon.RelayInfo) error {
	if err := ResolveMidjourneyRejectedDispatch(ctx, relayInfo); err != nil {
		stateErr := err
		if relayInfo != nil && relayInfo.Billing != nil {
			if adapter, ok := relayInfo.Billing.(*BillingSession); ok && adapter.session != nil {
				state := adapter.session.Snapshot()
				if state.Status == "settled" || state.Status == "refunded" || state.PendingAction == "" {
					stateErr = nil
				}
			}
		}
		if stateErr != nil {
			return err
		}
	}
	return RefundMidjourneyBilling(ctx, relayInfo)
}

// MarkMidjourneyDispatch fences a pre-consumed request before its upstream
// submission. A lost response therefore remains explicitly reconcilable and
// cannot be refunded by a generic cancellation path.
func MarkMidjourneyDispatch(ctx context.Context, relayInfo *relaycommon.RelayInfo, channelID int) error {
	if relayInfo == nil {
		return errors.New("Midjourney billing session is missing")
	}
	if relayInfo.Billing == nil {
		if relayInfo.PriceData.FreeModel {
			relayInfo.TaskSubmissionUncertain = true
			return nil
		}
		return errors.New("Midjourney billing session is missing")
	}
	adapter, ok := relayInfo.Billing.(*BillingSession)
	if !ok || adapter.session == nil {
		return errors.New("Midjourney billing session adapter is unavailable")
	}
	if channelID <= 0 {
		return errors.New("Midjourney dispatch channel is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := adapter.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(baseCtx, billingOperationTimeout)
	defer cancel()
	err := adapter.session.MarkDispatch(requestCtx, channelID)
	if err == nil {
		relayInfo.TaskSubmissionUncertain = true
		adapter.settlementAttempted = true
	}
	adapter.sync()
	return err
}

// settleWithUsageAndCommit adapts the module callback API for MJ task marker
// cleanup while preserving the request timeout used by the legacy transport.
func (s *BillingSession) settleWithUsageAndCommit(actual, channelID int, commit func(*gorm.DB) error) error {
	if s == nil || s.session == nil {
		return errors.New("billing session is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlementAttempted = true
	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, billingOperationTimeout)
	defer cancel()
	err := s.session.SettleWithUsageAndCommit(ctx, actual, channelID, commit)
	s.sync()
	return err
}

// commitMidjourneySettlement clears the task hand-off only after locking the
// current row in the same transaction as the session ledger. This prevents a
// stale poll snapshot from resurrecting a marker or clearing another request's
// marker during a replay.
func commitMidjourneySettlement(tx *gorm.DB, task *model.Midjourney, actual int) error {
	if task == nil || task.Id <= 0 {
		return errors.New("Midjourney billing task is invalid")
	}
	current, err := model.LockMidjourneyForBilling(tx, task.Id)
	if err != nil {
		return err
	}
	if task.BillingRequestID != "" && current.BillingRequestID != "" && task.BillingRequestID != current.BillingRequestID {
		return errors.New("Midjourney billing request identity conflicts with the task")
	}
	if current.BillingPending {
		if current.BillingAction != midjourneyBillingActionSettlement || current.BillingTargetQuota != actual || current.BillingDelta != actual || current.Quota != actual {
			return errors.New("Midjourney settlement marker conflicts with the requested result")
		}
	}
	clearMidjourneyBillingIntent(current)
	if err := current.UpdateBillingStateTx(tx); err != nil {
		return err
	}
	return nil
}

// settleMidjourneyFreeTaskBilling records the per-request statistic and the
// settlement receipt for a true free model that deliberately has no funding
// session. The receipt and task marker still commit together, so a retry does
// not increment request_count twice.
func settleMidjourneyFreeTaskBilling(ctx context.Context, task *model.Midjourney) (bool, error) {
	if task == nil || task.Id <= 0 {
		return false, errors.New("Midjourney task must be persisted before billing")
	}
	if task.GetBillingChannelId() <= 0 {
		return false, errors.New("Midjourney billing channel is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	engine := billingEngine()
	var result billingcontract.QuotaAdjustment
	var input billingcontract.BillingRequest
	var replayed bool
	var candidate model.Midjourney
	var candidateSet bool
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := model.LockMidjourneyForBilling(tx, task.Id)
		if err != nil {
			return err
		}
		if !current.BillingPending {
			return nil
		}
		if current.BillingAction != midjourneyBillingActionSettlement || current.BillingTargetQuota != 0 || current.BillingDelta != 0 {
			return errors.New("Midjourney free settlement marker is invalid")
		}
		input, err = midjourneyBillingInput(tx, current)
		if err != nil {
			return err
		}
		source := current.BillingSource
		if source == "" {
			source = BillingSourceWallet
		}
		result, err = engine.ApplyAdjustmentTx(ctx, tx, input, billingcontract.BillingAdjustment{
			OperationID:        current.BillingOperationID,
			Source:             source,
			SubscriptionID:     current.BillingSubscriptionID,
			Delta:              0,
			UsageDelta:         0,
			RequestDelta:       1,
			ChannelID:          current.GetBillingChannelId(),
			UseHistoricalToken: true,
		})
		if err != nil {
			return err
		}
		replayed = result.Replayed
		clearMidjourneyBillingIntent(current)
		if err := current.UpdateBillingStateTx(tx); err != nil {
			return err
		}
		candidate = *current
		candidateSet = true
		return nil
	})
	if err != nil {
		return replayed, err
	}
	if candidateSet {
		*task = candidate
	}
	if input.UserID > 0 {
		engine.PublishCommitted(input)
	}
	return replayed, nil
}

// SettleMidjourneyTaskBilling settles a persisted MJ task through its original
// request session. Task marker cleanup is part of the same terminal ledger
// transaction, so this path never applies the original quota a second time.
func SettleMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, prepared bool) (bool, error) {
	if !prepared {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if task == nil || task.Id == 0 {
		return false, errors.New("Midjourney task must be persisted before billing")
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return false, errors.New("Midjourney task quota is out of range")
	}
	if task.BillingRequestID != "" && relayInfo.RequestId != "" && task.BillingRequestID != relayInfo.RequestId {
		return false, errors.New("Midjourney billing request identity conflicts with the task")
	}
	if task.BillingPending && task.BillingAction != midjourneyBillingActionSettlement {
		return false, errors.New("Midjourney task has another pending billing stage")
	}
	if task.BillingFreeModel {
		ctx, cancel := context.WithTimeout(context.Background(), billingOperationTimeout)
		defer cancel()
		_, err := settleMidjourneyFreeTaskBilling(ctx, task)
		if err == nil {
			relayInfo.TaskSubmissionUncertain = false
		}
		return err == nil, err
	}
	if relayInfo.Billing == nil {
		ctx, cancel := context.WithTimeout(context.Background(), billingOperationTimeout)
		defer cancel()
		_, err := settleMidjourneyTaskFromSession(ctx, task)
		return err == nil, err
	}
	adapter, ok := relayInfo.Billing.(*BillingSession)
	if !ok {
		return false, errors.New("Midjourney billing session adapter is unavailable")
	}
	channelID := task.GetBillingChannelId()
	if channelID <= 0 {
		return false, errors.New("Midjourney billing channel is missing")
	}
	commit := func(tx *gorm.DB) error {
		return commitMidjourneySettlement(tx, task, task.Quota)
	}
	if err := adapter.settleWithUsageAndCommit(task.Quota, channelID, commit); err != nil {
		return false, err
	}
	relayInfo.TaskSubmissionUncertain = false
	clearMidjourneyBillingIntent(task)
	return true, nil
}

// settleMidjourneyTaskFromSession resumes the original request session for a
// task whose initial settlement marker survived a process interruption.
func settleMidjourneyTaskFromSession(ctx context.Context, task *model.Midjourney) (bool, error) {
	if task == nil || task.Id <= 0 {
		return false, errors.New("Midjourney task must be persisted before recovery")
	}
	if task.BillingRequestID == "" {
		return false, errors.New("Midjourney billing request identity is missing")
	}
	if task.GetBillingChannelId() <= 0 {
		return false, errors.New("Midjourney billing channel is missing")
	}
	engine := billingEngine()
	session, err := engine.Resume(ctx, task.BillingRequestID)
	if err != nil {
		return false, err
	}
	replayed := session.Snapshot().Status == "settled"
	commit := func(tx *gorm.DB) error {
		return commitMidjourneySettlement(tx, task, task.Quota)
	}
	if err := session.SettleWithUsageAndCommit(ctx, task.Quota, task.GetBillingChannelId(), commit); err != nil {
		return replayed, err
	}
	clearMidjourneyBillingIntent(task)
	return replayed, nil
}

// RefundMidjourneyQuota reverses every accounting element recorded for a billed legacy task.
func RefundMidjourneyQuota(ctx context.Context, task *model.Midjourney, reason string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if task == nil || task.Id <= 0 {
		logger.LogWarn(ctx, "Midjourney refund requires a persisted task")
		return false
	}
	quota := task.Quota
	if quota == 0 && !task.BillingPending {
		return true
	}
	if task.BillingPending && task.BillingAction != midjourneyBillingActionRefund {
		logger.LogWarn(ctx, fmt.Sprintf("Midjourney task %s has pending %s billing stage", task.MjId, task.BillingAction))
		return false
	}
	if !task.BillingPending {
		if err := persistMidjourneyBillingIntent(ctx, task.Id, midjourneyBillingActionRefund, 0, -quota, 0); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("创建 Midjourney 退款意图失败 task %s: %s", task.MjId, err.Error()))
			return false
		}
	}
	result, err := applyMidjourneyBillingIntent(ctx, task.Id)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还 Midjourney 额度失败 task %s: %s", task.MjId, err.Error()))
		return false
	}
	if result.Replayed {
		task.Quota = 0
		clearMidjourneyBillingIntent(task)
		return true
	}
	other := model.NewLogOther()
	other.SetPublic("task_id", task.MjId)
	other.SetPublic("reason", reason)
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.GetBillingChannelId(),
		ModelName: CovertMjpActionToModelName(task.Action),
		Quota:     quota,
		TokenId:   task.TokenId,
		Other:     other,
	})
	task.Quota = 0
	clearMidjourneyBillingIntent(task)
	return true
}

func runPendingMidjourneyBilling(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var tasks []*model.Midjourney
	if err := model.DB.WithContext(ctx).
		Where("billing_pending = ? AND billing_action IN ?", true, []string{midjourneyBillingActionSettlement, midjourneyBillingActionRefund}).
		Order("id asc").Limit(500).Find(&tasks).Error; err != nil {
		return err
	}
	var recoveryErr error
	for _, task := range tasks {
		if task == nil {
			continue
		}
		preConsumed := task.Quota
		action := task.BillingAction
		target := task.BillingTargetQuota
		if action == midjourneyBillingActionSettlement {
			var replayed bool
			var err error
			if task.BillingFreeModel {
				replayed, err = settleMidjourneyFreeTaskBilling(ctx, task)
			} else {
				replayed, err = settleMidjourneyTaskFromSession(ctx, task)
			}
			if err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover Midjourney settlement %s: %w", task.MjId, err))
				continue
			}
			if !replayed {
				other := model.NewLogOther()
				other.SetPublic("task_id", task.MjId)
				other.SetPublic("billing_recovery", true)
				model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
					UserId:    task.UserId,
					LogType:   model.LogTypeConsume,
					Content:   "Midjourney账务恢复结算",
					ChannelId: task.GetBillingChannelId(),
					ModelName: CovertMjpActionToModelName(task.Action),
					Quota:     target,
					TokenId:   task.TokenId,
					Other:     other,
				})
			}
			if task.Status == "FAILURE" && task.Quota != 0 {
				if !RefundMidjourneyQuota(ctx, task, task.FailReason) {
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover Midjourney failed task %s: refund remains pending", task.MjId))
				}
			}
			continue
		}
		result, err := applyMidjourneyBillingIntent(ctx, task.Id)
		if err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover Midjourney billing %s: %w", task.MjId, err))
			continue
		}
		if !result.Replayed {
			if action == midjourneyBillingActionRefund {
				other := model.NewLogOther()
				other.SetPublic("task_id", task.MjId)
				other.SetPublic("reason", "Midjourney账务恢复退款")
				model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
					UserId:    task.UserId,
					LogType:   model.LogTypeRefund,
					Content:   "",
					ChannelId: task.GetBillingChannelId(),
					ModelName: CovertMjpActionToModelName(task.Action),
					Quota:     preConsumed - target,
					TokenId:   task.TokenId,
					Other:     other,
				})
			}
		}
		clearMidjourneyBillingIntent(task)
	}
	return recoveryErr
}

// MarkMidjourneyBillingUnknown records an outcome requiring reconciliation
// without asserting that the upstream task was free to refund. It is used when
// the upstream id or channel credentials are unavailable.
func MarkMidjourneyBillingUnknown(ctx context.Context, task *model.Midjourney, reason string) error {
	if task == nil || task.Id <= 0 {
		return errors.New("Midjourney billing requires a persisted task")
	}
	fromStatus := task.Status
	task.Status = "UNKNOWN"
	task.Progress = "100%"
	task.FailReason = reason
	setMidjourneyBillingUnknown(task)
	won, err := task.UpdateWithStatus(fromStatus)
	if err == nil && won && task.BillingPending {
		logger.LogWarn(ctx, fmt.Sprintf("Midjourney task %s billing outcome remains unknown: %s", task.MjId, reason))
	}
	return err
}

// PrepareMidjourneyRefund marks a known terminal failure before its status CAS
// is committed. A pending settlement is left intact so recovery can charge the
// accepted submission before starting this distinct refund stage.
func PrepareMidjourneyRefund(task *model.Midjourney) error {
	if task == nil || task.Id <= 0 {
		return errors.New("Midjourney refund requires a persisted task")
	}
	if task.Quota == 0 {
		return nil
	}
	if task.BillingPending {
		if task.BillingAction != midjourneyBillingActionRefund {
			return fmt.Errorf("Midjourney task has pending %s billing stage", task.BillingAction)
		}
		return nil
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return errors.New("Midjourney task quota is out of range")
	}
	setMidjourneyBillingIntent(task, midjourneyBillingActionRefund, 0, -task.Quota)
	return nil
}

func GetMjRequestModel(relayMode int, midjRequest *dto.MidjourneyRequest) (string, *dto.MidjourneyResponse, bool) {
	action := ""
	if relayMode == relayconstant.RelayModeMidjourneyAction {
		// plus request
		err := CoverPlusActionToNormalAction(midjRequest)
		if err != nil {
			return "", err, false
		}
		action = midjRequest.Action
	} else {
		switch relayMode {
		case relayconstant.RelayModeMidjourneyImagine:
			action = constant.MjActionImagine
		case relayconstant.RelayModeMidjourneyVideo:
			action = constant.MjActionVideo
		case relayconstant.RelayModeMidjourneyEdits:
			action = constant.MjActionEdits
		case relayconstant.RelayModeMidjourneyDescribe:
			action = constant.MjActionDescribe
		case relayconstant.RelayModeMidjourneyBlend:
			action = constant.MjActionBlend
		case relayconstant.RelayModeMidjourneyShorten:
			action = constant.MjActionShorten
		case relayconstant.RelayModeMidjourneyChange:
			action = midjRequest.Action
		case relayconstant.RelayModeMidjourneyModal:
			action = constant.MjActionModal
		case relayconstant.RelayModeSwapFace:
			action = constant.MjActionSwapFace
		case relayconstant.RelayModeMidjourneyUpload:
			action = constant.MjActionUpload
		case relayconstant.RelayModeMidjourneySimpleChange:
			params := ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return "", MidjourneyErrorWrapper(constant.MjRequestError, "invalid_request"), false
			}
			action = params.Action
		case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition, relayconstant.RelayModeMidjourneyNotify:
			return "", nil, true
		default:
			return "", MidjourneyErrorWrapper(constant.MjRequestError, "unknown_relay_action"), false
		}
	}
	modelName := CovertMjpActionToModelName(action)
	return modelName, nil, true
}

func CoverPlusActionToNormalAction(midjRequest *dto.MidjourneyRequest) *dto.MidjourneyResponse {
	// "customId": "MJ::JOB::upsample::2::3dbbd469-36af-4a0f-8f02-df6c579e7011"
	customId := midjRequest.CustomId
	if customId == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_is_required")
	}
	splits := strings.Split(customId, "::")
	var action string
	if splits[1] == "JOB" {
		action = splits[2]
	} else {
		action = splits[1]
	}

	if action == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}
	if strings.Contains(action, "upsample") {
		index, err := strconv.Atoi(splits[3])
		if err != nil {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		midjRequest.Index = index
		midjRequest.Action = constant.MjActionUpscale
	} else if strings.Contains(action, "variation") {
		midjRequest.Index = 1
		if action == "variation" {
			index, err := strconv.Atoi(splits[3])
			if err != nil {
				return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
			}
			midjRequest.Index = index
			midjRequest.Action = constant.MjActionVariation
		} else if action == "low_variation" {
			midjRequest.Action = constant.MjActionLowVariation
		} else if action == "high_variation" {
			midjRequest.Action = constant.MjActionHighVariation
		}
	} else if strings.Contains(action, "pan") {
		midjRequest.Action = constant.MjActionPan
		midjRequest.Index = 1
	} else if strings.Contains(action, "reroll") {
		midjRequest.Action = constant.MjActionReRoll
		midjRequest.Index = 1
	} else if action == "Outpaint" {
		midjRequest.Action = constant.MjActionZoom
		midjRequest.Index = 1
	} else if action == "CustomZoom" {
		midjRequest.Action = constant.MjActionCustomZoom
		midjRequest.Index = 1
	} else if action == "Inpaint" {
		midjRequest.Action = constant.MjActionInPaint
		midjRequest.Index = 1
	} else {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action:"+customId)
	}
	return nil
}

func ConvertSimpleChangeParams(content string) *dto.MidjourneyRequest {
	split := strings.Split(content, " ")
	if len(split) != 2 {
		return nil
	}

	action := strings.ToLower(split[1])
	changeParams := &dto.MidjourneyRequest{}
	changeParams.TaskId = split[0]

	if action[0] == 'u' {
		changeParams.Action = "UPSCALE"
	} else if action[0] == 'v' {
		changeParams.Action = "VARIATION"
	} else if action == "r" {
		changeParams.Action = "REROLL"
		return changeParams
	} else {
		return nil
	}

	index, err := strconv.Atoi(action[1:2])
	if err != nil || index < 1 || index > 4 {
		return nil
	}
	changeParams.Index = index
	return changeParams
}

func DoMidjourneyHttpRequest(c *gin.Context, timeout time.Duration, fullRequestURL string) (*dto.MidjourneyResponseWithStatusCode, []byte, error) {
	var nullBytes []byte
	//var requestBody io.Reader
	//requestBody = c.Request.Body
	// read request body to json, delete accountFilter and notifyHook
	var mapResult map[string]interface{}
	// if get request, no need to read request body
	if c.Request.Method != "GET" {
		err := common.DecodeJson(c.Request.Body, &mapResult)
		if err != nil {
			return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_request_body_failed", http.StatusInternalServerError), nullBytes, err
		}
		if !setting.MjAccountFilterEnabled {
			delete(mapResult, "accountFilter")
		}
		if !setting.MjNotifyEnabled {
			delete(mapResult, "notifyHook")
		}
		//req, err := http.NewRequest(c.Request.Method, fullRequestURL, requestBody)
		// make new request with mapResult
	}
	if setting.MjModeClearEnabled {
		if prompt, ok := mapResult["prompt"].(string); ok {
			prompt = strings.Replace(prompt, "--fast", "", -1)
			prompt = strings.Replace(prompt, "--relax", "", -1)
			prompt = strings.Replace(prompt, "--turbo", "", -1)

			mapResult["prompt"] = prompt
		}
	}
	reqBody, err := common.Marshal(mapResult)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "marshal_request_body_failed", http.StatusInternalServerError), nullBytes, err
	}
	req, err := http.NewRequest(c.Request.Method, fullRequestURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "create_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// 使用带有超时的 context 创建新的请求
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))
	auth := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if auth != "" {
		auth = strings.TrimPrefix(auth, "Bearer ")
		req.Header.Set("mj-api-secret", auth)
	}
	defer cancel()
	resp, err := httpclient.GetHttpClient().Do(req)
	if err != nil {
		common.SysLog("do request failed: " + err.Error())
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "do_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	statusCode := resp.StatusCode
	//if statusCode != 200  {
	//	return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "bad_response_status_code", statusCode), nullBytes, nil
	//}
	err = req.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	err = c.Request.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	var midjResponse dto.MidjourneyResponse
	var midjourneyUploadsResponse dto.MidjourneyUploadResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_response_body_failed", statusCode), nullBytes, err
	}
	CloseResponseBodyGracefully(resp)
	logger.LogDebug(c, "midjourney response body: %s", responseBody)
	if len(responseBody) == 0 {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "empty_response_body", statusCode), responseBody, nil
	} else {
		err = common.Unmarshal(responseBody, &midjResponse)
		if err != nil {
			err2 := common.Unmarshal(responseBody, &midjourneyUploadsResponse)
			if err2 != nil {
				return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "unmarshal_response_body_failed", statusCode), responseBody, err
			}
		}
	}
	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   midjResponse,
	}, responseBody, nil
}
