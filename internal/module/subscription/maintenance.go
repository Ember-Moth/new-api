package subscription

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
)

func (s *Service) StartMaintenance(ctx context.Context, master bool) <-chan struct{} {
	s.maintenanceOnce.Do(func() {
		s.maintenanceDone = make(chan struct{})
		if !master {
			close(s.maintenanceDone)
			return
		}
		go func() {
			defer close(s.maintenanceDone)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				if err := s.RunMaintenance(ctx); err != nil && ctx.Err() == nil {
					logger.LogWarn(ctx, fmt.Sprintf("subscription maintenance failed: %v", err))
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
	return s.maintenanceDone
}

func (s *Service) RunMaintenance(ctx context.Context) error {
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
