package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/logger"
)

// Session serializes a request's reservation, settlement and refund lifecycle.
// The gateway receives value snapshots; core state contains no transport objects.
type Session struct {
	mu                                         sync.Mutex
	engine                                     *Engine
	input                                      contract.BillingRequest
	funding                                    fundingSource
	userQuota, preConsumed, tokenConsumed      int
	postDelta                                  int64
	trusted, fundingSettled, settled, refunded bool
}

func (s *Session) Snapshot() contract.BillingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.funding.State()
	state.UserQuota = s.userQuota
	state.PreConsumedQuota = s.preConsumed
	state.SubscriptionPostDelta = s.postDelta
	return state
}
func (s *Session) GetPreConsumedQuota() int { s.mu.Lock(); defer s.mu.Unlock(); return s.preConsumed }
func (s *Session) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.settled && !s.refunded && !s.fundingSettled && (s.tokenConsumed > 0 || s.funding.Consumed() > 0)
}
func (s *Session) preConsume(ctx context.Context, amount int) error {
	if s.trusted {
		logger.LogInfo(ctx, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.input.UserID, s.funding.Source()))
	} else if amount > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.input.UserID, logger.FormatQuota(amount), s.funding.Source()))
	}
	if err := s.engine.reserveToken(ctx, s.input, amount); err != nil {
		return err
	}
	if !s.input.Playground {
		s.tokenConsumed = amount
	}
	if err := s.funding.PreConsume(ctx, amount); err != nil {
		if s.tokenConsumed > 0 {
			rollbackErr := s.engine.deps.Accounting.IncreaseTokenQuota(context.WithoutCancel(ctx), s.input.TokenID, s.input.TokenKey, s.tokenConsumed)
			if rollbackErr != nil {
				common.SysError(fmt.Sprintf("token rollback failed user_id=%d token_id=%d amount=%d: %v", s.input.UserID, s.input.TokenID, s.tokenConsumed, rollbackErr))
				// A failed rollback cannot be treated as an ordinary quota fallback: doing
				// another pre-consume could debit the token a second time.
				return failure(contract.BillingStorageFailure, errors.Join(err, rollbackErr))
			}
			s.tokenConsumed = 0
		}
		if errors.Is(err, errInsufficientWallet) {
			remaining := 0
			if user, readErr := s.engine.deps.Users.GetUserCache(s.input.UserID); readErr == nil {
				remaining = user.Quota
			}
			return failure(contract.BillingInsufficientFunds, fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(remaining)))
		}
		return s.engine.fundingFailure(err)
	}
	s.preConsumed = amount
	return nil
}

func (s *Session) Reserve(ctx context.Context, target int) error {
	if err := validateQuota(target); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || s.trusted || target <= s.preConsumed {
		return nil
	}
	delta := target - s.preConsumed
	if err := s.funding.Reserve(ctx, delta); err != nil {
		return s.engine.fundingFailure(err)
	}
	if err := s.engine.reserveToken(ctx, s.input, delta); err != nil {
		if rollbackErr := s.funding.Reserve(context.WithoutCancel(ctx), -delta); rollbackErr != nil {
			common.SysError(fmt.Sprintf("funding reserve rollback failed user_id=%d source=%s: %v", s.input.UserID, s.funding.Source(), rollbackErr))
			return failure(contract.BillingStorageFailure, errors.Join(err, rollbackErr))
		}
		return err
	}
	s.preConsumed = target
	if !s.input.Playground {
		s.tokenConsumed += delta
	}
	return nil
}

func (s *Session) Settle(ctx context.Context, actual int) error {
	if err := validateQuota(actual); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refunded {
		return failure(contract.BillingSessionClosed, errors.New("billing session already refunded"))
	}
	if s.settled {
		return nil
	}
	delta := actual - s.preConsumed
	if delta == 0 {
		s.settled = true
		return nil
	}
	if !s.fundingSettled {
		if err := s.funding.Settle(ctx, delta); err != nil {
			return err
		}
		s.fundingSettled = true
	}
	var tokenErr error
	if !s.input.Playground {
		if delta > 0 {
			tokenErr = s.engine.deps.Accounting.DecreaseTokenQuota(ctx, s.input.TokenID, s.input.TokenKey, delta)
		} else {
			tokenErr = s.engine.deps.Accounting.IncreaseTokenQuota(ctx, s.input.TokenID, s.input.TokenKey, -delta)
		}
		if tokenErr != nil {
			common.SysError(fmt.Sprintf("token adjustment failed after funding settlement user_id=%d token_id=%d delta=%d: %v", s.input.UserID, s.input.TokenID, delta, tokenErr))
		}
	}
	if s.funding.Source() == contract.BillingSourceSubscription {
		s.postDelta += int64(delta)
	}
	s.settled = true
	return tokenErr
}

// Refund completes before returning. Non-idempotent wallet/token credits are
// attempted once, while subscription refunds can retry their request receipt.
func (s *Session) Refund(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || s.fundingSettled {
		return nil
	}
	if s.tokenConsumed <= 0 && s.funding.Consumed() <= 0 {
		return nil
	}
	s.refunded = true
	ctx = context.WithoutCancel(ctx)
	logger.LogInfo(ctx, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）", s.input.UserID, logger.FormatQuota(s.tokenConsumed), s.funding.Source()))
	fundingErr := s.funding.Refund(ctx)
	var tokenErr error
	if s.tokenConsumed > 0 {
		tokenErr = s.engine.deps.Accounting.IncreaseTokenQuota(ctx, s.input.TokenID, s.input.TokenKey, s.tokenConsumed)
	}
	return errors.Join(fundingErr, tokenErr)
}
