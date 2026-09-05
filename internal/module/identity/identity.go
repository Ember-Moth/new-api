package identity

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"gorm.io/gorm"
)

type ProviderRegistry interface {
	IsBuiltin(slug string) bool
	Register(provider *entity.CustomOAuthProvider)
	Unregister(slug string)
}

type TokenPolicy struct {
	MaxTokens         func() int
	MaxAutoGroups     func() int
	UserGroup         func(context.Context, int) (string, error)
	IsSelectableGroup func(string, string) bool
	AutoGroups        func(string) []string
}

type UserSecurity struct {
	AdvanceCurrentSession  func(contract.AuthIdentity, string) (*contract.AuthBundle, error)
	AdvanceVersion         func(*gorm.DB, int) (int64, error)
	PublishAuth            func(int) error
	PublishDeletedVersion  func(int, int64) error
	RevokeSessions         func(int, string) error
	InvalidateUser         func(int) error
	InvalidateTokens       func(int) error
	DeleteCredentials      func(*gorm.DB, int) error
	ReleaseExternalBinding func(*gorm.DB, string, int) error
}

type UserAuthorization interface {
	Capabilities(int, int) map[string]map[string]bool
	SetUserPermissionsInTx(*gorm.DB, int, map[string]map[string]bool) error
	ClearUserAuthorizationInTx(*gorm.DB, int) error
	ReloadPolicy() error
}

type UserWallet interface {
	AdjustWallet(context.Context, int, string, int) (int, error)
}

type Dependencies struct {
	VerifyEmail          func(string, string) bool
	UserSecurity         UserSecurity
	UserAuthorization    UserAuthorization
	UserWallet           UserWallet
	WelcomeQuota         func() int
	WelcomeGrant         func(int, int)
	TokenPolicy          TokenPolicy
	InvalidateTokenCache func(string) error
	DB                   *gorm.DB
	Providers            ProviderRegistry
}

type Service struct {
	verifyEmail       func(string, string) bool
	users             *repo.Users
	userSecurity      UserSecurity
	userAuthorization UserAuthorization
	userWallet        UserWallet
	welcomeQuota      func() int
	welcomeGrant      func(int, int)
	tokens            *repo.Tokens
	tokenPolicy       TokenPolicy
	providers         *repo.Providers
	registry          ProviderRegistry
}

func New(deps Dependencies) *Service {
	return &Service{
		verifyEmail: deps.VerifyEmail,
		users:       repo.NewUsers(deps.DB), userSecurity: deps.UserSecurity, userAuthorization: deps.UserAuthorization,
		userWallet: deps.UserWallet, welcomeQuota: deps.WelcomeQuota, welcomeGrant: deps.WelcomeGrant,
		tokens:      repo.NewTokens(deps.DB, deps.InvalidateTokenCache),
		tokenPolicy: deps.TokenPolicy,
		providers:   repo.NewProviders(deps.DB),
		registry:    deps.Providers,
	}
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
