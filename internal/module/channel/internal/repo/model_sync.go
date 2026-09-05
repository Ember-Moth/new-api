package repo

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	"gorm.io/gorm"
)

func (r *Catalog) ModelByName(ctx context.Context, name string) (*contract.Model, error) {
	var record entity.Model
	if err := r.db.WithContext(ctx).Where("model_name = ?", name).First(&record).Error; err != nil {
		return nil, err
	}
	return ModelResult(&record), nil
}

func (r *Catalog) OfficialModelsByNames(ctx context.Context, names []string) ([]*contract.Model, error) {
	var records []entity.Model
	if err := r.db.WithContext(ctx).Where("model_name IN ? AND sync_official <> 0", names).Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]*contract.Model, 0, len(records))
	for i := range records {
		result = append(result, ModelResult(&records[i]))
	}
	return result, nil
}

func (r *Catalog) SaveSyncedModel(ctx context.Context, model *contract.Model) error {
	record := entity.Model{
		Id: model.Id, ModelName: model.ModelName, Description: model.Description,
		Icon: model.Icon, Tags: model.Tags, VendorID: model.VendorID, Endpoints: model.Endpoints,
		Status: model.Status, SyncOfficial: model.SyncOfficial, CreatedTime: model.CreatedTime,
		UpdatedTime: model.UpdatedTime, NameRule: model.NameRule,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Save(&record).Error
	})
}

func (r *Catalog) MissingModels(ctx context.Context) ([]string, error) {
	var models []string
	r.db.WithContext(ctx).Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models)
	if len(models) == 0 {
		return []string{}, nil
	}
	var existing []string
	if err := r.db.WithContext(ctx).Model(&entity.Model{}).Where("model_name IN ?", models).Pluck("model_name", &existing).Error; err != nil {
		return nil, err
	}
	existingSet := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		existingSet[name] = struct{}{}
	}
	var missing []string
	for _, name := range models {
		if _, ok := existingSet[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing, nil
}
