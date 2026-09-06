// Package channel owns channel configuration and reusable routing groups.
package channel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/repo"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/routing"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/upstream"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type Service struct {
	*routing.Runtime
	prefillGroups  *repo.PrefillGroups
	catalog        *repo.Catalog
	pricing        CatalogPricing
	upstream       *upstream.Client
	providers      ProviderRequests
	disable        func(types.ChannelError, string)
	notifyModels   func(string, string)
	upstreamNotify struct {
		sync.Mutex
		lastNotifiedAt      int64
		lastChangedChannels int
		lastFailedChannels  int
	}
}

type Dependencies struct {
	Cache             *redis.Client
	ReadOnly          bool
	DB                *gorm.DB
	Pricing           CatalogPricing
	RoutingChanged    func()
	QueueUsedQuota    func(int, int) bool
	Providers         ProviderRequests
	DisableChannel    func(types.ChannelError, string)
	NotifyModelUpdate func(string, string)
}

func New(deps Dependencies) *Service {
	return &Service{Runtime: routing.New(deps.DB, deps.RoutingChanged, deps.QueueUsedQuota, routing.SnapshotConfig{Cache: deps.Cache, ReadOnly: deps.ReadOnly}), prefillGroups: repo.NewPrefillGroups(deps.DB), catalog: repo.NewCatalog(deps.DB), pricing: deps.Pricing, upstream: upstream.New(), providers: deps.Providers, disable: deps.DisableChannel, notifyModels: deps.NotifyModelUpdate}
}

func (s *Service) ListPrefillGroups(ctx context.Context, groupType string) ([]*contract.PrefillGroup, error) {
	return s.prefillGroups.List(ctx, groupType)
}

func (s *Service) CreatePrefillGroup(ctx context.Context, group *contract.PrefillGroup) error {
	if group.Name == "" || group.Type == "" {
		return errors.New("组名称和类型不能为空")
	}
	duplicate, err := s.prefillGroups.NameExists(ctx, 0, group.Name)
	if err != nil {
		return err
	}
	if duplicate {
		return errors.New("组名称已存在")
	}
	now := time.Now().Unix()
	group.CreatedTime, group.UpdatedTime = now, now
	return s.prefillGroups.Save(ctx, group, true)
}

func (s *Service) UpdatePrefillGroup(ctx context.Context, group *contract.PrefillGroup) error {
	if group.Id == 0 {
		return errors.New("缺少组 ID")
	}
	duplicate, err := s.prefillGroups.NameExists(ctx, group.Id, group.Name)
	if err != nil {
		return err
	}
	if duplicate {
		return errors.New("组名称已存在")
	}
	group.UpdatedTime = time.Now().Unix()
	return s.prefillGroups.Save(ctx, group, false)
}

func (s *Service) DeletePrefillGroup(ctx context.Context, id int) error {
	return s.prefillGroups.Delete(ctx, id)
}
