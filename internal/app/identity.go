package app

import (
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/oauth"
)

type providerRegistry struct{}

func (providerRegistry) IsBuiltin(slug string) bool {
	return oauth.IsProviderRegistered(slug) && !oauth.IsCustomProvider(slug)
}

func (providerRegistry) Register(provider *entity.CustomOAuthProvider) {
	oauth.RegisterOrUpdateCustomProvider(provider)
}

func (providerRegistry) Unregister(slug string) { oauth.UnregisterCustomProvider(slug) }
