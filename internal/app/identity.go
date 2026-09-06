package app

import (
	"fmt"

	identitygroups "github.com/QuantumNous/new-api/internal/module/identity/groups"

	"github.com/QuantumNous/new-api/internal/infra/logger"

	"context"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/config/setting"
	"github.com/QuantumNous/new-api/internal/config/setting/operation_setting"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/legacy/oauth"
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
		IsSelectableGroup: identitygroups.IsUserSelectableGroup,
		AutoGroups:        identitygroups.GetUserAutoGroup,
	}
}

func userSecurity() identity.UserSecurity {
	cache := usercache.New(model.DB)
	return identity.UserSecurity{
		IssueProof:            service.IssueSecurityProof,
		AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		AdvanceVersion:        cache.IncrementUserAuthVersionWithTx,
		PublishAuth:           cache.PublishUserAuthCache,
		PublishDeletedVersion: cache.PublishCommittedUserAuthVersion,
		RevokeSessions:        func(id int, reason string) error { _, err := model.RevokeAllUserSessions(id, reason); return err },
		InvalidateUser:        cache.InvalidateUserCache,
		InvalidateTokens:      model.InvalidateUserTokensCache,
		DeleteCredentials:     model.DeleteUserAuthenticationData,
	}
}
func recordWelcomeGrant(id, quota int) {
	model.RecordLog(id, model.LogTypeSystem, fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(quota)))
}
