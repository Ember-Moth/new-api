package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	billingsessions "github.com/QuantumNous/new-api/internal/module/billing/sessions"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	BillingSourceWallet       = billingcontract.BillingSourceWallet
	BillingSourceSubscription = billingcontract.BillingSourceSubscription
	billingOperationTimeout   = 30 * time.Second
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 无预扣的按次/免费请求使用独立的持久化结算操作。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int, recordUsage ...bool) error {
	withUsage := len(recordUsage) == 0 || recordUsage[0]
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后补扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后返还扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费与实际消耗一致，无需调整：%s（按次计费）",
				logger.FormatQuota(actualQuota),
			))
		}

		var settleErr error
		if adapter, ok := relayInfo.Billing.(*BillingSession); ok && withUsage {
			settleErr = adapter.settleWithUsage(actualQuota, relayInfo.ChannelId)
		} else {
			settleErr = relayInfo.Billing.Settle(actualQuota)
		}
		if settleErr != nil {
			return settleErr
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
		return nil
	}

	// Free/per-call requests without a reservation still use a durable operation.
	if relayInfo.FinalPreConsumedQuota != 0 {
		return fmt.Errorf("reservation has no durable billing session")
	}
	adjustment := billingcontract.BillingAdjustment{OperationID: "request:" + relayInfo.RequestId + ":settlement", Source: relayInfo.BillingSource, SubscriptionID: relayInfo.SubscriptionId, Delta: actualQuota}
	if withUsage {
		adjustment.ChannelID, adjustment.UsageDelta, adjustment.RequestDelta = relayInfo.ChannelId, actualQuota, 1
	}
	_, err := applyQuotaAdjustment(relayInfo, adjustment, true)
	return err
}

// BillingSession adapts module snapshots to the gateway's request metadata.
// State transitions and money movement are owned by billing/sessions.
type BillingSession struct {
	mu                  sync.Mutex
	session             *billingsessions.Session
	relayInfo           *relaycommon.RelayInfo
	ctx                 context.Context
	settlementAttempted bool
}

func billingEngine() *billingsessions.Engine {
	return billingsessions.New(billingsessions.Dependencies{Accounting: model.AccountingStore(), Users: usercache.New(model.DB), Tokens: tokencache.New(model.DB), Subscriptions: model.SubscriptionQuota(), Memberships: model.SubscriptionMemberships(), Catalog: model.SubscriptionCatalog(), TrustQuota: common.GetTrustQuota})
}
func billingRequest(info *relaycommon.RelayInfo) billingcontract.BillingRequest {
	return billingcontract.BillingRequest{RequestID: info.RequestId, ModelName: info.GetBillingModelName(), Preference: info.UserSetting.BillingPreference, UserID: info.UserId, TokenID: info.TokenId, TokenKey: info.TokenKey, TokenUnlimited: info.TokenUnlimited, Playground: info.IsPlayground, ForcePreConsume: info.ForcePreConsume}
}
func billingAPIError(err error) *types.NewAPIError {
	if err == nil {
		return nil
	}
	var failure *billingcontract.BillingFailure
	if !errors.As(err, &failure) {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	switch failure.Kind {
	case billingcontract.BillingInvalidQuota:
		return types.NewErrorWithStatusCode(err, types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	case billingcontract.BillingInsufficientFunds:
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	case billingcontract.BillingInsufficientToken:
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	case billingcontract.BillingQueryFailure:
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	default:
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
}
func NewBillingSession(c *gin.Context, info *relaycommon.RelayInfo, amount int) (*BillingSession, *types.NewAPIError) {
	if info == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	ctx := context.Background()
	input := billingRequest(info)
	if c != nil {
		input.TokenQuota = c.GetInt("token_quota")
		if c.Request != nil {
			ctx = c.Request.Context()
		}
	}
	beginCtx, cancel := context.WithTimeout(ctx, billingOperationTimeout)
	defer cancel()
	session, err := billingEngine().Begin(beginCtx, input, amount)
	if err != nil {
		return nil, billingAPIError(err)
	}
	adapter := &BillingSession{session: session, relayInfo: info, ctx: context.WithoutCancel(ctx)}
	adapter.sync()
	return adapter, nil
}
func (s *BillingSession) sync() {
	state := s.session.Snapshot()
	info := s.relayInfo
	info.FinalPreConsumedQuota = state.PreConsumedQuota
	info.BillingSource = state.Source
	if state.Source == BillingSourceWallet {
		info.UserQuota = state.UserQuota
	}
	info.SubscriptionId = state.SubscriptionID
	info.SubscriptionPreConsumed = state.SubscriptionPreConsumed
	info.SubscriptionPostDelta = state.SubscriptionPostDelta
	info.SubscriptionAmountTotal = state.SubscriptionTotal
	info.SubscriptionAmountUsedAfterPreConsume = state.SubscriptionUsed
	info.SubscriptionPlanId = state.PlanID
	info.SubscriptionPlanTitle = state.PlanTitle
}
func (s *BillingSession) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlementAttempted = true
	ctx, cancel := context.WithTimeout(s.ctx, billingOperationTimeout)
	defer cancel()
	err := s.session.Settle(ctx, actual)
	s.sync()
	return err
}
func (s *BillingSession) settleWithUsage(actual, channelID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlementAttempted = true
	ctx, cancel := context.WithTimeout(s.ctx, billingOperationTimeout)
	defer cancel()
	err := s.session.SettleWithUsage(ctx, actual, channelID)
	s.sync()
	return err
}
func (s *BillingSession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, billingOperationTimeout)
	defer cancel()
	err := s.session.Reserve(ctx, target)
	s.sync()
	if err != nil {
		return billingAPIError(err)
	}
	return nil
}
func (s *BillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlementAttempted {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, billingOperationTimeout)
	defer cancel()
	if err := s.session.Refund(ctx); err != nil {
		logger.LogError(s.ctx, "error refunding billing session: "+err.Error())
	}
}
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.settlementAttempted && s.session.NeedsRefund()
}
func (s *BillingSession) GetPreConsumedQuota() int { return s.session.GetPreConsumedQuota() }

// RunBillingRecovery retries only durable settlement/refund intents whose
// upstream outcome is already known. Untouched reservations remain available
// for explicit reconciliation instead of being refunded on a timer.
func RunBillingRecovery(ctx context.Context) error {
	_, sessionErr := billingEngine().RecoverPending(ctx, 500)
	taskErr := RunPendingTaskBilling(ctx)
	return errors.Join(sessionErr, taskErr)
}

// SettleTaskSubmissionBilling commits the initial task charge and clears its
// durable hand-off marker together. A recovery process can repeat this same
// session settlement if the submitting process exits after inserting the task.
func SettleTaskSubmissionBilling(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task) error {
	if info == nil || task == nil || task.ID <= 0 {
		return fmt.Errorf("task submission billing identity is missing")
	}
	var committedTask *model.Task
	commit := func(tx *gorm.DB) error {
		if err := CompleteTaskSubmissionBillingTx(tx, task.ID); err != nil {
			return err
		}
		var err error
		committedTask, err = model.LockTaskForBilling(tx, task.ID)
		return err
	}
	if adapter, ok := info.Billing.(*BillingSession); ok {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		adapter.settlementAttempted = true
		ctx, cancel := context.WithTimeout(adapter.ctx, billingOperationTimeout)
		defer cancel()
		err := adapter.session.SettleWithUsageAndCommit(ctx, task.Quota, task.ChannelId, commit)
		adapter.sync()
		if err == nil && committedTask != nil {
			*task = *committedTask
		}
		return err
	}
	if info.Billing != nil || info.FinalPreConsumedQuota != 0 {
		return fmt.Errorf("task reservation has no durable billing session")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), billingOperationTimeout)
	defer cancel()
	input := billingRequest(info)
	if task.Quota != 0 {
		return fmt.Errorf("paid task has no durable billing reservation")
	}
	err := model.DB.WithContext(ctx).Transaction(commit)
	if err == nil {
		if committedTask != nil {
			*task = *committedTask
		}
		billingEngine().PublishCommitted(input)
	}
	return err
}

// MarkBillingDispatch fences an asynchronous submission before network I/O.
func MarkBillingDispatch(info *relaycommon.RelayInfo) error {
	if info == nil {
		return fmt.Errorf("billing request is missing")
	}
	if adapter, ok := info.Billing.(*BillingSession); ok {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		ctx, cancel := context.WithTimeout(adapter.ctx, billingOperationTimeout)
		defer cancel()
		err := adapter.session.MarkDispatch(ctx, info.ChannelId)
		if err == nil {
			info.TaskSubmissionUncertain = true
			adapter.settlementAttempted = true
		}
		adapter.sync()
		return err
	}
	info.TaskSubmissionUncertain = true
	return nil
}

func ResolveRejectedBillingDispatch(info *relaycommon.RelayInfo) error {
	if info == nil {
		return fmt.Errorf("billing request is missing")
	}
	if adapter, ok := info.Billing.(*BillingSession); ok {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		ctx, cancel := context.WithTimeout(adapter.ctx, billingOperationTimeout)
		defer cancel()
		if err := adapter.session.ResolveRejectedDispatch(ctx); err != nil {
			return err
		}
		adapter.settlementAttempted = false
		adapter.sync()
	}
	info.TaskSubmissionUncertain = false
	return nil
}

// PreserveUncertainBilling is used after network I/O when there is no known
// final outcome. Unlike a failed pre-dispatch fence, failure to write this
// marker must still block an automatic refund in the current request.
func PreserveUncertainBilling(info *relaycommon.RelayInfo) error {
	if info == nil {
		return fmt.Errorf("billing request is missing")
	}
	if adapter, ok := info.Billing.(*BillingSession); ok {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		adapter.settlementAttempted = true
		ctx, cancel := context.WithTimeout(adapter.ctx, billingOperationTimeout)
		defer cancel()
		err := adapter.session.MarkDispatch(ctx, info.GetChannelID())
		adapter.sync()
		return err
	}
	return nil
}
