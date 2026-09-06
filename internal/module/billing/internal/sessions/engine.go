package sessions

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/accounting"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	subcontract "github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
	"github.com/QuantumNous/new-api/internal/infra/logger"
)

type Dependencies struct {
	Accounting    *accounting.Store
	Users         *usercache.Store
	Tokens        *tokencache.Store
	Subscriptions *quota.Store
	Memberships   *memberships.Store
	Catalog       *catalog.Store
	TrustQuota    func() int
}
type Engine struct{ deps Dependencies }

func New(deps Dependencies) *Engine { return &Engine{deps: deps} }
func failure(kind contract.BillingFailureKind, err error) error {
	return &contract.BillingFailure{Kind: kind, Cause: err}
}
func insufficient(err error) bool {
	var e *contract.BillingFailure
	return errors.As(err, &e) && e.Kind == contract.BillingInsufficientFunds
}
func validateQuota(value int) error {
	if value < 0 || value > common.MaxQuota {
		return failure(contract.BillingInvalidQuota, fmt.Errorf("billing quota out of range: %d", value))
	}
	return nil
}

func (e *Engine) Begin(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	if err := validateQuota(amount); err != nil {
		return nil, err
	}
	switch common.NormalizeBillingPreference(input.Preference) {
	case "wallet_only":
		return e.wallet(ctx, input, amount)
	case "subscription_only":
		return e.subscription(ctx, input, amount)
	case "wallet_first":
		session, err := e.wallet(ctx, input, amount)
		if insufficient(err) {
			return e.subscription(ctx, input, amount)
		}
		return session, err
	default:
		active, err := e.deps.Memberships.HasActiveUserSubscription(ctx, input.UserID)
		if err != nil {
			return nil, failure(contract.BillingQueryFailure, err)
		}
		if !active {
			return e.wallet(ctx, input, amount)
		}
		session, err := e.subscription(ctx, input, amount)
		if !insufficient(err) {
			return session, err
		}
		overflow, checkErr := e.deps.Memberships.UserActiveSubscriptionsAllowWalletOverflow(ctx, input.UserID)
		if checkErr != nil {
			return nil, failure(contract.BillingQueryFailure, checkErr)
		}
		if overflow {
			return e.wallet(ctx, input, amount)
		}
		return nil, err
	}
}
func (e *Engine) wallet(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	user, err := e.deps.Users.GetUserCache(input.UserID)
	if err != nil {
		return nil, failure(contract.BillingQueryFailure, err)
	}
	if user.Quota <= 0 {
		return nil, failure(contract.BillingInsufficientFunds, fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(user.Quota)))
	}
	if user.Quota < amount {
		return nil, failure(contract.BillingInsufficientFunds, fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(user.Quota), logger.FormatQuota(amount)))
	}
	s := &Session{engine: e, input: input, funding: &walletFunding{ledger: e.deps.Accounting, userID: input.UserID}, userQuota: user.Quota}
	trust := 0
	if e.deps.TrustQuota != nil {
		trust = e.deps.TrustQuota()
	}
	s.trusted = !input.ForcePreConsume && trust > 0 && user.Quota > trust && (input.TokenUnlimited || input.TokenQuota > trust)
	if s.trusted {
		amount = 0
	}
	if err := s.preConsume(ctx, amount); err != nil {
		return nil, err
	}
	return s, nil
}
func (e *Engine) subscription(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	s := &Session{engine: e, input: input, funding: &subscriptionFunding{quota: e.deps.Subscriptions, catalog: e.deps.Catalog, requestID: input.RequestID, userID: input.UserID, modelName: input.ModelName}}
	if err := s.preConsume(ctx, max(1, amount)); err != nil {
		return nil, err
	}
	return s, nil
}
func (e *Engine) reserveToken(ctx context.Context, input contract.BillingRequest, amount int) error {
	if amount == 0 || input.Playground {
		return nil
	}
	reserved, err := e.deps.Accounting.TryReserveTokenQuota(ctx, input.TokenID, input.TokenKey, amount, input.TokenUnlimited)
	if err != nil {
		return failure(contract.BillingInsufficientToken, err)
	}
	if reserved {
		return nil
	}
	remaining := 0
	if token, err := e.deps.Tokens.GetByKey(input.TokenKey, false); err == nil && token != nil {
		remaining = token.RemainQuota
	}
	return failure(contract.BillingInsufficientToken, fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(remaining), logger.FormatQuota(amount)))
}
func (e *Engine) fundingFailure(err error) error {
	if errors.Is(err, errInsufficientWallet) {
		return failure(contract.BillingInsufficientFunds, errors.New("用户额度不足"))
	}
	if errors.Is(err, subcontract.ErrSubscriptionQuotaInsufficient) {
		return failure(contract.BillingInsufficientFunds, fmt.Errorf("订阅额度不足或未配置订阅: %w", err))
	}
	return failure(contract.BillingStorageFailure, err)
}

// ApplyDelta supports per-call and task adjustments that do not have a session.
// Its result records which side committed so callers can avoid refunding it twice.
func (e *Engine) ApplyDelta(ctx context.Context, input contract.BillingRequest, source string, subscriptionID, delta int) (result contract.QuotaAdjustment, err error) {
	if delta < -common.MaxQuota || delta > common.MaxQuota {
		return result, failure(contract.BillingInvalidQuota, fmt.Errorf("billing delta out of range: %d", delta))
	}
	if source == contract.BillingSourceSubscription {
		if subscriptionID <= 0 {
			return result, errors.New("subscription id is missing")
		}
		if err = e.deps.Subscriptions.PostConsumeUserSubscriptionDelta(ctx, subscriptionID, int64(delta)); err != nil {
			return result, err
		}
		result.SubscriptionPostDelta = int64(delta)
	} else {
		if delta > 0 {
			err = e.deps.Accounting.DecreaseUserQuota(ctx, input.UserID, delta, false)
		} else {
			err = e.deps.Accounting.IncreaseUserQuota(ctx, input.UserID, -delta, false)
		}
		if err != nil {
			return result, err
		}
	}
	result.FundingApplied = true
	if !input.Playground {
		if delta > 0 {
			err = e.deps.Accounting.DecreaseTokenQuota(ctx, input.TokenID, input.TokenKey, delta)
		} else {
			err = e.deps.Accounting.IncreaseTokenQuota(ctx, input.TokenID, input.TokenKey, -delta)
		}
		if err != nil {
			return result, err
		}
		result.TokenApplied = true
	}
	return result, nil
}
