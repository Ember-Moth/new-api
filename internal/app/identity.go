package app

import (
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service/authz"
	"gorm.io/gorm"

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

type userAuthorization struct{}

func (userAuthorization) Capabilities(id, role int) map[string]map[string]bool {
	return authz.Capabilities(id, role)
}
func (userAuthorization) SetPermissions(tx *gorm.DB, id int, permissions map[string]map[string]bool) error {
	return authz.SetUserPermissionsInTx(tx, id, permissions)
}
func (userAuthorization) ClearPermissions(tx *gorm.DB, id int) error {
	return authz.ClearUserAuthorizationInTx(tx, id)
}
func (userAuthorization) Reload() error { return authz.ReloadPolicy() }

func userSecurity() identity.UserSecurity {
	return identity.UserSecurity{
		AdvanceCurrentSession:  service.AdvanceCurrentSessionToUserVersion,
		AdvanceVersion:         model.IncrementUserAuthVersionWithTx,
		PublishAuth:            model.PublishUserAuthCache,
		PublishDeletedVersion:  model.PublishCommittedUserAuthVersion,
		RevokeSessions:         func(id int, reason string) error { _, err := model.RevokeAllUserSessions(id, reason); return err },
		InvalidateUser:         model.InvalidateUserCache,
		InvalidateTokens:       model.InvalidateUserTokensCache,
		DeleteCredentials:      model.DeleteUserAuthenticationData,
		ReleaseExternalBinding: model.ReleaseExternalIdentityWithTx,
	}
}
func recordWelcomeGrant(id, quota int) {
	model.RecordLog(id, model.LogTypeSystem, fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(quota)))
}
