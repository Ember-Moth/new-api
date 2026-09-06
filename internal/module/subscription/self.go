package subscription

import (
	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
)

func (s *Service) SelfSubscriptions(ctx context.Context, userID int) (*contract.SelfSubscriptions, error) {
	preference, err := s.billingPreference(ctx, userID)
	if err != nil {
		return nil, err
	}
	all, err := s.Members.GetAllUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}
	active := make([]contract.SubscriptionSummary, 0, len(all))
	now := common.GetTimestamp()
	for _, entry := range all {
		if entry.Subscription.Status == "active" && entry.Subscription.EndTime > now {
			active = append(active, entry)
		}
	}
	return &contract.SelfSubscriptions{BillingPreference: preference, Subscriptions: active, AllSubscriptions: all}, nil
}
