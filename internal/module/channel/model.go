package channel

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/repo"
)

// CatalogPricing is the read-only pricing projection used by model management,
// plus the invalidation signal emitted after a metadata change commits.
type CatalogPricing interface {
	ModelEndpointTypes(string) []constant.EndpointType
	ModelGroups(string) []string
	ModelQuotaTypes(string) []int
	Models() []contract.ModelPricing
	Refresh()
}

func (s *Service) SearchModels(ctx context.Context, keyword, vendor, status, syncOfficial string, offset, limit int) ([]*contract.Model, int64, error) {
	records, total, err := s.catalog.SearchModels(ctx, keyword, vendor, status, syncOfficial, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	models := make([]*contract.Model, 0, len(records))
	for _, record := range records {
		models = append(models, repo.ModelResult(record))
	}
	s.enrichModels(ctx, models)
	return models, total, nil
}

func (s *Service) AllModelMetadata(ctx context.Context) ([]*contract.Model, error) {
	return s.catalog.AllModels(ctx)
}

func (s *Service) MissingModels(ctx context.Context) ([]string, error) {
	return s.catalog.MissingModels(ctx)
}

func (s *Service) Model(ctx context.Context, id int) (*contract.Model, error) {
	model, err := s.catalog.Model(ctx, id)
	if err != nil {
		return nil, err
	}
	s.enrichModels(ctx, []*contract.Model{model})
	return model, nil
}

func (s *Service) VendorModelCounts(ctx context.Context) (map[int64]int64, error) {
	return s.catalog.GetVendorModelCounts(ctx)
}

func (s *Service) PreferredModelOwnerChannelTypes(ctx context.Context, modelNames, groups []string) (map[string]int, error) {
	return s.catalog.GetPreferredModelOwnerChannelTypes(ctx, modelNames, groups)
}

func (s *Service) CreateModel(ctx context.Context, model *contract.Model) error {
	if model.ModelName == "" {
		return errors.New("模型名称不能为空")
	}
	duplicate, err := s.catalog.ModelNameExists(ctx, 0, model.ModelName)
	if err != nil {
		return err
	}
	if duplicate {
		return errors.New("模型名称已存在")
	}
	now := time.Now().Unix()
	model.CreatedTime, model.UpdatedTime = now, now
	if err := s.catalog.SaveModel(ctx, model, true); err != nil {
		return err
	}
	if s.pricing != nil {
		s.pricing.Refresh()
	}
	return nil
}

func (s *Service) UpdateModel(ctx context.Context, model *contract.Model, statusOnly bool) error {
	if model.Id == 0 {
		return errors.New("缺少模型 ID")
	}
	if statusOnly {
		if err := s.catalog.SetModelStatus(ctx, model.Id, model.Status); err != nil {
			return err
		}
	} else {
		duplicate, err := s.catalog.ModelNameExists(ctx, model.Id, model.ModelName)
		if err != nil {
			return err
		}
		if duplicate {
			return errors.New("模型名称已存在")
		}
		model.UpdatedTime = time.Now().Unix()
		if err := s.catalog.SaveModel(ctx, model, false); err != nil {
			return err
		}
	}
	if s.pricing != nil {
		s.pricing.Refresh()
	}
	return nil
}

func (s *Service) DeleteModel(ctx context.Context, id int) error {
	if err := s.catalog.DeleteModel(ctx, id); err != nil {
		return err
	}
	if s.pricing != nil {
		s.pricing.Refresh()
	}
	return nil
}
