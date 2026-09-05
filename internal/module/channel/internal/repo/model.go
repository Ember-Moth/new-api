package repo

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
)

func ModelResult(record *entity.Model) *contract.Model {
	return &contract.Model{
		Id: record.Id, ModelName: record.ModelName, Description: record.Description,
		Icon: record.Icon, Tags: record.Tags, VendorID: record.VendorID, Endpoints: record.Endpoints,
		Status: record.Status, SyncOfficial: record.SyncOfficial, CreatedTime: record.CreatedTime,
		UpdatedTime: record.UpdatedTime, NameRule: record.NameRule,
	}
}

func (r *Catalog) AllModels(ctx context.Context) ([]*contract.Model, error) {
	var records []entity.Model
	if err := r.db.WithContext(ctx).Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]*contract.Model, 0, len(records))
	for i := range records {
		result = append(result, ModelResult(&records[i]))
	}
	return result, nil
}

func (r *Catalog) Model(ctx context.Context, id int) (*contract.Model, error) {
	var record entity.Model
	if err := r.db.WithContext(ctx).First(&record, id).Error; err != nil {
		return nil, err
	}
	return ModelResult(&record), nil
}

func (r *Catalog) ModelNameExists(ctx context.Context, id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.Model{}).Where("model_name = ? AND id <> ?", name, id).Count(&count).Error
	return count > 0, err
}

func (r *Catalog) SaveModel(ctx context.Context, model *contract.Model, create bool) error {
	record := entity.Model{
		Id: model.Id, ModelName: model.ModelName, Description: model.Description,
		Icon: model.Icon, Tags: model.Tags, VendorID: model.VendorID, Endpoints: model.Endpoints,
		Status: model.Status, SyncOfficial: model.SyncOfficial, CreatedTime: model.CreatedTime,
		UpdatedTime: model.UpdatedTime, NameRule: model.NameRule,
	}
	db := r.db.WithContext(ctx)
	if create {
		status, syncOfficial := record.Status, record.SyncOfficial
		if err := db.Create(&record).Error; err != nil {
			return err
		}
		// Preserve explicit zero values when GORM applies insert defaults.
		model.Id, model.Status, model.SyncOfficial = record.Id, record.Status, record.SyncOfficial
		return db.Model(&entity.Model{}).Where("id = ?", record.Id).Updates(map[string]any{
			"status": status, "sync_official": syncOfficial,
		}).Error
	}
	return db.Model(&entity.Model{}).Where("id = ?", record.Id).
		Select("model_name", "description", "icon", "tags", "vendor_id", "endpoints", "status", "sync_official", "name_rule", "updated_time").
		Updates(&record).Error
}

func (r *Catalog) SetModelStatus(ctx context.Context, id, status int) error {
	return r.db.WithContext(ctx).Model(&entity.Model{}).Where("id = ?", id).Update("status", status).Error
}

func (r *Catalog) DeleteModel(ctx context.Context, id int) error {
	return r.db.WithContext(ctx).Delete(&entity.Model{}, id).Error
}
