package repo

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOAuthAccountBound = errors.New("this OAuth account is already bound to another user")

type Bindings struct{ db *gorm.DB }

func NewBindings(db *gorm.DB) *Bindings { return &Bindings{db: db} }
func (r *Bindings) List(ctx context.Context, userID int) ([]contract.UserOAuthBindingResponse, error) {
	rows := make([]contract.UserOAuthBindingResponse, 0)
	err := r.db.WithContext(ctx).Model(&entity.UserOAuthBinding{}).
		Select("user_oauth_bindings.provider_id, custom_oauth_providers.name AS provider_name, custom_oauth_providers.slug AS provider_slug, custom_oauth_providers.icon AS provider_icon, user_oauth_bindings.provider_user_id").
		Joins("JOIN custom_oauth_providers ON custom_oauth_providers.id = user_oauth_bindings.provider_id").
		Where("user_oauth_bindings.user_id = ?", userID).Order("user_oauth_bindings.id asc").Scan(&rows).Error
	return rows, err
}
func (r *Bindings) Owner(ctx context.Context, providerID int, subject string) (*entity.User, error) {
	var binding entity.UserOAuthBinding
	if err := r.db.WithContext(ctx).Where("provider_id = ? AND provider_user_id = ?", providerID, subject).First(&binding).Error; err != nil {
		return nil, err
	}
	var user entity.User
	if err := r.db.WithContext(ctx).First(&user, binding.UserId).Error; err != nil {
		return nil, err
	}
	return &user, nil
}
func (r *Bindings) Taken(ctx context.Context, providerID int, subject string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.UserOAuthBinding{}).Where("provider_id = ? AND provider_user_id = ?", providerID, subject).Count(&count).Error
	return count > 0, err
}

// WriteInTransaction keeps the provider and user alive while a binding is added
// or replaced. Provider deletion takes a conflicting lock before checking binds.
func (r *Bindings) WriteInTransaction(ctx context.Context, binding *entity.UserOAuthBinding, replace bool) error {
	if binding.UserId == 0 {
		return errors.New("user ID is required")
	}
	if binding.ProviderId == 0 {
		return errors.New("provider ID is required")
	}
	if binding.ProviderUserId == "" {
		return errors.New("provider user ID is required")
	}
	tx := r.db.WithContext(ctx)
	var provider entity.CustomOAuthProvider
	if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Select("id").First(&provider, binding.ProviderId).Error; err != nil {
		return err
	}
	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Select("id").First(&user, binding.UserId).Error; err != nil {
		return err
	}
	binding.CreatedAt = time.Now()
	if replace {
		tx = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "provider_id"}}, DoUpdates: clause.AssignmentColumns([]string{"provider_user_id"})})
	}
	err := tx.Create(binding).Error
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" && pgError.ConstraintName == "ux_provider_userid" {
		return ErrOAuthAccountBound
	}
	return err
}
func (r *Bindings) Replace(ctx context.Context, userID, providerID int, subject string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return NewBindings(tx).WriteInTransaction(ctx, &entity.UserOAuthBinding{UserId: userID, ProviderId: providerID, ProviderUserId: subject}, true)
	})
}
func (r *Bindings) Delete(ctx context.Context, userID, providerID int) error {
	return r.db.WithContext(ctx).Where("user_id = ? AND provider_id = ?", userID, providerID).Delete(&entity.UserOAuthBinding{}).Error
}
func DeleteUserOAuthBindingsWithTx(tx *gorm.DB, userID int) error {
	return tx.Where("user_id = ?", userID).Delete(&entity.UserOAuthBinding{}).Error
}
