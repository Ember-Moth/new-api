package app

import (
	"fmt"

	"github.com/QuantumNous/new-api/logger"

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

func userSecurity() identity.UserSecurity {
	return identity.UserSecurity{
		AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		AdvanceVersion:        model.IncrementUserAuthVersionWithTx,
		PublishAuth:           model.PublishUserAuthCache,
		PublishDeletedVersion: model.PublishCommittedUserAuthVersion,
		RevokeSessions:        func(id int, reason string) error { _, err := model.RevokeAllUserSessions(id, reason); return err },
		InvalidateUser:        model.InvalidateUserCache,
		InvalidateTokens:      model.InvalidateUserTokensCache,
		DeleteCredentials:     model.DeleteUserAuthenticationData,
	}
}
func recordWelcomeGrant(id, quota int) {
	model.RecordLog(id, model.LogTypeSystem, fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(quota)))
}
