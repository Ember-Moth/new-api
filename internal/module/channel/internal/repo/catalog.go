package repo

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	"gorm.io/gorm"
)

// Catalog owns model and vendor metadata on the application's primary database.
type Catalog struct {
	db *gorm.DB
}

const commonGroupCol = `"group"`

func NewCatalog(db *gorm.DB) *Catalog {
	return &Catalog{db: db}
}

func vendorResult(record entity.Vendor) *contract.Vendor {
	return &contract.Vendor{
		Id: record.Id, Name: record.Name, Description: record.Description, Icon: record.Icon,
		Status: record.Status, CreatedTime: record.CreatedTime, UpdatedTime: record.UpdatedTime,
	}
}

func (r *Catalog) Vendors(ctx context.Context, keyword string, offset, limit int, search bool) ([]*contract.Vendor, int64, error) {
	query := r.db.WithContext(ctx).Model(&entity.Vendor{})
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	var total int64
	if limit >= 0 {
		if err := query.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	if search {
		query = query.Order("id DESC")
	}
	var records []entity.Vendor
	if err := query.Offset(offset).Limit(limit).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	result := make([]*contract.Vendor, 0, len(records))
	for _, record := range records {
		result = append(result, vendorResult(record))
	}
	return result, total, nil
}

func (r *Catalog) VendorsByIDs(ctx context.Context, ids []int) ([]*contract.Vendor, error) {
	var records []entity.Vendor
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]*contract.Vendor, 0, len(records))
	for _, record := range records {
		result = append(result, vendorResult(record))
	}
	return result, nil
}

func (r *Catalog) Vendor(ctx context.Context, id int) (*contract.Vendor, error) {
	var record entity.Vendor
	if err := r.db.WithContext(ctx).First(&record, id).Error; err != nil {
		return nil, err
	}
	return vendorResult(record), nil
}

func (r *Catalog) VendorByName(ctx context.Context, name string) (*contract.Vendor, error) {
	var record entity.Vendor
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&record).Error; err != nil {
		return nil, err
	}
	return vendorResult(record), nil
}

func (r *Catalog) VendorNameExists(ctx context.Context, id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.Vendor{}).Where("name = ? AND id <> ?", name, id).Count(&count).Error
	return count > 0, err
}

func (r *Catalog) SaveVendor(ctx context.Context, vendor *contract.Vendor, create bool) error {
	record := entity.Vendor{
		Id: vendor.Id, Name: vendor.Name, Description: vendor.Description, Icon: vendor.Icon,
		Status: vendor.Status, CreatedTime: vendor.CreatedTime, UpdatedTime: vendor.UpdatedTime,
	}
	var err error
	if create {
		err = r.db.WithContext(ctx).Create(&record).Error
	} else {
		err = r.db.WithContext(ctx).Save(&record).Error
	}
	if err == nil {
		*vendor = *vendorResult(record)
	}
	return err
}

func (r *Catalog) DeleteVendor(ctx context.Context, id int) error {
	return r.db.WithContext(ctx).Delete(&entity.Vendor{}, id).Error
}
