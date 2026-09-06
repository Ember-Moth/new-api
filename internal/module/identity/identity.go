package identity

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity/authn"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/ceremony"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/passkeys"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/twofa"
	"github.com/go-redis/redis/v8"
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
	IssueProof            func(contract.AuthIdentity, string, []string) (string, int64, error)
	AdvanceCurrentSession func(contract.AuthIdentity, string) (*contract.AuthBundle, error)
	AdvanceVersion        func(*gorm.DB, int) (int64, error)
	PublishAuth           func(int) error
	PublishDeletedVersion func(int, int64) error
	RevokeSessions        func(int, string) error
	InvalidateUser        func(int) error
	InvalidateTokens      func(int) error
	DeleteCredentials     func(*gorm.DB, int) error
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
	Cache                *redis.Client
	Authentication       *authn.Runtime
	TwoFAEvent           func(int, string)
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
	Authentication    *authn.Runtime
	passkeys          *passkeys.Store
	passkeyFlows      *passkeys.Flows
	factors           *twofa.Store
	twoFAEvent        func(int, string)
	bindings          *repo.Bindings
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
		Authentication: deps.Authentication,
		passkeys:       passkeys.NewStore(deps.DB, deps.UserSecurity.AdvanceVersion, deps.UserSecurity.PublishAuth), passkeyFlows: passkeys.NewFlows(ceremony.NewFlows(deps.DB, deps.Cache)),
		factors: twofa.New(deps.DB, deps.UserSecurity.AdvanceVersion, deps.UserSecurity.PublishAuth), twoFAEvent: deps.TwoFAEvent,
		bindings:    repo.NewBindings(deps.DB),
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
