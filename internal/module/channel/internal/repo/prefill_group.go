package repo

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	"gorm.io/gorm"
)

type PrefillGroups struct {
	db *gorm.DB
}

func NewPrefillGroups(db *gorm.DB) *PrefillGroups {
	return &PrefillGroups{db: db}
}

func (r *PrefillGroups) List(ctx context.Context, groupType string) ([]*contract.PrefillGroup, error) {
	query := r.db.WithContext(ctx).Model(&entity.PrefillGroup{})
	if groupType != "" {
		query = query.Where("type = ?", groupType)
	}
	var records []entity.PrefillGroup
	if err := query.Order("updated_time DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	groups := make([]*contract.PrefillGroup, 0, len(records))
	for _, record := range records {
		groups = append(groups, &contract.PrefillGroup{
			Id: record.Id, Name: record.Name, Type: record.Type, Items: []byte(record.Items),
			Description: record.Description, CreatedTime: record.CreatedTime, UpdatedTime: record.UpdatedTime,
		})
	}
	return groups, nil
}

func (r *PrefillGroups) NameExists(ctx context.Context, id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.PrefillGroup{}).Where("name = ? AND id <> ?", name, id).Count(&count).Error
	return count > 0, err
}

func (r *PrefillGroups) Save(ctx context.Context, group *contract.PrefillGroup, create bool) error {
	record := entity.PrefillGroup{
		Id: group.Id, Name: group.Name, Type: group.Type, Items: entity.JSONValue(group.Items),
		Description: group.Description, CreatedTime: group.CreatedTime, UpdatedTime: group.UpdatedTime,
	}
	query := r.db.WithContext(ctx)
	var err error
	if create {
		err = query.Create(&record).Error
	} else {
		err = query.Save(&record).Error
	}
	if err == nil {
		group.Id = record.Id
	}
	return err
}

func (r *PrefillGroups) Delete(ctx context.Context, id int) error {
	return r.db.WithContext(ctx).Delete(&entity.PrefillGroup{}, id).Error
}
