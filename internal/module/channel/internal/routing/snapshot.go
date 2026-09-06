package routing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/configsync"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type SnapshotConfig struct {
	Cache    *redis.Client
	ReadOnly bool
}
type channelSnapshot struct {
	Channels  []*Channel
	Abilities []Ability
}
type snapshotState struct {
	sync.Mutex
	dirty                atomic.Bool
	store                *configsync.Store
	configured, readOnly bool
	version              string
}

func (r *Runtime) ReloadChannelCache(ctx context.Context) error {
	r.snapshot.Lock()
	defer r.snapshot.Unlock()
	var snapshot channelSnapshot
	version := ""
	if r.snapshot.readOnly {
		published, err := r.snapshot.store.Read(ctx)
		if err != nil {
			return err
		}
		if published.Version == r.snapshot.version && !r.snapshot.dirty.Load() {
			return nil
		}
		if err := common.Unmarshal(published.Data, &snapshot); err != nil {
			return err
		}
		version = published.Version
	} else {
		if r.db == nil {
			return errors.New("database is required to publish channel configuration")
		}
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Serialize publishers before the statement that captures the source.
			// Existing channel mutations need no new locks or dual writes.
			if r.snapshot.configured {
				if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('new-api:channels:' || current_schema(), 0))").Error; err != nil {
					return err
				}
			}
			var rows []struct {
				Channel
				SnapshotAbilities string
			}
			// One MVCC statement captures channels and their abilities together.
			if err := tx.Table("channels").Select(`channels.*, COALESCE((SELECT jsonb_agg(a ORDER BY a."group", a.model) FROM abilities a WHERE a.channel_id=channels.id), '[]'::jsonb)::text AS snapshot_abilities`).Order("channels.id").Scan(&rows).Error; err != nil {
				return err
			}
			snapshot.Channels = make([]*Channel, 0, len(rows))
			for i := range rows {
				snapshot.Channels = append(snapshot.Channels, &rows[i].Channel)
				var abilities []Ability
				if err := common.UnmarshalJsonStr(rows[i].SnapshotAbilities, &abilities); err != nil {
					return err
				}
				snapshot.Abilities = append(snapshot.Abilities, abilities...)
			}
			if r.snapshot.configured {
				payload, err := common.Marshal(snapshot)
				if err != nil {
					return err
				}
				published, err := r.snapshot.store.Publish(ctx, payload)
				if err != nil {
					return err
				}
				version = published.Version
			}
			return nil
		})
		if err != nil {
			return err
		}
		if version != "" && version == r.snapshot.version && !r.snapshot.dirty.Load() {
			return nil
		}
	}
	r.applyChannelSnapshot(snapshot)
	r.snapshot.version = version
	return nil
}
func (r *Runtime) WatchChannelCache(ctx context.Context, frequency int) {
	if r.snapshot.configured || r.snapshot.readOnly {
		r.snapshot.store.Watch(ctx, time.Duration(frequency)*time.Second, r.ReloadChannelCache, func(err error) { common.SysError("channel snapshot synchronization failed: " + err.Error()) })
		return
	}
	if frequency <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(frequency) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.ReloadChannelCache(ctx); err != nil {
				common.SysError(err.Error())
			}
		}
	}
}
func (r *Runtime) ChannelSnapshotVersion() string {
	r.snapshot.Lock()
	defer r.snapshot.Unlock()
	return r.snapshot.version
}
