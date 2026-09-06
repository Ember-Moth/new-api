package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing/accounting"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
)

var errInsufficientWallet = errors.New("wallet quota insufficient")

type fundingSource interface {
	Source() string
	PreConsume(context.Context, int) error
	Reserve(context.Context, int) error
	Settle(context.Context, int) error
	Refund(context.Context) error
	Consumed() int
	State() contract.BillingState
}

type walletFunding struct {
	ledger           *accounting.Store
	userID, consumed int
}

func (w *walletFunding) Source() string { return contract.BillingSourceWallet }
func (w *walletFunding) Consumed() int  { return w.consumed }
func (w *walletFunding) State() contract.BillingState {
	return contract.BillingState{Source: w.Source()}
}
func (w *walletFunding) PreConsume(ctx context.Context, amount int) error {
	if amount == 0 {
		return nil
	}
	reserved, err := w.ledger.TryReserveUserQuota(ctx, w.userID, amount)
	if err != nil {
		return err
	}
	if !reserved {
		return errInsufficientWallet
	}
	w.consumed = amount
	return nil
}
func (w *walletFunding) Reserve(ctx context.Context, delta int) error {
	// Additional reservation retains settlement's arrears behavior: charge the
	// full difference, even when the wallet balance becomes negative.
	if err := w.ledger.DeltaUpdateUserQuota(ctx, w.userID, -delta); err != nil {
		return err
	}
	w.consumed += delta
	return nil
}
func (w *walletFunding) Settle(ctx context.Context, delta int) error {
	return w.ledger.DeltaUpdateUserQuota(ctx, w.userID, -delta)
}
func (w *walletFunding) Refund(ctx context.Context) error {
	if w.consumed <= 0 {
		return nil
	}
	// Wallet credits are not idempotent. The session invokes this at most once.
	return w.ledger.IncreaseUserQuota(ctx, w.userID, w.consumed, false)
}

type subscriptionFunding struct {
	quota                *quota.Store
	catalog              *catalog.Store
	requestID, modelName string
	userID               int
	state                contract.BillingState
}

func (s *subscriptionFunding) Source() string               { return contract.BillingSourceSubscription }
func (s *subscriptionFunding) Consumed() int                { return int(s.state.SubscriptionPreConsumed) }
func (s *subscriptionFunding) State() contract.BillingState { return s.state }
func (s *subscriptionFunding) PreConsume(ctx context.Context, amount int) error {
	r, err := s.quota.PreConsumeUserSubscription(ctx, s.requestID, s.userID, s.modelName, 0, int64(amount))
	if err != nil {
		return err
	}
	s.state = contract.BillingState{Source: s.Source(), SubscriptionID: r.UserSubscriptionId, SubscriptionPreConsumed: r.PreConsumed, SubscriptionTotal: r.AmountTotal, SubscriptionUsed: r.AmountUsedAfter}
	if plan, err := s.catalog.PlanInfo(ctx, r.UserSubscriptionId); err == nil && plan != nil {
		s.state.PlanID = plan.PlanId
		s.state.PlanTitle = plan.PlanTitle
	}
	return nil
}
func (s *subscriptionFunding) Reserve(ctx context.Context, delta int) error {
	r, err := s.quota.AdjustSubscriptionPreConsume(ctx, s.requestID, int64(delta))
	if err != nil {
		return err
	}
	s.state.SubscriptionPreConsumed = r.PreConsumed
	s.state.SubscriptionUsed = r.AmountUsedAfter
	return nil
}
func (s *subscriptionFunding) Settle(ctx context.Context, delta int) error {
	return s.quota.PostConsumeUserSubscriptionDelta(ctx, s.state.SubscriptionID, int64(delta))
}
func (s *subscriptionFunding) Refund(ctx context.Context) error {
	if s.Consumed() <= 0 {
		return nil
	}
	// Only the request-ID-protected subscription refund is safe to retry.
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.quota.RefundSubscriptionPreConsume(ctx, s.requestID); err == nil {
			return nil
		} else {
			last = err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return last
}
