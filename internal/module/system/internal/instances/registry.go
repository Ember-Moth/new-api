package instances

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
)

const instanceKeyPrefix = "system:instance:"

// Keep offline nodes visible for a day, then reclaim their ephemeral metadata.
const instanceRetention = 24 * time.Hour

type Registry struct{ cache *redis.Client }

func NewRegistry(cache *redis.Client) *Registry { return &Registry{cache: cache} }

func (r *Registry) UpsertSystemInstance(ctx context.Context, nodeName string, info any, startedAt int64, lastSeenAt int64) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for instance reporting")
	}
	if strings.TrimSpace(nodeName) == "" || len(nodeName) > 128 {
		return errors.New("node name must contain between 1 and 128 bytes")
	}
	infoText := ""
	if info != nil {
		data, err := common.Marshal(info)
		if err != nil {
			return err
		}
		infoText = string(data)
	}
	if lastSeenAt == 0 {
		lastSeenAt = common.GetTimestamp()
	}
	data, err := common.Marshal(entity.SystemInstance{NodeName: nodeName, Info: infoText, StartedAt: startedAt, LastSeenAt: lastSeenAt})
	if err != nil {
		return err
	}
	return r.cache.Set(ctx, instanceKeyPrefix+nodeName, data, instanceRetention).Err()
}

func (r *Registry) ListSystemInstances(ctx context.Context) ([]*entity.SystemInstance, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for instance reporting")
	}
	// SCAN can repeat keys while heartbeats update the database. Deduplicate
	// the snapshot and tolerate keys expiring between SCAN and MGET.
	byNode := make(map[string]*entity.SystemInstance)
	var cursor uint64
	for {
		keys, next, err := r.cache.Scan(ctx, cursor, instanceKeyPrefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			values, err := r.cache.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				if value == nil {
					continue
				}
				text, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("invalid instance metadata type %T", value)
				}
				var row entity.SystemInstance
				if err := common.UnmarshalJsonStr(text, &row); err != nil {
					return nil, err
				}
				byNode[row.NodeName] = &row
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	rows := make([]*entity.SystemInstance, 0, len(byNode))
	for _, row := range byNode {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LastSeenAt == rows[j].LastSeenAt {
			return rows[i].NodeName < rows[j].NodeName
		}
		return rows[i].LastSeenAt > rows[j].LastSeenAt
	})
	return rows, nil
}

func (r *Registry) DeleteStaleSystemInstances(ctx context.Context, now int64) (int64, error) {
	rows, err := r.ListSystemInstances(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, row := range rows {
		if now-row.LastSeenAt <= contract.SystemInstanceStaleAfterSeconds {
			continue
		}
		deleted, err := r.DeleteStaleSystemInstance(ctx, row.NodeName, now)
		if err != nil {
			return count, err
		}
		if deleted {
			count++
		}
	}
	return count, nil
}

// Check freshness and remove the record atomically so a concurrent heartbeat
// cannot be deleted using a stale snapshot from the management list.
var deleteStaleInstance = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
local instance = cjson.decode(value)
if instance.last_seen_at >= tonumber(ARGV[1]) then return 0 end
return redis.call('DEL', KEYS[1])
`)

func (r *Registry) DeleteStaleSystemInstance(ctx context.Context, nodeName string, now int64) (bool, error) {
	if r.cache == nil {
		return false, errors.New("DragonflyDB is required for instance reporting")
	}
	removed, err := deleteStaleInstance.Run(ctx, r.cache, []string{instanceKeyPrefix + nodeName}, now-contract.SystemInstanceStaleAfterSeconds).Int64()
	return removed > 0, err
}
