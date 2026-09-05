package app

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

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

func tokenPolicy() identity.TokenPolicy {
	return identity.TokenPolicy{
		MaxTokens:         operation_setting.GetMaxUserTokens,
		MaxAutoGroups:     setting.GetMaxTokenAutoGroups,
		UserGroup:         func(ctx context.Context, userID int) (string, error) { return model.GetUserGroup(userID, false) },
		IsSelectableGroup: service.IsUserSelectableGroup,
		AutoGroups:        service.GetUserAutoGroup,
	}
}
