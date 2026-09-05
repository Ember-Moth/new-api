package identity

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"gorm.io/gorm"
)

type ProviderRegistry interface {
	IsBuiltin(slug string) bool
	Register(provider *entity.CustomOAuthProvider)
	Unregister(slug string)
}

type Dependencies struct {
	DB        *gorm.DB
	Providers ProviderRegistry
}

type Service struct {
	providers *repo.Providers
	registry  ProviderRegistry
}

func New(deps Dependencies) *Service {
	return &Service{providers: repo.NewProviders(deps.DB), registry: deps.Providers}
}

// ProviderConfigs is the runtime-facing view; HTTP handlers use the redacted
// configuration projections below and never return client secrets.
func (s *Service) ProviderConfigs(ctx context.Context, enabledOnly bool) ([]*entity.CustomOAuthProvider, error) {
	return s.providers.All(ctx, enabledOnly)
}

func (s *Service) ProviderConfig(ctx context.Context, id int) (*entity.CustomOAuthProvider, error) {
	return s.providers.Get(ctx, id)
}

func (s *Service) ProviderConfigBySlug(ctx context.Context, slug string) (*entity.CustomOAuthProvider, error) {
	return s.providers.BySlug(ctx, slug)
}
