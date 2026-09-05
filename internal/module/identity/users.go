package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

type UserValidationError struct {
	Code    string
	Details map[string]any
}

func (e *UserValidationError) Error() string { return e.Code }

func CanManageUserRole(actorRole, targetRole int) bool {
	return actorRole == common.RoleRootUser || actorRole > targetRole
}

func (s *Service) ListUsers(ctx context.Context, filter contract.UserFilter) ([]*contract.UserResponse, int64, error) {
	users, total, err := s.users.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	result := make([]*contract.UserResponse, 0, len(users))
	for _, user := range users {
		result = append(result, userResponse(user))
	}
	return result, total, nil
}

func (s *Service) GetUser(ctx context.Context, actor contract.UserActor, id int) (*contract.UserResponse, error) {
	user, err := s.users.Get(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if !CanManageUserRole(actor.Role, user.Role) {
		return nil, &UserValidationError{Code: "same_level"}
	}
	response := userResponse(user)
	response.AdminPermissions = s.userAuthorization.Capabilities(user.Id, user.Role)
	return response, nil
}

func (s *Service) CreateUser(ctx context.Context, actor contract.UserActor, input contract.UserRequest) (*contract.UserMutation, error) {
	input.Username = strings.TrimSpace(input.Username)
	if input.Username == "" || input.Password == "" {
		return nil, &UserValidationError{Code: "invalid_params"}
	}
	if err := common.Validate.Struct(input); err != nil {
		return nil, &UserValidationError{Code: "input_invalid", Details: map[string]any{"Error": err.Error()}}
	}
	if input.Role >= actor.Role {
		return nil, &UserValidationError{Code: "create_higher_level"}
	}
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	password, err := common.Password2Hash(input.Password)
	if err != nil {
		return nil, err
	}
	user := &entity.User{Username: input.Username, Password: password, DisplayName: input.DisplayName, Role: input.Role, AffCode: common.GetRandomString(4)}
	if user.Role == 0 {
		user.Role = common.RoleCommonUser
	}
	if s.welcomeQuota != nil {
		user.Quota = s.welcomeQuota()
	}
	user.SetSetting(dto.UserSetting{SidebarModules: entity.DefaultSidebarConfigForRole(user.Role)})
	touched := false
	err = s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		if err := users.Create(user); err != nil {
			return err
		}
		var err error
		touched, err = s.updateUserPermissions(tx, actor, user.Id, user.Role, input.AdminPermissions)
		return err
	})
	if err != nil {
		return nil, err
	}
	if touched {
		if err := s.userAuthorization.Reload(); err != nil {
			return nil, err
		}
	}
	if s.welcomeGrant != nil && user.Quota > 0 {
		s.welcomeGrant(user.Id, user.Quota)
	}
	return &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.create", Parameters: map[string]any{"username": user.Username, "role": user.Role}}}, nil
}

func (s *Service) UpdateUser(ctx context.Context, actor contract.UserActor, input contract.UserRequest) (*contract.UserMutation, error) {
	input.Username = strings.TrimSpace(input.Username)
	if input.Id == 0 || input.Username == "" {
		return nil, &UserValidationError{Code: "invalid_params"}
	}
	if err := common.Validate.Struct(input); err != nil {
		return nil, &UserValidationError{Code: "input_invalid", Details: map[string]any{"Error": err.Error()}}
	}
	password := ""
	if input.Password != "" {
		var err error
		password, err = common.Password2Hash(input.Password)
		if err != nil {
			return nil, err
		}
	}
	var user *entity.User
	var oldUsername string
	touched, authChanged := false, false
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		var err error
		user, err = users.Lock(input.Id, false)
		if err != nil {
			return err
		}
		oldUsername = user.Username
		if input.Role != common.RoleGuestUser && input.Role != user.Role {
			return &UserValidationError{Code: "invalid_params"}
		}
		if !CanManageUserRole(actor.Role, user.Role) {
			return &UserValidationError{Code: "higher_level"}
		}
		fields := map[string]any{"username": input.Username, "display_name": input.DisplayName, "group": input.Group, "remark": input.Remark}
		if password != "" {
			fields["password"] = password
		}
		authChanged = (password != "" && password != user.Password) || user.Group != input.Group
		if authChanged {
			if _, err := s.userSecurity.AdvanceVersion(tx, user.Id); err != nil {
				return err
			}
		}
		if err := users.Update(user, fields); err != nil {
			return err
		}
		touched, err = s.updateUserPermissions(tx, actor, user.Id, user.Role, input.AdminPermissions)
		return err
	})
	if err != nil {
		return nil, err
	}
	if touched {
		if err := s.userAuthorization.Reload(); err != nil {
			return nil, err
		}
	}
	if authChanged {
		if err := s.userSecurity.RevokeSessions(user.Id, "admin_user_update"); err != nil {
			return nil, err
		}
	}
	if err := s.userSecurity.PublishAuth(user.Id); err != nil {
		return nil, err
	}
	return &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.update", Parameters: map[string]any{"username": oldUsername, "id": user.Id}}}, nil
}

func (s *Service) ClearUserBinding(ctx context.Context, actor contract.UserActor, id int, binding string) (*contract.UserMutation, error) {
	binding = strings.ToLower(strings.TrimSpace(binding))
	columns := map[string]string{"email": "email", "github": "github_id", "discord": "discord_id", "oidc": "oidc_id", "wechat": "wechat_id", "telegram": "telegram_id", "linuxdo": "linux_do_id"}
	column, ok := columns[binding]
	if !ok {
		return nil, errors.New("invalid binding type")
	}
	var user *entity.User
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		var err error
		user, err = users.Lock(id, false)
		if err != nil {
			return err
		}
		if !CanManageUserRole(actor.Role, user.Role) {
			return &UserValidationError{Code: "same_level"}
		}
		if err := users.Update(user, map[string]any{column: ""}); err != nil {
			return err
		}
		if binding == "telegram" {
			return s.userSecurity.ReleaseExternalBinding(tx, binding, user.Id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.userSecurity.PublishAuth(user.Id); err != nil {
		return nil, err
	}
	return &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.binding_clear", Parameters: map[string]any{"bindingType": binding, "username": user.Username}}}, nil
}

func (s *Service) DeleteUser(ctx context.Context, actor contract.UserActor, id int) (*contract.UserMutation, error) {
	var user *entity.User
	var version int64
	var keys []string
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		var err error
		user, err = users.Lock(id, false)
		if err != nil {
			return err
		}
		if actor.Role <= user.Role {
			return &UserValidationError{Code: "higher_level"}
		}
		version, err = s.userSecurity.AdvanceVersion(tx, user.Id)
		if err != nil {
			return err
		}
		keys, err = users.TokenKeys(user.Id)
		if err != nil {
			return err
		}
		if err := s.userSecurity.DeleteCredentials(tx, user.Id); err != nil {
			return err
		}
		return users.Delete(user, true)
	})
	if err != nil {
		return nil, err
	}
	if err := s.userSecurity.PublishDeletedVersion(user.Id, version); err != nil {
		common.SysError(fmt.Sprintf("failed to publish auth tombstone after hard deleting user %d: %v", user.Id, err))
	}
	for _, key := range keys {
		s.tokens.InvalidateKey(key)
	}
	if err := s.userSecurity.InvalidateUser(user.Id); err != nil {
		common.SysError(fmt.Sprintf("failed to invalidate user cache after hard deleting user %d: %v", user.Id, err))
	}
	return &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.delete", Parameters: map[string]any{"username": user.Username, "id": user.Id}}}, nil
}

func (s *Service) ManageUser(ctx context.Context, actor contract.UserActor, input contract.ManageUserRequest) (*contract.UserMutation, error) {
	if input.Id <= 0 {
		return nil, &UserValidationError{Code: "not_exists"}
	}
	if input.Action == "add_quota" {
		user, err := s.users.Get(ctx, input.Id, true)
		if err != nil {
			return nil, err
		}
		if !CanManageUserRole(actor.Role, user.Role) {
			return nil, &UserValidationError{Code: "higher_level"}
		}
		if input.Mode != "add" && input.Mode != "subtract" && input.Mode != "override" {
			return nil, &UserValidationError{Code: "invalid_params"}
		}
		if input.Mode != "override" && input.Value <= 0 {
			return nil, &UserValidationError{Code: "quota_change_zero"}
		}
		before, err := s.userWallet.AdjustWallet(ctx, user.Id, input.Mode, input.Value)
		if err != nil {
			return nil, err
		}
		params := map[string]any{"quota": logger.LogQuota(input.Value)}
		if input.Mode == "override" {
			params = map[string]any{"from": logger.LogQuota(before), "to": logger.LogQuota(input.Value)}
		}
		return &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.quota_" + input.Mode, Parameters: params}}, nil
	}
	var user *entity.User
	var version int64
	changed := false
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		var err error
		user, err = users.Lock(input.Id, true)
		if err != nil {
			return err
		}
		if !CanManageUserRole(actor.Role, user.Role) {
			return &UserValidationError{Code: "higher_level"}
		}
		fields := map[string]any{}
		switch input.Action {
		case "disable":
			if user.Role == common.RoleRootUser {
				return &UserValidationError{Code: "disable_root"}
			}
			fields["status"] = common.UserStatusDisabled
			changed = user.Status != common.UserStatusDisabled
		case "enable":
			fields["status"] = common.UserStatusEnabled
			changed = user.Status != common.UserStatusEnabled
		case "delete":
			if user.Role == common.RoleRootUser {
				return &UserValidationError{Code: "delete_root"}
			}
			version, err = s.userSecurity.AdvanceVersion(tx, user.Id)
			if err != nil {
				return err
			}
			return users.Delete(user, false)
		case "promote":
			if actor.Role != common.RoleRootUser {
				return &UserValidationError{Code: "cannot_promote"}
			}
			if user.Role >= common.RoleAdminUser {
				return &UserValidationError{Code: "already_admin"}
			}
			fields["role"] = common.RoleAdminUser
			changed = true
		case "demote":
			if user.Role == common.RoleRootUser {
				return &UserValidationError{Code: "demote_root"}
			}
			if user.Role == common.RoleCommonUser {
				return &UserValidationError{Code: "already_common"}
			}
			fields["role"] = common.RoleCommonUser
			changed = true
		default:
			return &UserValidationError{Code: "invalid_params"}
		}
		if changed {
			if _, err := s.userSecurity.AdvanceVersion(tx, user.Id); err != nil {
				return err
			}
		}
		if err := users.Update(user, fields); err != nil {
			return err
		}
		if input.Action == "demote" {
			return s.userAuthorization.ClearPermissions(tx, user.Id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if input.Action == "delete" {
		if err := s.userSecurity.PublishDeletedVersion(user.Id, version); err != nil {
			return nil, err
		}
		if err := s.userSecurity.RevokeSessions(user.Id, "user_deleted"); err != nil {
			return nil, err
		}
		if err := s.userSecurity.InvalidateUser(user.Id); err != nil {
			return nil, err
		}
	} else {
		if input.Action == "demote" {
			if err := s.userAuthorization.Reload(); err != nil {
				return nil, err
			}
		}
		if err := s.userSecurity.PublishAuth(user.Id); err != nil {
			return nil, err
		}
		if changed {
			reason := "user_security_changed"
			if input.Action == "demote" {
				reason = "admin_demote"
			}
			if err := s.userSecurity.RevokeSessions(user.Id, reason); err != nil {
				return nil, err
			}
		}
	}
	if err := s.userSecurity.InvalidateTokens(user.Id); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate tokens cache for user %d: %s", user.Id, err))
	}
	result := &contract.UserMutation{Audit: contract.UserAudit{TargetID: user.Id, Action: "user.manage", Parameters: map[string]any{"action": input.Action, "username": user.Username, "id": user.Id}}}
	if input.Action != "delete" {
		result.Data = userResponse(&entity.User{Role: user.Role, Status: user.Status})
	}
	return result, nil
}

func (s *Service) updateUserPermissions(tx *gorm.DB, actor contract.UserActor, id, role int, permissions map[string]map[string]bool) (bool, error) {
	if permissions == nil {
		if role < common.RoleAdminUser && actor.Role == common.RoleRootUser {
			return true, s.userAuthorization.ClearPermissions(tx, id)
		}
		return false, nil
	}
	if actor.Role != common.RoleRootUser {
		return false, errors.New("only root can update admin permissions")
	}
	if role < common.RoleAdminUser {
		return true, s.userAuthorization.ClearPermissions(tx, id)
	}
	return true, s.userAuthorization.SetPermissions(tx, id, permissions)
}

func userResponse(user *entity.User) *contract.UserResponse {
	result := &contract.UserResponse{Id: user.Id, Username: user.Username, DisplayName: user.DisplayName, Role: user.Role, Status: user.Status, Email: user.Email,
		GitHubId: user.GitHubId, DiscordId: user.DiscordId, OidcId: user.OidcId, WeChatId: user.WeChatId, TelegramId: user.TelegramId,
		Quota: user.Quota, UsedQuota: user.UsedQuota, RequestCount: user.RequestCount, Group: user.Group, AffCode: user.AffCode, AffCount: user.AffCount,
		AffQuota: user.AffQuota, AffHistoryQuota: user.AffHistoryQuota, InviterId: user.InviterId, LinuxDOId: user.LinuxDOId, Setting: user.Setting, Remark: user.Remark,
		StripeCustomer: user.StripeCustomer, CreatedAt: user.CreatedAt, LastLoginAt: user.LastLoginAt}
	if user.DeletedAt.Valid {
		result.DeletedAt = &user.DeletedAt.Time
	}
	return result
}
