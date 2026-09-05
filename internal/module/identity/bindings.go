package identity

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/repo"
	"gorm.io/gorm"
)

const ExternalIdentityProviderTelegram = entity.ExternalIdentityProviderTelegram

var ErrExternalIdentityAlreadyClaimed = entity.ErrExternalIdentityAlreadyClaimed
var ErrOAuthAccountBound = repo.ErrOAuthAccountBound

func (s *Service) OAuthBindings(ctx context.Context, actor contract.UserActor, userID int, administrative bool) ([]contract.UserOAuthBindingResponse, error) {
	if actor.ID == 0 {
		return nil, errors.New("未登录")
	}
	if administrative {
		user, err := s.users.Get(ctx, userID, false)
		if err != nil {
			return nil, err
		}
		if !CanManageUserRole(actor.Role, user.Role) {
			return nil, errors.New("no permission")
		}
	} else {
		userID = actor.ID
	}
	return s.bindings.List(ctx, userID)
}
func (s *Service) UnbindOAuth(ctx context.Context, actor contract.UserActor, userID, providerID int, administrative bool) error {
	if actor.ID == 0 {
		return errors.New("未登录")
	}
	if !administrative {
		return s.bindings.Delete(ctx, actor.ID, providerID)
	}
	return s.users.Transaction(ctx, func(users *repo.Users, tx *gorm.DB) error {
		target, err := users.Lock(userID, false)
		if err != nil {
			return err
		}
		if !CanManageUserRole(actor.Role, target.Role) {
			return errors.New("no permission")
		}
		return repo.NewBindings(tx).Delete(ctx, userID, providerID)
	})
}
func (s *Service) UserByOAuthBinding(ctx context.Context, providerID int, subject string) (*entity.User, error) {
	return s.bindings.Owner(ctx, providerID, subject)
}
func (s *Service) IsProviderUserIDTaken(ctx context.Context, providerID int, subject string) (bool, error) {
	return s.bindings.Taken(ctx, providerID, subject)
}
func (s *Service) SetOAuthBinding(ctx context.Context, userID, providerID int, subject string) error {
	return s.bindings.Replace(ctx, userID, providerID, subject)
}
func CreateUserOAuthBindingWithTx(tx *gorm.DB, binding *entity.UserOAuthBinding) error {
	if tx == nil {
		return errors.New("database transaction is required")
	}
	return repo.NewBindings(tx).WriteInTransaction(tx.Statement.Context, binding, false)
}
func DeleteUserOAuthBindingsWithTx(tx *gorm.DB, userID int) error {
	return repo.DeleteUserOAuthBindingsWithTx(tx, userID)
}
func ClaimExternalIdentityWithTx(tx *gorm.DB, provider, subject string, userID int) error {
	return repo.ClaimExternalIdentityWithTx(tx, provider, subject, userID)
}
func ReleaseExternalIdentityWithTx(tx *gorm.DB, provider string, userID int) error {
	return repo.ReleaseExternalIdentityWithTx(tx, provider, userID)
}
func ReleaseAllExternalIdentitiesWithTx(tx *gorm.DB, userID int) error {
	return repo.ReleaseAllExternalIdentitiesWithTx(tx, userID)
}
