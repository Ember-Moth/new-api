package identity

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
)

func (s *Service) SetupTwoFA(ctx context.Context, userID int) (*contract.TwoFASetup, error) {
	user, err := s.users.Get(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	key, err := common.GenerateTOTPSecret(user.Username)
	if err != nil {
		common.SysLog("生成TOTP密钥失败: " + err.Error())
		return nil, errors.New("生成2FA密钥失败")
	}
	codes, err := common.GenerateBackupCodes()
	if err != nil {
		common.SysLog("生成备用码失败: " + err.Error())
		return nil, errors.New("生成备用码失败")
	}
	factor := &entity.TwoFA{UserId: userID, Secret: key.Secret()}
	if err := s.factors.EnrollPending(ctx, factor, codes); err != nil {
		return nil, err
	}
	if s.twoFAEvent != nil {
		s.twoFAEvent(userID, "开始设置两步验证")
	}
	return &contract.TwoFASetup{Secret: key.Secret(), QRCodeData: common.GenerateQRCodeData(key.Secret(), user.Username), BackupCodes: codes}, nil
}

func (s *Service) TwoFAStatus(ctx context.Context, userID int) (*contract.TwoFAStatus, error) {
	factor, err := s.factors.GetTwoFAByUserId(ctx, userID)
	if err != nil {
		return nil, err
	}
	status := &contract.TwoFAStatus{}
	if factor == nil {
		return status, nil
	}
	status.Enabled, status.Locked = factor.IsEnabled, factor.IsLocked()
	if factor.IsEnabled {
		count, err := s.factors.GetUnusedBackupCodeCount(ctx, userID)
		if err != nil {
			common.SysLog("获取备用码数量失败: " + err.Error())
		} else {
			status.BackupCodesRemaining = &count
		}
	}
	return status, nil
}

func (s *Service) requireTwoFASession(ctx context.Context, userID int, session *contract.AuthIdentity) error {
	if session == nil || session.UserID != userID || session.SessionID == "" || session.SessionVersion <= 0 {
		return ErrSessionRequired
	}
	user, err := s.users.Get(ctx, userID, false)
	if err != nil {
		return err
	}
	if user.AuthVersion != session.UserAuthVersion || user.Status != common.UserStatusEnabled {
		return ErrSessionRevoked
	}
	return nil
}

func (s *Service) EnableTwoFA(ctx context.Context, userID int, code string, session *contract.AuthIdentity) (*contract.AuthBundle, error) {
	if err := s.requireTwoFASession(ctx, userID, session); err != nil {
		return nil, err
	}
	factor, err := s.factors.GetTwoFAByUserId(ctx, userID)
	if err != nil {
		return nil, err
	}
	if factor == nil {
		return nil, errors.New("请先完成2FA初始化设置")
	}
	if factor.IsEnabled {
		return nil, errors.New("2FA已经启用")
	}
	clean, err := common.ValidateNumericCode(code)
	if err != nil {
		return nil, err
	}
	if !common.ValidateTOTPCode(factor.Secret, clean) {
		return nil, errors.New("验证码或备用码错误，请重试")
	}
	if err := s.factors.EnableWithAuthVersion(ctx, factor, factorSessionVersion(session)); err != nil {
		return nil, err
	}
	bundle, err := s.userSecurity.AdvanceCurrentSession(*session, "twofa_enabled")
	if err != nil {
		return nil, err
	}
	if s.twoFAEvent != nil {
		s.twoFAEvent(userID, "成功启用两步验证")
	}
	return bundle, nil
}

func (s *Service) DisableTwoFA(ctx context.Context, userID int, code string, session *contract.AuthIdentity) (*contract.AuthBundle, error) {
	if err := s.requireTwoFASession(ctx, userID, session); err != nil {
		return nil, err
	}
	factor, err := s.factors.GetTwoFAByUserId(ctx, userID)
	if err != nil {
		return nil, err
	}
	if factor == nil || !factor.IsEnabled {
		return nil, errors.New("用户未启用2FA")
	}
	valid := false
	if clean, err := common.ValidateNumericCode(code); err == nil {
		valid, _ = s.factors.ValidateTOTPAndUpdateUsage(ctx, factor, clean)
	}
	if !valid {
		valid, err = s.factors.ValidateBackupCodeAndUpdateUsage(ctx, factor, code)
		if err != nil {
			return nil, err
		}
	}
	if !valid {
		return nil, errors.New("验证码或备用码错误，请重试")
	}
	if err := s.factors.DisableTwoFAWithAuthVersion(ctx, userID, factorSessionVersion(session)); err != nil {
		return nil, err
	}
	bundle, err := s.userSecurity.AdvanceCurrentSession(*session, "twofa_disabled")
	if err != nil {
		return nil, err
	}
	if s.twoFAEvent != nil {
		s.twoFAEvent(userID, "禁用两步验证")
	}
	return bundle, nil
}

func (s *Service) RegenerateTwoFABackupCodes(ctx context.Context, userID int, code string, session *contract.AuthIdentity) (*contract.TwoFARecoveryRotation, error) {
	if err := s.requireTwoFASession(ctx, userID, session); err != nil {
		return nil, err
	}
	factor, err := s.factors.GetTwoFAByUserId(ctx, userID)
	if err != nil {
		return nil, err
	}
	if factor == nil || !factor.IsEnabled {
		return nil, errors.New("用户未启用2FA")
	}
	clean, err := common.ValidateNumericCode(code)
	if err != nil {
		return nil, err
	}
	valid, err := s.factors.ValidateTOTPAndUpdateUsage(ctx, factor, clean)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, errors.New("验证码或备用码错误，请重试")
	}
	codes, err := common.GenerateBackupCodes()
	if err != nil {
		return nil, errors.New("生成备用码失败")
	}
	if err := s.factors.ReplaceBackupCodesWithAuthVersion(ctx, userID, codes, factorSessionVersion(session)); err != nil {
		common.SysLog("保存备用码失败: " + err.Error())
		return nil, errors.New("保存备用码失败")
	}
	bundle, err := s.userSecurity.AdvanceCurrentSession(*session, "twofa_backup_codes_regenerated")
	if err != nil {
		return nil, err
	}
	if s.twoFAEvent != nil {
		s.twoFAEvent(userID, "重新生成两步验证备用码")
	}
	return &contract.TwoFARecoveryRotation{AuthBundle: bundle, BackupCodes: codes}, nil
}

func (s *Service) TwoFAStats(ctx context.Context) (map[string]any, error) {
	return s.factors.GetTwoFAStats(ctx)
}

func (s *Service) AdminDisableTwoFA(ctx context.Context, actor contract.UserActor, userID int) error {
	if err := s.factors.DisableTwoFAWithAuthVersion(ctx, userID, func(target *entity.User) error {
		if !CanManageUserRole(actor.Role, target.Role) {
			return errors.New("无权操作同级或更高级用户的2FA设置")
		}
		return nil
	}); err != nil {
		if errors.Is(err, entity.ErrTwoFANotEnabled) {
			return errors.New("用户未启用2FA")
		}
		return err
	}
	return s.userSecurity.RevokeSessions(userID, "admin_twofa_disabled")
}

// factorSessionVersion rechecks the authenticated user version under the mutation lock.
func factorSessionVersion(session *contract.AuthIdentity) func(*entity.User) error {
	return func(user *entity.User) error {
		if user.AuthVersion != session.UserAuthVersion || user.Status != common.UserStatusEnabled {
			return ErrSessionRevoked
		}
		return nil
	}
}
