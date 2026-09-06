package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/legacy/model"
)

const authArtifactCleanupInterval = time.Hour

// StartAuthArtifactCleanup removes expired dashboard Sessions and old
// expired provider assertion receipts. Only control-plane instances perform cleanup.
func StartAuthArtifactCleanup() {
	if !common.IsControlPlane {
		return
	}
	go func() {
		cleanupAuthArtifacts()
		ticker := time.NewTicker(authArtifactCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			cleanupAuthArtifacts()
		}
	}()
}

func cleanupAuthArtifacts() {
	now := time.Now()
	count, err := model.CountUserSessionsCreatedSince(0, now.Add(-time.Hour).Unix())
	if err != nil {
		common.SysError("failed to count hourly user session issuance: " + err.Error())
	} else if count > int64(common.UserSessionHourlyAlertThreshold) {
		common.SysError(fmt.Sprintf(
			"hourly user session issuance exceeded alert threshold: count=%d threshold=%d window_seconds=%d",
			count,
			common.UserSessionHourlyAlertThreshold,
			int64(time.Hour/time.Second),
		))
	}
	if err := model.DeleteExpiredUserSessions(now.Unix()); err != nil {
		common.SysError("failed to delete expired user sessions: " + err.Error())
	}
	if err := model.DeleteOldRevokedUserSessions(now.Unix()); err != nil {
		common.SysError("failed to delete old revoked user sessions: " + err.Error())
	}
	if err := model.DeleteExpiredAuthAssertionReceipts(now); err != nil {
		common.SysError("failed to delete expired assertion receipts: " + err.Error())
	}

}
