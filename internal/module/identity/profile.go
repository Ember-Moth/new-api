package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"gorm.io/gorm"
)

var (
	ErrPasswordUnset    = errors.New("user password is not set")
	ErrOriginalPassword = errors.New("original password is incorrect")
	ErrSessionRequired  = errors.New("当前认证方式不支持安全验证")
	ErrSessionRevoked   = errors.New("login session is revoked")
)

// SelfUserData is the shared dashboard projection for self, login and refresh.
func SelfUserData(user *entity.User, role int, capabilities map[string]map[string]bool) *contract.SelfUserResponse {
	permissions := contract.DashboardPermissions{SidebarSettings: role != common.RoleRootUser, SidebarModules: map[string]any{}, AdminPermissions: capabilities}
	if role == common.RoleAdminUser {
		permissions.SidebarModules["admin"] = map[string]any{"setting": false}
	} else if role != common.RoleRootUser {
		permissions.SidebarModules["admin"] = false
	}
	settings := user.GetSetting()
	return &contract.SelfUserResponse{Id: user.Id, Username: user.Username, DisplayName: user.DisplayName, Role: user.Role, Status: user.Status, Email: user.Email,
		GitHubId: user.GitHubId, DiscordId: user.DiscordId, OidcId: user.OidcId, WeChatId: user.WeChatId, TelegramId: user.TelegramId, Group: user.Group,
		Quota: user.Quota, UsedQuota: user.UsedQuota, RequestCount: user.RequestCount, AffCode: user.AffCode, AffCount: user.AffCount, AffQuota: user.AffQuota,
		AffHistoryQuota: user.AffHistoryQuota, InviterId: user.InviterId, LinuxDOId: user.LinuxDOId, Setting: user.Setting, StripeCustomer: user.StripeCustomer,
		SidebarModules: settings.SidebarModules, Permissions: permissions}
}

func (s *Service) Self(ctx context.Context, actor contract.UserActor) (*contract.SelfUserResponse, error) {
	user, err := s.users.Get(ctx, actor.ID, false)
	if err != nil {
		return nil, err
	}
	return SelfUserData(user, actor.Role, s.userAuthorization.Capabilities(actor.ID, actor.Role)), nil
}

func (s *Service) RotatePersonalAccessToken(ctx context.Context, id int) (string, error) {
	key, err := common.GenerateRandomKey(29 + common.GetRandomInt(4))
	if err != nil {
		common.SysLog("failed to generate key: " + err.Error())
		return "", &UserValidationError{Code: "generate_failed"}
	}
	exists, err := s.users.AccessTokenExists(ctx, key)
	if err != nil {
		return "", err
	}
	if exists {
		return "", &UserValidationError{Code: "uuid_duplicate"}
	}
	if err := s.users.SetAccessToken(ctx, id, key); err != nil {
		return "", err
	}
	return key, nil
}

func (s *Service) AffiliationCode(ctx context.Context, id int) (string, error) {
	var code string
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		code = user.AffCode
		if code != "" {
			return nil
		}
		code = common.GetRandomString(4)
		return users.Update(user, map[string]any{"aff_code": code})
	})
	return code, err
}

func (s *Service) UpdateSelf(ctx context.Context, id int, input contract.SelfUpdateRequest, session *contract.AuthIdentity) (*contract.AuthBundle, error) {
	if input.Preference != "" && input.Preference != "sidebar_modules" && input.Preference != "language" {
		return nil, &UserValidationError{Code: "invalid_params"}
	}
	if input.Preference == "" {
		if err := common.Validate.Struct(input.ProfileInput); err != nil {
			return nil, &UserValidationError{Code: "invalid_input"}
		}
	}
	changingPassword := input.Preference == "" && input.Password != ""
	var password string
	if changingPassword {
		var err error
		password, err = common.Password2Hash(input.Password)
		if err != nil {
			return nil, err
		}
	}
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		if input.Preference != "" {
			if input.PreferenceValue == nil {
				return nil
			}
			settings := user.GetSetting()
			if input.Preference == "sidebar_modules" {
				settings.SidebarModules = *input.PreferenceValue
			} else {
				settings.Language = *input.PreferenceValue
			}
			user.SetSetting(settings)
			return users.Update(user, map[string]any{"setting": user.Setting})
		}
		fields := map[string]any{}
		if input.Username != "" {
			fields["username"] = input.Username
		}
		if input.DisplayName != "" {
			fields["display_name"] = input.DisplayName
		}
		if changingPassword {
			if user.Password == "" {
				return ErrPasswordUnset
			}
			if !common.ValidatePasswordAndHash(input.OriginalPassword, user.Password) {
				return ErrOriginalPassword
			}
			if session == nil || session.UserID != id || session.SessionID == "" || session.SessionVersion <= 0 {
				return ErrSessionRequired
			}
			if session.UserAuthVersion != user.AuthVersion || user.Status != common.UserStatusEnabled {
				return ErrSessionRevoked
			}
			if _, err := s.userSecurity.AdvanceVersion(tx, id); err != nil {
				return err
			}
			fields["password"] = password
		}
		if len(fields) == 0 {
			return nil
		}
		return users.Update(user, fields)
	})
	if err != nil {
		return nil, err
	}
	if err := s.userSecurity.PublishAuth(id); err != nil {
		return nil, err
	}
	if changingPassword {
		return s.userSecurity.AdvanceCurrentSession(*session, "password_changed")
	}
	return nil, nil
}

func (s *Service) DeleteSelf(ctx context.Context, id int) error {
	var version int64
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		if user.Role == common.RoleRootUser {
			return &UserValidationError{Code: "delete_root"}
		}
		version, err = s.userSecurity.AdvanceVersion(tx, id)
		if err != nil {
			return err
		}
		return users.Delete(user, false)
	})
	if err != nil {
		return err
	}
	if err := s.userSecurity.PublishDeletedVersion(id, version); err != nil {
		return err
	}
	if err := s.userSecurity.RevokeSessions(id, "user_deleted"); err != nil {
		return err
	}
	return s.userSecurity.InvalidateUser(id)
}

func (s *Service) BindEmail(ctx context.Context, id int, input contract.BindEmailRequest) error {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if s.verifyEmail == nil || !s.verifyEmail(email, input.Code) {
		return &UserValidationError{Code: "verification_code"}
	}
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		if err := users.LockEmail(email); err != nil {
			return err
		}
		taken, err := users.EmailTaken(email, id)
		if err != nil {
			return err
		}
		if taken {
			return &UserValidationError{Code: "email_taken"}
		}
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		return users.Update(user, map[string]any{"email": email})
	})
	if err != nil {
		return err
	}
	return s.userSecurity.PublishAuth(id)
}
