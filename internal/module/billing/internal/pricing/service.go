package pricing

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
)

type Dependencies struct {
	Channels   *channel.Service
	Users      *usercache.Store
	SaveOption func(context.Context, string, string) error
}
type snapshot struct {
	catalog    contract.PricingSnapshot
	endpoints  map[string][]constant.EndpointType
	groups     map[string][]string
	quotaTypes map[string]int
	builtAt    time.Time
	version    uint64
	plugins    *jsplugin.RoutingGeneration
}
type Service struct {
	deps      Dependencies
	refreshMu sync.Mutex
	state     atomic.Pointer[snapshot]
	version   atomic.Uint64
}

func New(deps Dependencies) *Service { return &Service{deps: deps} }

// Invalidation never waits on channel/routing locks held by the writer.
func (s *Service) Invalidate() { s.version.Add(1) }
func (s *Service) load(ctx context.Context, force bool) (*snapshot, error) {
	current := s.state.Load()
	if err := ctx.Err(); err != nil {
		return current, err
	}
	generation := jsplugin.DefaultRegistry.Generation()
	version := s.version.Load()
	if !force && current != nil && current.version == version && current.plugins == generation && time.Since(current.builtAt) < time.Minute {
		return current, nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	current = s.state.Load()
	version = s.version.Load()
	generation = jsplugin.DefaultRegistry.Generation()
	if !force && current != nil && current.version == version && current.plugins == generation && time.Since(current.builtAt) < time.Minute {
		return current, nil
	}
	next, err := s.build(ctx, generation)
	if err != nil {
		return current, err
	}
	next.builtAt = time.Now()
	next.version = version
	next.plugins = generation
	s.state.Store(next)
	return next, nil
}
func (s *Service) Refresh(ctx context.Context) error { _, err := s.load(ctx, true); return err }

// Snapshot returns an owned copy. A failed refresh returns the last complete
// snapshot alongside its error, so callers choose explicitly how to handle it.
func (s *Service) Snapshot(ctx context.Context) (contract.PricingSnapshot, error) {
	current, err := s.load(ctx, false)
	if current == nil {
		return contract.PricingSnapshot{}, err
	}
	result := current.catalog
	result.Vendors = slices.Clone(result.Vendors)
	result.Endpoints = maps.Clone(result.Endpoints)
	result.Prices = slices.Clone(result.Prices)
	for i := range result.Prices {
		item := &result.Prices[i]
		item.EnableGroup = slices.Clone(item.EnableGroup)
		item.SupportedEndpointTypes = slices.Clone(item.SupportedEndpointTypes)
		for _, field := range []**float64{&item.CacheRatio, &item.CreateCacheRatio, &item.ImageRatio, &item.AudioRatio, &item.AudioCompletionRatio} {
			if *field != nil {
				value := **field
				*field = &value
			}
		}
		item.BillingUsageSchema = maps.Clone(item.BillingUsageSchema)
		for key, field := range item.BillingUsageSchema {
			field.Enum = slices.Clone(field.Enum)
			field.Description = maps.Clone(field.Description)
			item.BillingUsageSchema[key] = field
		}
		item.BillingUsageExamples = slices.Clone(item.BillingUsageExamples)
		for index := range item.BillingUsageExamples {
			item.BillingUsageExamples[index].Facts = maps.Clone(item.BillingUsageExamples[index].Facts)
		}
	}
	return result, err
}
func (s *Service) EndpointTypes(ctx context.Context, name string) ([]constant.EndpointType, error) {
	current, err := s.load(ctx, false)
	if current == nil {
		return []constant.EndpointType{}, err
	}
	return append([]constant.EndpointType{}, current.endpoints[name]...), err
}
func (s *Service) ModelGroups(ctx context.Context, name string) ([]string, error) {
	current, err := s.load(ctx, false)
	if current == nil {
		return []string{}, err
	}
	return append([]string{}, current.groups[name]...), err
}
func (s *Service) QuotaTypes(ctx context.Context, name string) ([]int, error) {
	current, err := s.load(ctx, false)
	if current != nil {
		if value, ok := current.quotaTypes[name]; ok {
			return []int{value}, err
		}
	}
	return []int{}, err
}
