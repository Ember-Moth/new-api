package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
)

const (
	codexCredentialRefreshThreshold = 24 * time.Hour
	codexCredentialRefreshBatchSize = 200
	codexCredentialRefreshTimeout   = 15 * time.Second
)

func shouldAutoRefreshCodexChannelStatus(status int) bool {
	return status == common.ChannelStatusEnabled || status == common.ChannelStatusAutoDisabled
}

// RunCodexCredentialAutoRefreshOnce performs one credential refresh pass. It
// returns I/O failures so a scheduled system task can persist a failed result
// and retry on the next interval.
func RunCodexCredentialAutoRefreshOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now()

	var refreshed int
	var scanned int
	var refreshErr error

	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var channels []*model.Channel
		err := model.DB.WithContext(ctx).
			Select("id", "name", "key", "status", "channel_info").
			Where("type = ? AND (status = ? OR status = ?)",
				constant.ChannelTypeCodex,
				common.ChannelStatusEnabled,
				common.ChannelStatusAutoDisabled,
			).
			Order("id asc").
			Limit(codexCredentialRefreshBatchSize).
			Offset(offset).
			Find(&channels).Error
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.LogError(ctx, fmt.Sprintf("codex credential auto-refresh: query channels failed: %v", err))
			return fmt.Errorf("query Codex channels: %w", err)
		}
		if len(channels) == 0 {
			break
		}
		offset += codexCredentialRefreshBatchSize

		for _, ch := range channels {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ch == nil {
				continue
			}
			scanned++
			if ch.ChannelInfo.IsMultiKey {
				continue
			}

			rawKey := strings.TrimSpace(ch.Key)
			if rawKey == "" {
				continue
			}

			oauthKey, err := parseCodexOAuthKey(rawKey)
			if err != nil {
				continue
			}

			refreshToken := strings.TrimSpace(oauthKey.RefreshToken)
			if refreshToken == "" {
				continue
			}

			expiredAtRaw := strings.TrimSpace(oauthKey.Expired)
			expiredAt, err := time.Parse(time.RFC3339, expiredAtRaw)
			if err == nil && !expiredAt.IsZero() && expiredAt.Sub(now) > codexCredentialRefreshThreshold {
				continue
			}

			refreshCtx, cancel := context.WithTimeout(ctx, codexCredentialRefreshTimeout)
			newKey, _, err := RefreshCodexChannelCredential(refreshCtx, ch.Id, CodexCredentialRefreshOptions{ResetCaches: false})
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if refreshErr == nil {
					refreshErr = fmt.Errorf("refresh channel_id=%d name=%s: %w", ch.Id, ch.Name, err)
				}
				logger.LogWarn(ctx, fmt.Sprintf("codex credential auto-refresh: channel_id=%d name=%s refresh failed: %v", ch.Id, ch.Name, err))
				continue
			}

			refreshed++
			logger.LogInfo(ctx, fmt.Sprintf("codex credential auto-refresh: channel_id=%d name=%s refreshed, expires_at=%s", ch.Id, ch.Name, newKey.Expired))
		}
	}

	if refreshed > 0 && ctx.Err() == nil {
		if err := model.ChannelService().ReloadChannelCache(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("reload channel cache: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if common.DebugEnabled {
		logger.LogDebug(ctx, "codex credential auto-refresh: scanned=%d refreshed=%d", scanned, refreshed)
	}
	return refreshErr
}
