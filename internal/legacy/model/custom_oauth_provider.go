package model

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
)

type CustomOAuthProvider = entity.CustomOAuthProvider

func GetAllCustomOAuthProviders() ([]*CustomOAuthProvider, error) {
	return identity.New(identity.Dependencies{DB: DB}).ProviderConfigs(context.Background(), false)
}
func GetEnabledCustomOAuthProviders() ([]*CustomOAuthProvider, error) {
	return identity.New(identity.Dependencies{DB: DB}).ProviderConfigs(context.Background(), true)
}
func GetCustomOAuthProviderById(id int) (*CustomOAuthProvider, error) {
	return identity.New(identity.Dependencies{DB: DB}).ProviderConfig(context.Background(), id)
}
func GetCustomOAuthProviderBySlug(slug string) (*CustomOAuthProvider, error) {
	return identity.New(identity.Dependencies{DB: DB}).ProviderConfigBySlug(context.Background(), slug)
}
