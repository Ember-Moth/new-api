package repo

import (
	"context"
	"errors"

	"gorm.io/gorm/clause"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
)

type Providers struct{ db *gorm.DB }

func NewProviders(db *gorm.DB) *Providers { return &Providers{db: db} }

func (r *Providers) All(ctx context.Context, enabledOnly bool) ([]*entity.CustomOAuthProvider, error) {
	query := r.db.WithContext(ctx).Order("id asc")
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var providers []*entity.CustomOAuthProvider
	err := query.Find(&providers).Error
	return providers, err
}

func (r *Providers) Get(ctx context.Context, id int) (*entity.CustomOAuthProvider, error) {
	var provider entity.CustomOAuthProvider
	if err := r.db.WithContext(ctx).First(&provider, id).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

func (r *Providers) BySlug(ctx context.Context, slug string) (*entity.CustomOAuthProvider, error) {
	var provider entity.CustomOAuthProvider
	if err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&provider).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

func (r *Providers) SlugTaken(ctx context.Context, slug string, id int) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.CustomOAuthProvider{}).Where("slug = ? AND id <> ?", slug, id).Count(&count).Error
	return count > 0, err
}

func (r *Providers) Save(ctx context.Context, provider *entity.CustomOAuthProvider, create bool) error {
	if create {
		return r.db.WithContext(ctx).Create(provider).Error
	}
	return r.db.WithContext(ctx).Save(provider).Error
}

func (r *Providers) DeleteUnbound(ctx context.Context, id int) (*entity.CustomOAuthProvider, error) {
	var provider entity.CustomOAuthProvider
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&provider, id).Error; err != nil {
			return errors.New("未找到该 OAuth 提供商")
		}
		var count int64
		if err := tx.Model(&entity.UserOAuthBinding{}).Where("provider_id = ?", id).Count(&count).Error; err != nil {
			return errors.New("检查用户绑定时发生错误，请稍后重试")
		}
		if count > 0 {
			return errors.New("该 OAuth 提供商还有用户绑定，无法删除。请先解除所有用户绑定。")
		}
		return tx.Delete(&provider).Error
	})
	return &provider, err
}
