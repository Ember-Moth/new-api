package subscription

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/shared/common"
)

func (s *Service) RunMaintenance(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	totalExpired, totalReset := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := s.Members.ExpireDueSubscriptions(ctx, 300)
		if err != nil {
			return err
		}
		totalExpired += count
		if count < 300 {
			break
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := s.Quota.ResetDueSubscriptions(ctx, 300)
		if err != nil {
			return err
		}
		totalReset += count
		if count < 300 {
			break
		}
	}
	if time.Since(s.lastCleanup) >= 30*time.Minute {
		if _, err := s.Quota.CleanupSubscriptionPreConsumeRecords(ctx, 7*24*3600); err != nil {
			return err
		}
		s.lastCleanup = time.Now()
	}
	if common.DebugEnabled && (totalExpired > 0 || totalReset > 0) {
		logger.LogDebug(ctx, "subscription maintenance: reset_count=%d, expired_count=%d", totalReset, totalExpired)
	}
	return nil
}
