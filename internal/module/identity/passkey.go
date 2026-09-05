package identity

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/go-webauthn/webauthn/webauthn"
)

func (s *Service) AuthenticatedPasskeyUser(ctx context.Context, id int) (*entity.User, error) {
	if id == 0 {
		return nil, errors.New("未登录")
	}
	user, err := s.users.Get(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, errors.New("该用户已被禁用")
	}
	return user, nil
}
func (s *Service) PasskeyByUserID(ctx context.Context, id int) (*entity.PasskeyCredential, error) {
	return s.passkeys.GetPasskeyByUserID(ctx, id)
}
func (s *Service) PasskeyByCredentialID(ctx context.Context, id []byte) (*entity.PasskeyCredential, error) {
	return s.passkeys.GetPasskeyByCredentialID(ctx, id)
}
func (s *Service) RecordPasskeyAssertion(ctx context.Context, id int, credential *webauthn.Credential, usedAt time.Time) error {
	return s.passkeys.UpdatePasskeyAssertionState(ctx, id, credential, usedAt)
}
func (s *Service) SaveRegisteredPasskey(ctx context.Context, credential *entity.PasskeyCredential, session contract.AuthIdentity) (*contract.AuthBundle, error) {
	if credential == nil || session.UserID != credential.UserID {
		return nil, ErrSessionRequired
	}
	if err := s.passkeys.UpsertPasskeyCredentialWithAuthVersion(ctx, credential, factorSessionVersion(&session)); err != nil {
		return nil, err
	}
	return s.userSecurity.AdvanceCurrentSession(session, "passkey_registered")
}
func (s *Service) DeletePasskey(ctx context.Context, userID int, session contract.AuthIdentity) (*contract.AuthBundle, error) {
	if session.UserID != userID {
		return nil, ErrSessionRequired
	}
	if err := s.passkeys.DeletePasskeyByUserIDWithAuthVersion(ctx, userID, factorSessionVersion(&session)); err != nil {
		return nil, err
	}
	return s.userSecurity.AdvanceCurrentSession(session, "passkey_deleted")
}
func (s *Service) AdminResetPasskey(ctx context.Context, actor contract.UserActor, id int) (*entity.User, error) {
	user, err := s.users.Get(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if err := s.passkeys.DeletePasskeyByUserIDWithAuthVersion(ctx, id, func(target *entity.User) error {
		if !CanManageUserRole(actor.Role, target.Role) {
			return errors.New("no permission")
		}
		return nil
	}); err != nil {
		if errors.Is(err, entity.ErrPasskeyNotFound) {
			return nil, errors.New("该用户尚未绑定 Passkey")
		}
		return nil, err
	}
	if err := s.userSecurity.RevokeSessions(id, "admin_passkey_reset"); err != nil {
		return nil, err
	}
	return user, nil
}
func (s *Service) PasskeyFlow(ctx context.Context, purpose string, id int, sid, scope string, data *webauthn.SessionData) (string, int64, error) {
	return s.passkeyFlows.CreateSessionDataFlow(ctx, purpose, id, sid, scope, data)
}
func (s *Service) ConsumePasskeyFlow(ctx context.Context, token, purpose string, id int, sid string) (*webauthn.SessionData, string, error) {
	return s.passkeyFlows.PopSessionDataFlow(ctx, token, purpose, id, sid)
}
func (s *Service) PasskeyRequiresTwoFA(ctx context.Context, id int) (bool, error) {
	return s.factors.IsTwoFAEnabled(ctx, id)
}
func (s *Service) IssueSecurityProof(session contract.AuthIdentity, method string, scopes []string) (string, int64, error) {
	return s.userSecurity.IssueProof(session, method, scopes)
}
