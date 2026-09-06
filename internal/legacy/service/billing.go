package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	billingsessions "github.com/QuantumNous/new-api/internal/module/billing/sessions"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/legacy/model"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = billingcontract.BillingSourceWallet
	BillingSourceSubscription = billingcontract.BillingSourceSubscription
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
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
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

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
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

	// 回退：无 BillingSession 时使用旧路径
	quotaDelta := actualQuota - relayInfo.FinalPreConsumedQuota
	if quotaDelta != 0 {
		return PostConsumeQuota(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true)
	}
	return nil
}

// BillingSession adapts module snapshots to the gateway's request metadata.
// State transitions and money movement are owned by billing/sessions.
type BillingSession struct {
	mu        sync.Mutex
	session   *billingsessions.Session
	relayInfo *relaycommon.RelayInfo
	ctx       context.Context
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
	session, err := billingEngine().Begin(ctx, input, amount)
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
	err := s.session.Settle(s.ctx, actual)
	s.sync()
	return err
}
func (s *BillingSession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.session.Reserve(s.ctx, target)
	s.sync()
	if err != nil {
		return billingAPIError(err)
	}
	return nil
}
func (s *BillingSession) Refund(c *gin.Context) {
	if err := s.session.Refund(s.ctx); err != nil {
		logger.LogError(s.ctx, "error refunding billing session: "+err.Error())
	}
}
func (s *BillingSession) NeedsRefund() bool        { return s.session.NeedsRefund() }
func (s *BillingSession) GetPreConsumedQuota() int { return s.session.GetPreConsumedQuota() }
