package memberships

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/subscription/internal/dbtime"

	"gorm.io/gorm/clause"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func (s *Store) ExpireDueSubscriptions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := dbtime.Timestamp(s.db.WithContext(ctx))
	var subs []UserSubscription
	if err := s.db.WithContext(ctx).Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("end_time asc, id asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	expiredCount := 0
	userIds := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.UserId > 0 {
			userIds[sub.UserId] = struct{}{}
		}
	}
	for userId := range userIds {
		cacheGroup := ""
		expiredForUser := 0
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := s.groups.Lock(tx, userId); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var expired []UserSubscription
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now).Order("end_time asc, id asc").Find(&expired).Error; err != nil {
				return err
			}
			if len(expired) == 0 {
				return nil
			}
			ids := make([]int, 0, len(expired))
			for _, sub := range expired {
				ids = append(ids, sub.Id)
			}
			res := tx.Model(&UserSubscription{}).
				Where("id IN ?", ids).
				Updates(map[string]interface{}{
					"status":     "expired",
					"updated_at": common.GetTimestamp(),
				})
			if res.Error != nil {
				return res.Error
			}
			expiredForUser = int(res.RowsAffected)

			// If there's an active upgraded subscription, keep current group.
			var activeSub UserSubscription
			activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
				userId, "active", now).
				Order("end_time desc, id desc").
				Limit(1).
				Find(&activeSub)
			if activeQuery.Error != nil {
				return activeQuery.Error
			}
			if activeQuery.RowsAffected > 0 {
				return nil
			}

			// Find the most recently expired subscription that defines a group transition
			// (an explicit downgrade target or an upgrade snapshot to revert).
			var lastExpired UserSubscription
			expiredQuery := tx.Where("user_id = ? AND status = ? AND (downgrade_group <> '' OR upgrade_group <> '')",
				userId, "expired").
				Order("end_time desc, id desc").
				Limit(1).
				Find(&lastExpired)
			if expiredQuery.Error != nil {
				return expiredQuery.Error
			}
			if expiredQuery.RowsAffected == 0 {
				return nil
			}
			currentGroup, err := s.groups.Lock(tx, userId)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			// An explicit downgrade group takes precedence; otherwise revert to the
			// group held before purchase (legacy behavior, only when the subscription
			// actually elevated the user).
			target := strings.TrimSpace(lastExpired.DowngradeGroup)
			if target == "" {
				upgradeGroup := strings.TrimSpace(lastExpired.UpgradeGroup)
				prevGroup := strings.TrimSpace(lastExpired.PrevUserGroup)
				if upgradeGroup == "" || prevGroup == "" {
					return nil
				}
				if currentGroup != upgradeGroup {
					return nil
				}
				target = prevGroup
			}
			if target == "" || target == currentGroup {
				return nil
			}
			if err := s.groups.Set(tx, userId, target); err != nil {
				return err
			}
			cacheGroup = target
			return nil
		})
		if err != nil {
			return expiredCount, err
		}
		expiredCount += expiredForUser
		if cacheGroup != "" {
			s.RefreshUserGroupCache(userId, "subscription expiration")
		}
	}
	return expiredCount, nil
}
