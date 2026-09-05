package identity

import (
	"context"
	"math"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

func (s *Service) UpdateNotificationSettings(ctx context.Context, id int, req contract.NotificationSettingsRequest) error {
	// 验证预警类型
	if req.QuotaWarningType != dto.NotifyTypeEmail && req.QuotaWarningType != dto.NotifyTypeWebhook && req.QuotaWarningType != dto.NotifyTypeBark && req.QuotaWarningType != dto.NotifyTypeGotify {
		return &UserValidationError{Code: "SettingInvalidType"}
	}

	// 验证预警阈值
	if !(req.QuotaWarningThreshold > 0) || math.IsInf(req.QuotaWarningThreshold, 0) {
		return &UserValidationError{Code: "QuotaThresholdGtZero"}
	}

	// 如果是webhook类型,验证webhook地址
	if req.QuotaWarningType == dto.NotifyTypeWebhook {
		if req.WebhookUrl == "" {
			return &UserValidationError{Code: "SettingWebhookEmpty"}
		}
		// 验证URL格式
		if _, err := url.ParseRequestURI(req.WebhookUrl); err != nil {
			return &UserValidationError{Code: "SettingWebhookInvalid"}
		}
	}

	// 如果是邮件类型，验证邮箱地址
	if req.QuotaWarningType == dto.NotifyTypeEmail && req.NotificationEmail != "" {
		// 验证邮箱格式
		if !strings.Contains(req.NotificationEmail, "@") {
			return &UserValidationError{Code: "SettingEmailInvalid"}
		}
	}

	// 如果是Bark类型，验证Bark URL
	if req.QuotaWarningType == dto.NotifyTypeBark {
		if req.BarkUrl == "" {
			return &UserValidationError{Code: "SettingBarkUrlEmpty"}
		}
		// 验证URL格式
		if _, err := url.ParseRequestURI(req.BarkUrl); err != nil {
			return &UserValidationError{Code: "SettingBarkUrlInvalid"}
		}
		// 检查是否是HTTP或HTTPS
		if !strings.HasPrefix(req.BarkUrl, "https://") && !strings.HasPrefix(req.BarkUrl, "http://") {
			return &UserValidationError{Code: "SettingUrlMustHttp"}
		}
	}

	// 如果是Gotify类型，验证Gotify URL和Token
	if req.QuotaWarningType == dto.NotifyTypeGotify {
		if req.GotifyUrl == "" {
			return &UserValidationError{Code: "SettingGotifyUrlEmpty"}
		}
		if req.GotifyToken == "" {
			return &UserValidationError{Code: "SettingGotifyTokenEmpty"}
		}
		// 验证URL格式
		if _, err := url.ParseRequestURI(req.GotifyUrl); err != nil {
			return &UserValidationError{Code: "SettingGotifyUrlInvalid"}
		}
		// 检查是否是HTTP或HTTPS
		if !strings.HasPrefix(req.GotifyUrl, "https://") && !strings.HasPrefix(req.GotifyUrl, "http://") {
			return &UserValidationError{Code: "SettingUrlMustHttp"}
		}
	}

	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		settings := user.GetSetting()
		settings.NotifyType = req.QuotaWarningType
		settings.QuotaWarningThreshold = req.QuotaWarningThreshold
		settings.AcceptUnsetRatioModel = req.AcceptUnsetModelRatioModel
		settings.RecordIpLog = req.RecordIpLog
		if user.Role >= common.RoleAdminUser && req.UpstreamModelUpdateNotifyEnabled != nil {
			settings.UpstreamModelUpdateNotifyEnabled = *req.UpstreamModelUpdateNotifyEnabled
		}
		// Clear inactive notification credentials while retaining other account preferences.
		settings.WebhookUrl, settings.WebhookSecret, settings.NotificationEmail, settings.BarkUrl = "", "", "", ""
		settings.GotifyUrl, settings.GotifyToken, settings.GotifyPriority = "", "", 0
		// 如果是webhook类型,添加webhook相关设置
		if req.QuotaWarningType == dto.NotifyTypeWebhook {
			settings.WebhookUrl = req.WebhookUrl
			if req.WebhookSecret != "" {
				settings.WebhookSecret = req.WebhookSecret
			}
		}

		// 如果提供了通知邮箱，添加到设置中
		if req.QuotaWarningType == dto.NotifyTypeEmail && req.NotificationEmail != "" {
			settings.NotificationEmail = req.NotificationEmail
		}

		// 如果是Bark类型，添加Bark URL到设置中
		if req.QuotaWarningType == dto.NotifyTypeBark {
			settings.BarkUrl = req.BarkUrl
		}

		// 如果是Gotify类型，添加Gotify配置到设置中
		if req.QuotaWarningType == dto.NotifyTypeGotify {
			settings.GotifyUrl = req.GotifyUrl
			settings.GotifyToken = req.GotifyToken
			// Gotify优先级范围0-10，超出范围则使用默认值5
			if req.GotifyPriority < 0 || req.GotifyPriority > 10 {
				settings.GotifyPriority = 5
			} else {
				settings.GotifyPriority = req.GotifyPriority
			}
		}

		user.SetSetting(settings)
		return users.Update(user, map[string]any{"setting": user.Setting})
	})
	if err == nil {
		err = s.userSecurity.PublishAuth(id)
	}
	if err != nil {
		return &UserValidationError{Code: "update_failed"}
	}
	return nil
}

func (s *Service) BillingPreference(ctx context.Context, id int) (string, error) {
	user, err := s.users.Get(ctx, id, false)
	if err != nil {
		return "", err
	}
	return common.NormalizeBillingPreference(user.GetSetting().BillingPreference), nil
}

func (s *Service) UpdateBillingPreference(ctx context.Context, id int, value string) (string, error) {
	preference := common.NormalizeBillingPreference(value)
	err := s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		user, err := users.Lock(id, false)
		if err != nil {
			return err
		}
		settings := user.GetSetting()
		settings.BillingPreference = preference
		user.SetSetting(settings)
		return users.Update(user, map[string]any{"setting": user.Setting})
	})
	if err != nil {
		return "", err
	}
	if err := s.userSecurity.PublishAuth(id); err != nil {
		return "", err
	}
	return preference, nil
}

func (s *Service) CheckoutBuyer(ctx context.Context, id int) (*contract.CheckoutBuyer, error) {
	user, err := s.users.Get(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return &contract.CheckoutBuyer{ID: user.Id, Username: user.Username, Email: user.Email, StripeCustomer: user.StripeCustomer, Group: user.Group}, nil
}
