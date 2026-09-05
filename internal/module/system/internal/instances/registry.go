package instances

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Registry struct{ db *gorm.DB }

func NewRegistry(db *gorm.DB) *Registry { return &Registry{db: db} }

func (r *Registry) UpsertSystemInstance(ctx context.Context, nodeName string, info any, startedAt int64, lastSeenAt int64) error {
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
	instance := &entity.SystemInstance{
		NodeName:   nodeName,
		Info:       infoText,
		StartedAt:  startedAt,
		LastSeenAt: lastSeenAt,
		UpdatedAt:  lastSeenAt,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"info",
			"started_at",
			"last_seen_at",
			"updated_at",
		}),
	}).Create(instance).Error
}

func (r *Registry) ListSystemInstances(ctx context.Context) ([]*entity.SystemInstance, error) {
	var instances []*entity.SystemInstance
	err := r.db.WithContext(ctx).Order("last_seen_at desc").Find(&instances).Error
	return instances, err
}

func (r *Registry) DeleteStaleSystemInstances(ctx context.Context, now int64) (int64, error) {
	result := r.db.WithContext(ctx).Where("last_seen_at < ?", now-contract.SystemInstanceStaleAfterSeconds).Delete(&entity.SystemInstance{})
	return result.RowsAffected, result.Error
}

func (r *Registry) DeleteStaleSystemInstance(ctx context.Context, nodeName string, now int64) (bool, error) {
	result := r.db.WithContext(ctx).Where("node_name = ? AND last_seen_at < ?", nodeName, now-contract.SystemInstanceStaleAfterSeconds).Delete(&entity.SystemInstance{})
	return result.RowsAffected > 0, result.Error
}
