package identity

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
)

func providerResponse(p *entity.CustomOAuthProvider) *contract.CustomOAuthProviderResponse {
	return &contract.CustomOAuthProviderResponse{
		Id:                    p.Id,
		Name:                  p.Name,
		Slug:                  p.Slug,
		Icon:                  p.Icon,
		Enabled:               p.Enabled,
		ClientId:              p.ClientId,
		AuthorizationEndpoint: p.AuthorizationEndpoint,
		TokenEndpoint:         p.TokenEndpoint,
		UserInfoEndpoint:      p.UserInfoEndpoint,
		Scopes:                p.Scopes,
		UserIdField:           p.UserIdField,
		UsernameField:         p.UsernameField,
		DisplayNameField:      p.DisplayNameField,
		EmailField:            p.EmailField,
		WellKnown:             p.WellKnown,
		AuthStyle:             p.AuthStyle,
		AccessPolicy:          p.AccessPolicy,
		AccessDeniedMessage:   p.AccessDeniedMessage,
	}
}

func (s *Service) ListProviders(ctx context.Context) ([]*contract.CustomOAuthProviderResponse, error) {
	providers, err := s.providers.All(ctx, false)
	if err != nil {
		return nil, err
	}
	result := make([]*contract.CustomOAuthProviderResponse, 0, len(providers))
	for _, provider := range providers {
		result = append(result, providerResponse(provider))
	}
	return result, nil
}

func (s *Service) GetProvider(ctx context.Context, id int) (*contract.CustomOAuthProviderResponse, error) {
	provider, err := s.providers.Get(ctx, id)
	if err != nil {
		return nil, errors.New("未找到该 OAuth 提供商")
	}
	return providerResponse(provider), nil
}

func (s *Service) validateProviderMutation(ctx context.Context, provider *entity.CustomOAuthProvider) error {
	if s.registry == nil {
		return errors.New("OAuth provider registry is not configured")
	}
	if err := validateCustomOAuthProvider(provider); err != nil {
		return err
	}
	taken, err := s.providers.SlugTaken(ctx, provider.Slug, provider.Id)
	if err != nil || taken {
		return errors.New("该 Slug 已被使用")
	}
	if s.registry.IsBuiltin(provider.Slug) {
		return errors.New("该 Slug 与内置 OAuth 提供商冲突")
	}
	return nil
}

func (s *Service) CreateProvider(ctx context.Context, req contract.CreateCustomOAuthProviderRequest) (*contract.CustomOAuthProviderResponse, error) {
	provider := &entity.CustomOAuthProvider{
		Name:                  req.Name,
		Slug:                  req.Slug,
		Icon:                  req.Icon,
		Enabled:               req.Enabled,
		ClientId:              req.ClientId,
		ClientSecret:          req.ClientSecret,
		AuthorizationEndpoint: req.AuthorizationEndpoint,
		TokenEndpoint:         req.TokenEndpoint,
		UserInfoEndpoint:      req.UserInfoEndpoint,
		Scopes:                req.Scopes,
		UserIdField:           req.UserIdField,
		UsernameField:         req.UsernameField,
		DisplayNameField:      req.DisplayNameField,
		EmailField:            req.EmailField,
		WellKnown:             req.WellKnown,
		AuthStyle:             req.AuthStyle,
		AccessPolicy:          req.AccessPolicy,
		AccessDeniedMessage:   req.AccessDeniedMessage,
	}

	if err := s.validateProviderMutation(ctx, provider); err != nil {
		return nil, err
	}
	if err := s.providers.Save(ctx, provider, true); err != nil {
		return nil, err
	}
	s.registry.Register(provider)
	return providerResponse(provider), nil
}

func (s *Service) UpdateProvider(ctx context.Context, id int, req contract.UpdateCustomOAuthProviderRequest) (*contract.CustomOAuthProviderResponse, error) {
	provider, err := s.providers.Get(ctx, id)
	if err != nil {
		return nil, errors.New("未找到该 OAuth 提供商")
	}
	oldSlug := provider.Slug
	// Update fields
	if req.Name != "" {
		provider.Name = req.Name
	}
	if req.Slug != "" {
		provider.Slug = req.Slug
	}
	if req.Icon != nil {
		provider.Icon = *req.Icon
	}
	if req.Enabled != nil {
		provider.Enabled = *req.Enabled
	}
	if req.ClientId != "" {
		provider.ClientId = req.ClientId
	}
	if req.ClientSecret != "" {
		provider.ClientSecret = req.ClientSecret
	}
	if req.AuthorizationEndpoint != "" {
		provider.AuthorizationEndpoint = req.AuthorizationEndpoint
	}
	if req.TokenEndpoint != "" {
		provider.TokenEndpoint = req.TokenEndpoint
	}
	if req.UserInfoEndpoint != "" {
		provider.UserInfoEndpoint = req.UserInfoEndpoint
	}
	if req.Scopes != "" {
		provider.Scopes = req.Scopes
	}
	if req.UserIdField != "" {
		provider.UserIdField = req.UserIdField
	}
	if req.UsernameField != "" {
		provider.UsernameField = req.UsernameField
	}
	if req.DisplayNameField != "" {
		provider.DisplayNameField = req.DisplayNameField
	}
	if req.EmailField != "" {
		provider.EmailField = req.EmailField
	}
	if req.WellKnown != nil {
		provider.WellKnown = *req.WellKnown
	}
	if req.AuthStyle != nil {
		provider.AuthStyle = *req.AuthStyle
	}
	if req.AccessPolicy != nil {
		provider.AccessPolicy = *req.AccessPolicy
	}
	if req.AccessDeniedMessage != nil {
		provider.AccessDeniedMessage = *req.AccessDeniedMessage
	}

	if err := s.validateProviderMutation(ctx, provider); err != nil {
		return nil, err
	}
	if err := s.providers.Save(ctx, provider, false); err != nil {
		return nil, err
	}
	if oldSlug != provider.Slug {
		s.registry.Unregister(oldSlug)
	}
	s.registry.Register(provider)
	return providerResponse(provider), nil
}

func (s *Service) DeleteProvider(ctx context.Context, id int) error {
	if s.registry == nil {
		return errors.New("OAuth provider registry is not configured")
	}
	provider, err := s.providers.Get(ctx, id)
	if err != nil {
		return errors.New("未找到该 OAuth 提供商")
	}
	count, err := s.providers.BindingCount(ctx, id)
	if err != nil {
		return errors.New("检查用户绑定时发生错误，请稍后重试")
	}
	if count > 0 {
		return errors.New("该 OAuth 提供商还有用户绑定，无法删除。请先解除所有用户绑定。")
	}
	if err := s.providers.Delete(ctx, id); err != nil {
		return err
	}
	s.registry.Unregister(provider.Slug)
	return nil
}
