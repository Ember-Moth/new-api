package channel

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
)

func (s *Service) ListVendors(ctx context.Context, offset, limit int) ([]*contract.Vendor, int64, error) {
	return s.catalog.Vendors(ctx, "", offset, limit, false)
}

func (s *Service) SearchVendors(ctx context.Context, keyword string, offset, limit int) ([]*contract.Vendor, int64, error) {
	return s.catalog.Vendors(ctx, keyword, offset, limit, true)
}

func (s *Service) VendorsByIDs(ctx context.Context, ids []int) ([]*contract.Vendor, error) {
	return s.catalog.VendorsByIDs(ctx, ids)
}

func (s *Service) Vendor(ctx context.Context, id int) (*contract.Vendor, error) {
	return s.catalog.Vendor(ctx, id)
}

func (s *Service) VendorByName(ctx context.Context, name string) (*contract.Vendor, error) {
	return s.catalog.VendorByName(ctx, name)
}

func (s *Service) CreateVendor(ctx context.Context, vendor *contract.Vendor) error {
	if vendor.Name == "" {
		return errors.New("供应商名称不能为空")
	}
	duplicate, err := s.catalog.VendorNameExists(ctx, 0, vendor.Name)
	if err != nil {
		return err
	}
	if duplicate {
		return errors.New("供应商名称已存在")
	}
	now := time.Now().Unix()
	vendor.CreatedTime, vendor.UpdatedTime = now, now
	return s.catalog.SaveVendor(ctx, vendor, true)
}

func (s *Service) UpdateVendor(ctx context.Context, vendor *contract.Vendor) error {
	if vendor.Id == 0 {
		return errors.New("缺少供应商 ID")
	}
	duplicate, err := s.catalog.VendorNameExists(ctx, vendor.Id, vendor.Name)
	if err != nil {
		return err
	}
	if duplicate {
		return errors.New("供应商名称已存在")
	}
	vendor.UpdatedTime = time.Now().Unix()
	return s.catalog.SaveVendor(ctx, vendor, false)
}

func (s *Service) DeleteVendor(ctx context.Context, id int) error {
	return s.catalog.DeleteVendor(ctx, id)
}
