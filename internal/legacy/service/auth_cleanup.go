package service

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/shared/common"
)

// RunAuthArtifactCleanup performs one authentication artifact maintenance
// pass. It returns I/O failures so the system task runner can persist a failed
// result and retry on the next scheduled interval.
func RunAuthArtifactCleanup(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	now := time.Now()
	count, err := model.CountUserSessionsCreatedSinceWithContext(ctx, 0, now.Add(-time.Hour).Unix())
	if err != nil {
		return fmt.Errorf("count hourly user session issuance: %w", err)
	}
	if count > int64(common.UserSessionHourlyAlertThreshold) {
		common.SysError(fmt.Sprintf(
			"hourly user session issuance exceeded alert threshold: count=%d threshold=%d window_seconds=%d",
			count,
			common.UserSessionHourlyAlertThreshold,
			int64(time.Hour/time.Second),
		))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := model.DeleteExpiredAuthAssertionReceiptsWithContext(ctx, now); err != nil {
		return fmt.Errorf("delete expired assertion receipts: %w", err)
	}
	return nil
}
