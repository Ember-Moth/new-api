package accounting

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// quotaBatch is immutable while it is awaiting a confirmed database commit.
// Reusing its ID makes retries safe after an uncertain COMMIT response.
type quotaBatch struct {
	ID     string
	Stores []map[int]int
}

type quotaBatchUpdate struct {
	columns string
	query   string
	rows    [][]int64
	args    []any
	users   bool
}

func (s *Store) applyQuotaBatch(ctx context.Context, batch *quotaBatch) error {
	userIDs := make(map[int]struct{})
	for _, kind := range []int{BatchUpdateTypeUserQuota, BatchUpdateTypeUsedQuota, BatchUpdateTypeRequestCount} {
		for id := range batch.Stores[kind] {
			userIDs[id] = struct{}{}
		}
	}
	ids := make([]int, 0, len(userIDs))
	for id := range userIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	users := quotaBatchUpdate{
		columns: "id, quota, used_quota, request_count", users: true,
		query: `UPDATE users AS u SET quota = u.quota + d.quota,
used_quota = u.used_quota + d.used_quota, request_count = u.request_count + d.request_count
FROM d WHERE u.id = d.id AND u.deleted_at IS NULL
AND u.quota::numeric + d.quota BETWEEN ? AND ? RETURNING u.id`,
		args: []any{-common.MaxWalletQuota, common.MaxWalletQuota},
	}
	for _, id := range ids {
		quota, used, requests := batch.Stores[BatchUpdateTypeUserQuota][id], batch.Stores[BatchUpdateTypeUsedQuota][id], batch.Stores[BatchUpdateTypeRequestCount][id]
		if quota != 0 || used != 0 || requests != 0 {
			users.rows = append(users.rows, []int64{int64(id), int64(quota), int64(used), int64(requests)})
		}
	}
	tokens := quotaBatchUpdate{
		columns: "id, quota",
		query: `UPDATE tokens AS t SET remain_quota = t.remain_quota + d.quota,
used_quota = t.used_quota - d.quota, accessed_time = ?
FROM d WHERE t.id = d.id AND t.deleted_at IS NULL RETURNING t.id`,
		args: []any{common.GetTimestamp()},
	}
	channels := quotaBatchUpdate{
		columns: "id, quota",
		query:   `UPDATE channels AS c SET used_quota = c.used_quota + d.quota FROM d WHERE c.id = d.id RETURNING c.id`,
	}
	for _, group := range []struct {
		kind   int
		update *quotaBatchUpdate
	}{{BatchUpdateTypeTokenQuota, &tokens}, {BatchUpdateTypeChannelUsedQuota, &channels}} {
		ids = ids[:0]
		for id, value := range batch.Stores[group.kind] {
			if value != 0 {
				ids = append(ids, id)
			}
		}
		sort.Ints(ids)
		for _, id := range ids {
			group.update.rows = append(group.update.rows, []int64{int64(id), int64(batch.Stores[group.kind][id])})
		}
	}
	if len(users.rows)+len(tokens.rows)+len(channels.rows) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receipt := tx.Exec("INSERT INTO quota_batch_receipts (id) VALUES (?::uuid) ON CONFLICT DO NOTHING", batch.ID)
		if receipt.Error != nil {
			return receipt.Error
		}
		if receipt.RowsAffected == 0 {
			return nil
		}
		for _, update := range []quotaBatchUpdate{users, tokens, channels} {
			// Bound SQL size and the number of bind parameters per statement.
			for offset := 0; offset < len(update.rows); offset += 500 {
				rows := update.rows[offset:min(offset+500, len(update.rows))]
				var values strings.Builder
				args := make([]any, 0, len(rows)*len(rows[0])+len(update.args))
				for index, row := range rows {
					if index > 0 {
						values.WriteByte(',')
					}
					values.WriteByte('(')
					for column, value := range row {
						if column > 0 {
							values.WriteByte(',')
						}
						values.WriteString("?::bigint")
						args = append(args, value)
					}
					values.WriteByte(')')
				}
				args = append(args, update.args...)
				var updated []int
				query := "WITH d(" + update.columns + ") AS (VALUES " + values.String() + ") " + update.query
				if err := tx.Raw(query, args...).Scan(&updated).Error; err != nil {
					return err
				}
				if !update.users || len(updated) == len(rows) {
					continue
				}
				matched := make(map[int]struct{}, len(updated))
				for _, id := range updated {
					matched[id] = struct{}{}
				}
				var missing []int64
				for _, row := range rows {
					if _, ok := matched[int(row[0])]; !ok {
						missing = append(missing, row[0])
					}
				}
				var active int64
				if err := tx.Model(&entity.User{}).Where("id IN ?", missing).Count(&active).Error; err != nil {
					return err
				}
				if active != 0 {
					return fmt.Errorf("batch %s: %w", batch.ID, billingcontract.ErrWalletQuotaLimitExceeded)
				}
			}
		}
		return nil
	})
}
