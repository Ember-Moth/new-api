package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/configsync"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

// TaskPluginSnapshots distributes the desired override set. Compilation and
// atomic routing-generation installation remain owned by the plugin runtime.
type TaskPluginSnapshots struct {
	db       *gorm.DB
	cache    *configsync.Store
	readOnly bool
}

func NewTaskPluginSnapshots(db *gorm.DB, cache *redis.Client, readOnly bool) *TaskPluginSnapshots {
	return &TaskPluginSnapshots{db: db, cache: configsync.New(cache, "task-plugins"), readOnly: readOnly}
}
func (s *TaskPluginSnapshots) Load(ctx context.Context) (TaskPluginSyncSnapshot, error) {
	var snapshot TaskPluginSyncSnapshot
	if s.readOnly {
		published, err := s.cache.Read(ctx)
		if err != nil {
			return snapshot, err
		}
		if err := common.Unmarshal(published.Data, &snapshot); err != nil {
			return snapshot, err
		}
		snapshot.PublishedVersion = published.Version
		return snapshot, nil
	}
	if s.db == nil {
		return snapshot, errors.New("database is required to publish plugin configuration")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('new-api:task-plugins:' || current_schema(), 0))").Error; err != nil {
			return err
		}
		var err error
		snapshot, err = getTaskPluginSyncSnapshot(tx)
		if err != nil {
			return err
		}
		payload, err := common.Marshal(snapshot)
		if err != nil {
			return err
		}
		published, err := s.cache.Publish(ctx, payload)
		if err != nil {
			return err
		}
		snapshot.PublishedVersion = published.Version
		return nil
	})
	return snapshot, err
}
func (s *TaskPluginSnapshots) Watch(ctx context.Context, refresh func(context.Context) error) {
	s.cache.Watch(ctx, 30*time.Second, refresh, func(err error) { common.SysError("task plugin configuration synchronization failed: " + err.Error()) })
}
